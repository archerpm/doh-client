package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/setup"
)

type fakeSetupClient struct {
	cfg                 config.Config
	initialActive       bool
	statusErr           error
	enableErr           error
	enableInactive      bool
	rollbackSaveErr     error
	rollbackEnableErr   error
	rollbackWrongActive bool
	saves               []config.Config
	enables             []bool
}

func (f *fakeSetupClient) Config() (config.Config, error) { return *f.cfg.Clone(), nil }
func (f *fakeSetupClient) Status() (daemon.Status, error) {
	return daemon.Status{Active: f.initialActive}, f.statusErr
}
func (f *fakeSetupClient) SaveConfig(c config.Config) (config.Config, error) {
	f.saves = append(f.saves, *c.Clone())
	if len(f.saves) == 2 && f.rollbackSaveErr != nil {
		return config.Config{}, f.rollbackSaveErr
	}
	f.cfg = *c.Clone()
	return c, nil
}
func (f *fakeSetupClient) SetEnabled(on bool) (daemon.Status, error) {
	f.enables = append(f.enables, on)
	if len(f.enables) == 1 {
		if f.enableErr != nil {
			return daemon.Status{}, f.enableErr
		}
		return daemon.Status{Active: !f.enableInactive, LastError: "partial DNS warning"}, nil
	}
	if f.rollbackEnableErr != nil {
		return daemon.Status{}, f.rollbackEnableErr
	}
	active := on
	if f.rollbackWrongActive {
		active = !on
	}
	return daemon.Status{Active: active}, nil
}

func TestSetupEnableFailureRollsBackConfigAndActualActive(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, inactive := range []bool{false, true} {
			f := &fakeSetupClient{cfg: *config.Default(), initialActive: active, enableInactive: inactive}
			// Persisted Enabled differs from actual Active (failed startup / restore).
			f.cfg.Enabled = !active
			f.cfg.Rules = []config.Rule{{Domain: "existing.test", Enabled: false}}
			original := f.cfg.Clone()
			if !inactive {
				f.enableErr = errors.New("port busy")
			}
			var result SetupResult
			_, ok := setupRulesAndEnable(f, []setup.Group{{Domains: []string{"new.test"}}}, &result)
			if ok || result.Ok || len(f.saves) != 2 || !reflect.DeepEqual(f.cfg, *original) || !reflect.DeepEqual(f.enables, []bool{true, active}) {
				t.Fatalf("active=%v inactive=%v: ok=%v cfg=%+v calls=%v", active, inactive, ok, f.cfg, f.enables)
			}
			if len(f.saves[0].Rules) != 2 || len(result.Stages) != 3 || result.Stages[1].ID != "enable" || result.Stages[1].Status != "failed" || result.Stages[2].ID != "rollback" || result.Stages[2].Status != "ok" {
				t.Fatalf("stages=%+v saves=%+v", result.Stages, f.saves)
			}
		}
	}
}

func TestSetupRollbackFailuresAreSeparateStage(t *testing.T) {
	for _, failure := range []string{"save", "enable", "both", "wrong active"} {
		t.Run(failure, func(t *testing.T) {
			f := &fakeSetupClient{cfg: *config.Default(), enableErr: errors.New("enable failed")}
			if failure == "save" || failure == "both" {
				f.rollbackSaveErr = errors.New("rollback save failed")
			}
			if failure == "enable" || failure == "both" {
				f.rollbackEnableErr = errors.New("rollback enable failed")
			}
			f.rollbackWrongActive = failure == "wrong active"
			var result SetupResult
			_, ok := setupRulesAndEnable(f, []setup.Group{{Domains: []string{"new.test"}}}, &result)
			if ok || len(f.saves) != 2 || len(f.enables) != 2 || len(result.Stages) != 3 {
				t.Fatalf("result=%+v", result)
			}
			stage := result.Stages[2]
			if stage.ID != "rollback" || stage.Status != "failed" || result.Stages[1].ID != "enable" || result.Stages[1].Detail != "enable failed" {
				t.Fatalf("stages=%+v", result.Stages)
			}
			if f.rollbackSaveErr != nil && !strings.Contains(stage.Detail, f.rollbackSaveErr.Error()) {
				t.Fatalf("lost save error: %+v", stage)
			}
			if f.rollbackEnableErr != nil && !strings.Contains(stage.Detail, f.rollbackEnableErr.Error()) {
				t.Fatalf("lost enable error: %+v", stage)
			}
		})
	}
}

func TestSetupSuccessAndFailedSnapshot(t *testing.T) {
	for _, failSnapshot := range []bool{false, true} {
		f := &fakeSetupClient{cfg: *config.Default()}
		if failSnapshot {
			f.statusErr = errors.New("status unavailable")
		}
		var result SetupResult
		st, ok := setupRulesAndEnable(f, []setup.Group{{Domains: []string{"new.test"}}}, &result)
		if failSnapshot {
			if ok || len(f.saves) != 0 || len(f.enables) != 0 || result.Stages[0].Status != "failed" {
				t.Fatalf("side effects after failed snapshot: %+v", result)
			}
		} else if !ok || !st.Active || len(f.saves) != 1 || !reflect.DeepEqual(f.enables, []bool{true}) {
			t.Fatalf("success: %+v %v", result, ok)
		}
	}
}
