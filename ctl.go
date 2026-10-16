package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/archerpm/doh-client/internal/api"
	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/paths"
)

const ctlUsage = `doh-client status [--json]       current state of the running service
doh-client enable | disable      turn the bypass on / off
doh-client test DOMAIN [--json]  ask the active provider about DOMAIN
doh-client diagnose DOMAIN [--json] why a site does not open: rule, provider, DNS, OS DNS, TCP/TLS
doh-client flush                 clear the DNS cache
doh-client reapply-dns           reapply system DNS for the active proxy
doh-client config export [FILE]  write the settings as JSON (stdout by default)
doh-client config import FILE    replace the settings (validated by the service; "-" = stdin)`

// ctl runs the automation commands against the local service API. Exit codes:
// 0 ok, 1 failure, 2 usage, 3 service not reachable.
func ctl(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "status", "enable", "disable", "test", "flush", "config", "diagnose", "reapply-dns":
	default:
		return 0, false
	}
	attachConsole()
	return ctlWithClient(api.NewClient(paths.APIAddr, paths.TokenFile()), args)
}

func ctlWithClient(cl *api.Client, args []string) (int, bool) {
	out := func(v any) { b, _ := json.MarshalIndent(v, "", "  "); fmt.Println(string(b)) }
	fail := func(err error) (int, bool) {
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, api.ErrNoService) || errors.Is(err, api.ErrNoAccess) {
			return 3, true
		}
		return 1, true
	}
	if args[0] == "diagnose" {
		return ctlDiagnose(cl, args)
	}
	hasJSON := false
	var rest []string
	for _, a := range args[1:] {
		if a == "--json" {
			hasJSON = true
		} else {
			rest = append(rest, a)
		}
	}
	if hasJSON && args[0] != "status" && args[0] != "enable" && args[0] != "disable" && args[0] != "test" {
		fmt.Fprintln(os.Stderr, "--json не поддерживается для команды", args[0])
		fmt.Fprintln(os.Stderr, ctlUsage)
		return 2, true
	}
	// reject typos before touching the service: `disable --jsno` must not disable
	for _, a := range rest {
		if strings.HasPrefix(a, "-") && a != "-" {
			fmt.Fprintln(os.Stderr, "unknown option", a)
			fmt.Fprintln(os.Stderr, ctlUsage)
			return 2, true
		}
	}
	if (args[0] == "status" || args[0] == "enable" || args[0] == "disable" || args[0] == "flush" || args[0] == "reapply-dns") && len(rest) > 0 {
		fmt.Fprintln(os.Stderr, ctlUsage)
		return 2, true
	}
	switch args[0] {
	case "status":
		st, err := cl.Status()
		if err != nil {
			return fail(err)
		}
		if hasJSON {
			out(st)
			return 0, true
		}
		state := "выключен"
		if st.Active {
			state = "включён"
		}
		fmt.Printf("обход: %s (режим %s), служба %s, аптайм %s\n", state, st.Mode, st.Version, st.Uptime)
		fmt.Printf("запросов %d, из кэша %d, по правилам %d, ошибок %d\n", st.Stats.Queries, st.Stats.CacheHits, st.Stats.RuleHits, st.Stats.Failures)
		for _, h := range st.Health {
			fmt.Printf("  %-14s down=%v %.0f мс\n", h.ID, h.Down, h.LatencyMS)
		}
		if st.LastError != "" {
			fmt.Println("ошибка:", st.LastError)
		}
		for _, w := range st.Warnings {
			fmt.Println("внимание:", w)
		}
	case "enable", "disable":
		st, err := cl.SetEnabled(args[0] == "enable")
		if err != nil {
			return fail(err)
		}
		if hasJSON {
			out(st)
		} else {
			fmt.Println("active:", st.Active)
		}
	case "test":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, ctlUsage)
			return 2, true
		}
		res, err := cl.Test(rest[0])
		if err != nil {
			return fail(err)
		}
		if hasJSON {
			out(res)
			return 0, true
		}
		for _, r := range res {
			verdict := "обычный ответ"
			switch {
			case r.Error != "":
				verdict = "ошибка: " + r.Error
			case r.Blocked:
				verdict = "отказ (0.0.0.0)"
			case r.Proxied:
				verdict = "ПРОКСИ"
			case r.Differs:
				verdict = "отличается от эталона"
			}
			fmt.Printf("  %-26s %-14s %4d мс  %s\n", r.Name, verdict, r.MS, strings.Join(r.Addrs, ", "))
		}
	case "reapply-dns":
		if err := cl.ReapplyDNS(); err != nil {
			return fail(err)
		}
		fmt.Println("DNS применён заново")
	case "flush":
		if err := cl.Flush(); err != nil {
			return fail(err)
		}
		fmt.Println("кэш сброшен")
	case "config":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, ctlUsage)
			return 2, true
		}
		switch rest[0] {
		case "export":
			if len(rest) != 1 && len(rest) != 2 {
				fmt.Fprintln(os.Stderr, ctlUsage)
				return 2, true
			}
			cfg, err := cl.Config()
			if err != nil {
				return fail(err)
			}
			b, _ := json.MarshalIndent(cfg, "", "  ")
			if len(rest) > 1 {
				if err := os.WriteFile(rest[1], append(b, '\n'), 0o644); err != nil {
					return fail(err)
				}
				fmt.Println("сохранено:", rest[1])
			} else {
				fmt.Println(string(b))
			}
		case "import":
			if len(rest) != 2 {
				fmt.Fprintln(os.Stderr, ctlUsage)
				return 2, true
			}
			var r io.Reader = os.Stdin
			if rest[1] != "-" {
				f, err := os.Open(rest[1])
				if err != nil {
					return fail(err)
				}
				defer f.Close()
				r = f
			}
			raw, err := io.ReadAll(io.LimitReader(r, config.MaxConfigBytes+1))
			if err != nil {
				return fail(err)
			}
			if len(raw) > config.MaxConfigBytes {
				return fail(fmt.Errorf("файл больше %d МБ", config.MaxConfigBytes>>20))
			}
			var cfg config.Config
			dec := json.NewDecoder(bytes.NewReader(raw))
			if err := dec.Decode(&cfg); err != nil {
				return fail(fmt.Errorf("файл не похож на настройки: %w", err))
			}
			if _, err := dec.Token(); err != io.EOF { // nothing but whitespace may follow
				return fail(fmt.Errorf("после настроек в файле есть лишние данные"))
			}
			if _, err := cl.SaveConfig(cfg); err != nil {
				return fail(err)
			}
			fmt.Println("настройки применены")
		default:
			fmt.Fprintln(os.Stderr, ctlUsage)
			return 2, true
		}
	}
	return 0, true
}
