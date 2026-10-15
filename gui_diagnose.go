package main

import "github.com/archerpm/doh-client/internal/daemon"

func (a *App) DiagnoseDomain(domain string) (daemon.DiagnoseResult, error) {
	return a.cl.Diagnose(domain)
}
