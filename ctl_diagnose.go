package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/archerpm/doh-client/internal/api"
)

// ctlDiagnose takes the full command arguments, including "diagnose".
// The caller wires dispatch and console attachment in ctl.go.
func ctlDiagnose(cl *api.Client, args []string) (int, bool) {
	if len(args) == 0 || args[0] != "diagnose" {
		return 0, false
	}
	if len(args) < 2 || len(args) > 3 || strings.HasPrefix(args[1], "-") || (len(args) == 3 && args[2] != "--json") {
		fmt.Fprintln(os.Stderr, "использование: doh-client diagnose DOMAIN [--json]")
		return 2, true
	}
	result, err := cl.Diagnose(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		if errors.Is(err, api.ErrNoService) || errors.Is(err, api.ErrNoAccess) {
			return 3, true
		}
		return 1, true
	}
	if len(args) == 3 {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			return 1, true
		}
		fmt.Println(string(b))
	} else {
		fmt.Println("домен:", result.Domain)
		for _, stage := range result.Stages {
			fmt.Printf("  %s: %s\n", stage.ID, stage.Status)
		}
		fmt.Printf("результат: %s (%s)\n%s\n", result.Verdict.Code, result.Verdict.Stage, result.Verdict.Action.Text)
	}
	return 0, true
}
