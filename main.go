// DoH Client: GUI + system service for routing DNS through DNS-over-HTTPS
// providers that bypass geo-blocking.
//
//	doh-client                    start the GUI
//	doh-client service install    install + start the system service (admin)
//	doh-client service uninstall  stop + remove the service, restore DNS (admin)
//	doh-client service start|stop|restart|status
//	doh-client run                run the resolver in the foreground (admin)
//	doh-client restore-dns        emergency: restore original system DNS (admin)
//	doh-client status|enable|disable|reapply-dns|test|diagnose|flush|config   control the running service (see ctlUsage)
package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/svc"
)

var version = "1.1.2"

// attachConsoleCLI is a hook for testing CLI console attachment.
var attachConsoleCLI = attachConsole

func main() {
	daemon.Version = version
	if len(os.Args) > 1 {
		if code, handled := cli(os.Args[1:]); handled {
			os.Exit(code)
		}
	}
	runGUI()
}

func cli(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "service", "run", "restore-dns", "version", "--version", "-v":
		attachConsoleCLI()
		if slices.Contains(args[1:], "--json") {
			fmt.Fprintln(os.Stderr, "--json не поддерживается для команды", args[0])
			return 2, true
		}
	}
	if code, ok := ctl(args); ok {
		return code, true
	}
	fail := func(err error) (int, bool) {
		fmt.Fprintln(os.Stderr, "error:", err)
		logCLIError(err)
		return 1, true
	}
	switch args[0] {
	case "service":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: doh-client service install|uninstall|start|stop|restart|status|run")
			return 2, true
		}
		var err error
		switch args[1] {
		case "run":
			err = svc.Run()
		case "install":
			owner := ""
			if len(args) >= 4 && args[2] == "--owner" {
				owner = args[3]
			}
			err = svc.Install(owner)
		case "uninstall":
			err = svc.Uninstall()
		case "start", "stop", "restart":
			err = svc.Control(args[1])
		case "status":
			fmt.Println(svc.Status())
		default:
			fmt.Fprintln(os.Stderr, "unknown service command", args[1])
			return 2, true
		}
		if err != nil {
			return fail(err)
		}
		return 0, true
	case "run":
		if err := svc.RunForeground(); err != nil {
			return fail(err)
		}
		return 0, true
	case "restore-dns":
		if err := svc.RestoreDNS(); err != nil {
			return fail(err)
		}
		fmt.Println("system DNS restored")
		return 0, true
	case "version", "--version", "-v":
		fmt.Println(version)
		return 0, true
	}
	switch args[0] {
	case "help", "--help", "-h":
		attachConsole()
		fmt.Println(mainUsage())
		return 0, true
	}
	if !strings.HasPrefix(args[0], "-") {
		// a mistyped command must not silently open the GUI
		attachConsole()
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s\n", args[0], mainUsage())
		return 2, true
	}
	return 0, false // e.g. --minimized: GUI flag
}

func mainUsage() string {
	return "usage: doh-client [--minimized]            start the GUI\n" +
		"       doh-client service install|uninstall|start|stop|restart|status\n" +
		"       doh-client run | restore-dns | version\n" + ctlUsage
}
