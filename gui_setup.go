package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/paths"
	"github.com/archerpm/doh-client/internal/setup"
)

// SetupStage is one visible step of the one-click setup.
type SetupStage struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok | warn | failed | info
	Detail string `json:"detail,omitempty"`
}

// SetupResult is what the UI shows after QuickSetup.
type SetupResult struct {
	Stages []SetupStage `json:"stages"`
	Ok     bool         `json:"ok"`
}

var setupBusy atomic.Bool

// QuickSetup does everything in one go: install the service if needed (one UAC
// prompt), add the rules of the chosen presets (never touching existing rules),
// turn the bypass on and check that the first preset domain really goes through a
// provider proxy. Stops at the first failed stage and says which one.
func (a *App) QuickSetup(presetIDs []string) (SetupResult, error) {
	if !setupBusy.CompareAndSwap(false, true) {
		return SetupResult{}, fmt.Errorf("настройка уже выполняется")
	}
	defer setupBusy.Store(false)

	var res SetupResult
	add := func(id, title, status, detail string) bool {
		res.Stages = append(res.Stages, SetupStage{ID: id, Title: title, Status: status, Detail: detail})
		return status != "failed"
	}

	var groups []setup.Group
	for _, id := range presetIDs {
		for _, p := range presets {
			if p.ID == id {
				groups = append(groups, setup.Group{Domains: p.Domains, Providers: p.Providers})
			}
		}
	}
	if len(groups) == 0 {
		return res, fmt.Errorf("выберите хотя бы один набор")
	}

	// Only (re)install when the service really is absent or unusable: a service that
	// is merely still starting must be waited for, not stopped and reinstalled.
	switch st := a.State(); st.Service {
	case "running":
		add("service", "Служба", "ok", "уже работает")
	case "starting":
		up := false
		for i := 0; i < 40 && !up; i++ {
			_, err := a.cl.Status()
			up = err == nil
			if !up {
				time.Sleep(250 * time.Millisecond)
			}
		}
		if !up {
			add("service", "Служба", "failed", "служба запущена, но не отвечает — см. "+paths.LogFile())
			return res, nil
		}
		add("service", "Служба", "ok", "запустилась")
	default: // not_installed, stopped, unknown, no_access (installing from this account fixes ownership)
		if err := a.InstallService(); err != nil {
			add("service", "Установка службы", "failed", err.Error())
			return res, nil
		}
		add("service", "Установка службы", "ok", "служба установлена и запущена")
	}

	st, ok := setupRulesAndEnable(a.cl, groups, &res)
	if !ok {
		return res, nil
	}
	a.updateTray(st.Active)
	switch {
	case st.LastError != "":
		add("enable", "Включение обхода", "warn", "обход включён, но: "+st.LastError)
	case st.Mode == config.ModeHosts:
		add("enable", "Включение обхода", "ok", "записи добавлены в файл hosts (DNS системы не менялся)")
	default:
		add("enable", "Включение обхода", "ok", "локальный DNS запущен, DNS системы переключён")
	}
	for _, w := range st.Warnings {
		add("warning", "Внимание", "warn", w)
	}

	// Verify through the diagnosis, which follows the real route of each domain
	// (its rule, active provider and connection to the address actually returned),
	// for the first domain of each chosen preset, in parallel.
	var probes []string
	for _, g := range groups {
		if len(g.Domains) > 0 && len(probes) < 3 {
			probes = append(probes, g.Domains[0])
		}
	}
	type verdict struct {
		dom, code, text string
		err             error
	}
	out := make([]verdict, len(probes))
	var wg sync.WaitGroup
	for i, d := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := a.cl.Diagnose(d)
			out[i] = verdict{d, r.Verdict.Code, r.Verdict.Action.Text, err}
		}()
	}
	wg.Wait()
	okN, bad := 0, []string{}
	for _, v := range out {
		switch {
		case v.err != nil:
			bad = append(bad, v.dom+": не удалось проверить ("+v.err.Error()+")")
		case v.code == "transport_ok":
			okN++
		case v.code == "inconclusive":
			bad = append(bad, v.dom+": проверка не завершилась, повторите позже")
		default:
			bad = append(bad, v.dom+": "+v.text)
		}
	}
	switch {
	case len(probes) == 0:
	case len(bad) == 0:
		add("verify", "Проверка", "ok", fmt.Sprintf("соединение с адресами подтверждено для %d из %d доменов", okN, len(probes)))
	default:
		add("verify", "Проверка", "warn", "настройка применена, но доступ не подтверждён: "+strings.Join(bad, "; "))
	}
	add("browser", "Браузер", "info", "Отключите «Безопасный DNS» в Chrome/Edge/Firefox, иначе браузер обходит клиент своим DoH")
	res.Ok = true
	return res, nil
}

// setupClient is the part of the API used by the compensating setup transaction.
type setupClient interface {
	Config() (config.Config, error)
	Status() (daemon.Status, error)
	SaveConfig(config.Config) (config.Config, error)
	SetEnabled(bool) (daemon.Status, error)
}

// setupRulesAndEnable snapshots actual activation independently of the persisted
// Enabled flag. Compensation attempts both restores even when the first fails.
func setupRulesAndEnable(cl setupClient, groups []setup.Group, res *SetupResult) (daemon.Status, bool) {
	add := func(id, title, status, detail string) {
		res.Stages = append(res.Stages, SetupStage{ID: id, Title: title, Status: status, Detail: detail})
	}
	cfg, err := cl.Config()
	if err != nil {
		add("rules", "Правила", "failed", err.Error())
		return daemon.Status{}, false
	}
	initial := cfg.Clone()
	before, err := cl.Status()
	if err != nil {
		add("rules", "Правила", "failed", "не удалось сохранить исходное состояние: "+err.Error())
		return daemon.Status{}, false
	}
	n := setup.MergeRules(&cfg, groups)
	if n > 0 {
		if _, err := cl.SaveConfig(cfg); err != nil {
			add("rules", "Правила", "failed", err.Error())
			return daemon.Status{}, false
		}
		add("rules", "Правила", "ok", fmt.Sprintf("добавлено правил: %d (существующие не изменены)", n))
	} else {
		add("rules", "Правила", "ok", "все домены наборов уже есть в правилах")
	}
	st, err := cl.SetEnabled(true)
	if err == nil && st.Active {
		return st, true
	}
	if err == nil {
		err = fmt.Errorf("служба не включила обход: %s", st.LastError)
	}
	add("enable", "Включение обхода", "failed", err.Error())
	var rollbackErrors []error
	if _, err := cl.SaveConfig(*initial); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("конфигурация: %w", err))
	}
	restored, err := cl.SetEnabled(before.Active)
	if err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("состояние обхода: %w", err))
	} else if restored.Active != before.Active {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("состояние обхода не восстановлено: active=%v", restored.Active))
	}
	if err := errors.Join(rollbackErrors...); err != nil {
		add("rollback", "Откат настройки", "failed", err.Error())
	} else {
		add("rollback", "Откат настройки", "ok", "исходные правила и состояние обхода восстановлены")
	}
	return restored, false
}
