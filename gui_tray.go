package main

import (
	"fmt"
	"strings"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/elevate"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// closeHidesToTray: the window cross only minimizes to the tray while a service
// keeps the bypass running; without a usable service the app has nothing to
// keep alive and quits.
func closeHidesToTray(service string) bool {
	return service == "running" || service == "starting"
}

// serviceActions says which service commands the tray menu offers for a state.
type serviceActions struct{ Install, Uninstall, Start, Stop, Restart bool }

func actionsFor(service string) serviceActions {
	switch service {
	case "running", "starting":
		return serviceActions{Uninstall: true, Stop: true, Restart: true}
	case "stopped":
		return serviceActions{Uninstall: true, Start: true}
	default: // not_installed, no_access (reinstall fixes ownership), unknown
		return serviceActions{Install: true}
	}
}

func serviceTitle(service string, active bool) string {
	switch service {
	case "running":
		if active {
			return "● Включён"
		}
		return "○ Выключен"
	case "starting":
		return "… Служба запускается"
	case "stopped":
		return "○ Служба остановлена"
	case "no_access":
		return "○ Нет доступа к службе"
	case "not_installed":
		return "○ Служба не установлена"
	}
	return "○ Состояние неизвестно"
}

type providerItem struct {
	ID, Name string
	Selected bool
}

func providerItems(c config.Config) []providerItem {
	items := make([]providerItem, 0, len(c.Providers))
	for _, p := range c.Providers {
		name := p.Name
		if name == "" {
			name = p.ID
		}
		items = append(items, providerItem{ID: p.ID, Name: name, Selected: p.ID == c.ActiveProvider})
	}
	return items
}

// trayModel is everything the tray menu shows; the menu is rebuilt only when
// its signature changes, so an open menu is not replaced under the user.
type trayModel struct {
	Service   string
	Active    bool
	Providers []providerItem
	Autostart bool
}

func (m trayModel) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%v|%v", m.Service, m.Active, m.Autostart)
	for _, p := range m.Providers {
		fmt.Fprintf(&b, "|%s:%s:%v", p.ID, p.Name, p.Selected)
	}
	return b.String()
}

// refreshTray rebuilds the tray menu when the model changed.
func (a *App) refreshTray(st State) {
	if !a.ready.Load() {
		return
	}
	m := trayModel{Service: st.Service, Autostart: a.GetAutostart()}
	if st.Status != nil {
		m.Active = st.Status.Active
	}
	if st.Service == "running" {
		if c, err := a.cl.Config(); err == nil {
			m.Providers = providerItems(c)
		}
	}
	sig := m.signature()
	a.mu.Lock()
	if a.tray == nil || sig == a.traySig {
		a.mu.Unlock()
		return
	}
	a.traySig = sig
	a.mu.Unlock()
	application.InvokeAsync(func() { a.buildTrayMenu(m) })
}

func (a *App) trayError(err error) {
	application.InvokeAsync(func() {
		a.app.Dialog.Error().SetTitle("DoH Client").SetMessage(err.Error()).Show()
	})
}

// forceTrayRefresh drops the cached signature and rebuilds from the real state.
func (a *App) forceTrayRefresh() {
	a.mu.Lock()
	a.traySig = ""
	a.lastActive = nil
	a.mu.Unlock()
	a.refreshTray(a.State())
}

func (a *App) buildTrayMenu(m trayModel) {
	menu := a.app.NewMenu()
	a.label = menu.Add(serviceTitle(m.Service, m.Active)).SetEnabled(false)
	menu.AddSeparator()

	running := m.Service == "running"
	a.toggle = menu.AddCheckbox("Обход геоблокировок", running && m.Active)
	a.toggle.SetEnabled(running)
	a.toggle.OnClick(func(ctx *application.Context) {
		want := ctx.ClickedMenuItem().Checked()
		go func() {
			if _, err := a.SetEnabled(want); err != nil {
				a.forceTrayRefresh()
				a.trayError(err)
			}
		}()
	})

	tunnels := menu.AddSubmenu("Туннель")
	if len(m.Providers) == 0 {
		tunnels.Add("Нет данных (служба не запущена)").SetEnabled(false)
	}
	for _, p := range m.Providers {
		item := tunnels.AddRadio(p.Name, p.Selected)
		id := p.ID
		item.OnClick(func(*application.Context) {
			go func() {
				if err := a.setProvider(id); err != nil {
					a.forceTrayRefresh()
					a.trayError(err)
				}
			}()
		})
	}

	acts := actionsFor(m.Service)
	svcMenu := menu.AddSubmenu("Служба")
	add := func(label, action string, on bool) {
		svcMenu.Add(label).SetEnabled(on).OnClick(func(*application.Context) {
			go func() {
				if err := a.serviceAction(action); err != nil {
					a.trayError(err)
				}
				a.forceTrayRefresh()
			}()
		})
	}
	add("Установить / починить", "install", acts.Install)
	add("Запустить", "start", acts.Start)
	add("Остановить", "stop", acts.Stop)
	add("Перезапустить", "restart", acts.Restart)
	svcMenu.AddSeparator()
	add("Удалить службу", "uninstall", acts.Uninstall)

	auto := menu.AddCheckbox("Автозапуск обхода", m.Autostart)
	auto.OnClick(func(ctx *application.Context) {
		want := ctx.ClickedMenuItem().Checked()
		go func() {
			if err := a.SetAutostart(want); err != nil {
				a.forceTrayRefresh()
				a.trayError(err)
			}
		}()
	})

	menu.AddSeparator()
	menu.Add("Открыть окно").OnClick(func(*application.Context) { a.showWindow() })
	menu.Add("Выход").OnClick(func(*application.Context) { a.app.Quit() })
	a.tray.SetMenu(menu)
}

// setProvider makes id the single active tunnel, like the Providers tab does.
func (a *App) setProvider(id string) error {
	c, err := a.cl.Config()
	if err != nil {
		return err
	}
	if c.ActiveProvider == id {
		return nil
	}
	c.ActiveProvider = id
	_, err = a.cl.SaveConfig(c)
	return err
}

// serviceAction runs one service command; privileged ones go through UAC.
func (a *App) serviceAction(action string) error {
	switch action {
	case "install":
		return a.InstallService()
	case "uninstall":
		return a.UninstallService()
	case "start", "stop", "restart":
		if err := elevate.Run("service", action); err != nil {
			return withCLILog(err)
		}
		return nil
	}
	return fmt.Errorf("неизвестная команда службы: %s", action)
}
