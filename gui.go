package main

import (
	"embed"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/archerpm/doh-client/internal/api"
	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/elevate"
	"github.com/archerpm/doh-client/internal/paths"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/archerpm/doh-client/internal/svc"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed frontend
var frontendFS embed.FS

//go:embed icons/tray-on.png
var trayOn []byte

//go:embed icons/tray-off.png
var trayOff []byte

//go:embed icons/tray-template.png
var trayTemplate []byte

//go:embed icons/tray-template-off.png
var trayTemplateOff []byte

// App is bound to the frontend; JS calls it as "main.App.<Method>".
type App struct {
	app    *application.App
	win    *application.WebviewWindow
	cl     *api.Client
	tray   *application.SystemTray
	toggle *application.MenuItem
	label  *application.MenuItem

	mu         sync.Mutex
	lastActive *bool
	traySig    string // signature of the tray menu last built
	ready      atomic.Bool
}

// State is what the UI polls.
type State struct {
	Service string         `json:"service"` // running | stopped | not_installed | unknown
	Status  *daemon.Status `json:"status,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func (a *App) State() State {
	st, err := a.cl.Status()
	if err == nil {
		a.updateTray(st.Active)
		return State{Service: "running", Status: &st}
	}
	a.updateTray(false)
	if errors.Is(err, api.ErrNoAccess) {
		return State{Service: "no_access", Error: err.Error()}
	}
	s := State{Service: svc.Status()}
	if !errors.Is(err, api.ErrNoService) {
		s.Error = err.Error()
	}
	if s.Service == "running" { // service up but API not reachable yet / token unreadable
		s.Service = "starting"
	}
	return s
}

func (a *App) GetConfig() (config.Config, error) { return a.cl.Config() }

func (a *App) SaveConfig(c config.Config) (config.Config, error) { return a.cl.SaveConfig(c) }

func (a *App) SetEnabled(on bool) (daemon.Status, error) {
	st, err := a.cl.SetEnabled(on)
	if err == nil {
		a.updateTray(st.Active)
		go a.forceTrayRefresh()
	}
	return st, err
}

func (a *App) TestDomain(domain string) ([]daemon.ProbeResult, error) { return a.cl.Test(domain) }

// TestProviders checks the bypass of every provider (only while the bypass is off).
func (a *App) TestProviders() ([]daemon.ProviderTest, error) { return a.cl.TestProviders() }

func (a *App) Log(after uint64) ([]resolver.Entry, error) { return a.cl.Log(after) }

func (a *App) FlushCache() error { return a.cl.Flush() }

func (a *App) ReapplyDNS() error { return a.cl.ReapplyDNS() }

func (a *App) Presets() []Preset { return presets }

func (a *App) DefaultProviders() []config.Provider { return config.BuiltinProviders() }

// InstallService installs/repairs the service with an elevation prompt and waits for the API.
func (a *App) InstallService() error {
	if err := elevate.Run("service", "install", "--owner", api.CurrentOwnerID()); err != nil {
		return withCLILog(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := a.cl.Status(); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("служба установлена, но не отвечает — см. " + paths.LogFile())
}

func (a *App) UninstallService() error {
	if err := elevate.Run("service", "uninstall"); err != nil {
		return withCLILog(err)
	}
	a.updateTray(false)
	return nil
}

func withCLILog(err error) error {
	if b, rerr := os.ReadFile(filepath.Join(paths.StateDir(), "cli-error.log")); rerr == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return errors.New(err.Error() + "\n" + s)
		}
	}
	return err
}

func (a *App) GetAutostart() bool {
	on, _ := a.app.Autostart.IsEnabled()
	return on
}

func (a *App) SetAutostart(on bool) error {
	if on {
		return a.app.Autostart.EnableWithOptions(application.AutostartOptions{Arguments: []string{"--minimized"}})
	}
	return a.app.Autostart.Disable()
}

type Info struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	StateDir  string `json:"state_dir"`
	LogFile   string `json:"log_file"`
	Installed string `json:"installed"`
}

func (a *App) Info() Info {
	return Info{Version: version, OS: runtime.GOOS + "/" + runtime.GOARCH, StateDir: paths.StateDir(), LogFile: paths.LogFile(), Installed: paths.InstallPath()}
}

func (a *App) Quit() { a.app.Quit() }

func (a *App) showWindow() {
	a.win.Show()
	a.win.Focus()
}

// updateTray refreshes icon and menu only on change.
func (a *App) updateTray(active bool) {
	if !a.ready.Load() {
		return
	}
	a.mu.Lock()
	if a.tray == nil || (a.lastActive != nil && *a.lastActive == active) {
		a.mu.Unlock()
		return
	}
	a.lastActive = &active
	a.mu.Unlock()
	application.InvokeAsync(func() {
		if runtime.GOOS == "darwin" {
			if active {
				a.tray.SetTemplateIcon(trayTemplate)
			} else {
				a.tray.SetTemplateIcon(trayTemplateOff)
			}
		} else if active {
			a.tray.SetIcon(trayOn)
		} else {
			a.tray.SetIcon(trayOff)
		}
		if active {
			a.tray.SetTooltip("DoH Client — включён")
		} else {
			a.tray.SetTooltip("DoH Client — выключен")
		}
	})
}

func runGUI() {
	prepareWebKitSandbox() // must run before WebKit initializes
	assets, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	a := &App{cl: api.NewClient(paths.APIAddr, paths.TokenFile())}
	single := &application.SingleInstanceOptions{
		UniqueID: "com.archerpm.doh-client",
		OnSecondInstanceLaunch: func(application.SecondInstanceData) {
			application.InvokeAsync(a.showWindow)
		},
	}
	// Linux: single-instance needs a session bus (Wails aborts without one); the
	// tray needs a StatusNotifierWatcher, otherwise closing the window must quit.
	hasBus, hasTray := trayAvailable()
	if !hasBus {
		single = nil
	}
	app := application.New(application.Options{
		Name:        "DoH Client",
		Description: "DNS-over-HTTPS client for bypassing geo-blocking",
		Services:    []application.Service{application.NewService(a)},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory, // tray app, no Dock icon
		},
		SingleInstance: single,
	})
	a.app = app

	minimized := slices.Contains(os.Args[1:], "--minimized")
	a.win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "DoH Client",
		Width:            1040,
		Height:           720,
		MinWidth:         760,
		MinHeight:        520,
		URL:              "/",
		Hidden:           minimized && hasTray,
		BackgroundColour: application.NewRGB(15, 18, 24),
	})
	if hasTray {
		a.setupTray(app)
	}

	// Main-thread dispatch is only valid once the app is running.
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		a.ready.Store(true)
		go func() {
			for {
				a.refreshTray(a.State())
				time.Sleep(5 * time.Second)
			}
		}()
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func (a *App) setupTray(app *application.App) {
	// The cross hides the window to the tray only while the service runs; with no
	// service there is nothing to keep alive, so the app quits.
	a.win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if closeHidesToTray(a.State().Service) {
			a.win.Hide()
			e.Cancel()
			return
		}
		app.Quit()
	})

	tray := app.SystemTray.New()
	a.mu.Lock()
	a.tray = tray
	a.mu.Unlock()
	a.buildTrayMenu(trayModel{Service: "unknown"})
	if runtime.GOOS == "darwin" {
		a.tray.SetTemplateIcon(trayTemplateOff)
	} else {
		a.tray.SetIcon(trayOff)
		a.tray.OnClick(a.showWindow)
	}
	a.tray.SetTooltip("DoH Client")
}
