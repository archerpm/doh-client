package probe

// Answer records a direct provider query, bypassing router suppression and cache.
type Answer struct {
	Provider string   `json:"provider"`
	Type     string   `json:"type"`
	Rcode    string   `json:"rcode,omitempty"`
	Addrs    []string `json:"addrs"`
	MS       int64    `json:"ms"`
	Error    string   `json:"error,omitempty"`
	Status   string   `json:"status"`
	Blocked  bool     `json:"blocked"`
}

type Action struct {
	Code string `json:"code"`
	Text string `json:"text"`
}
type Verdict struct {
	Code   string `json:"code"`
	Stage  string `json:"stage"`
	Action Action `json:"action"`
}
type Evidence struct {
	Matched     bool
	OSMismatch  bool
	Answers     []Answer // selected route answers, in effective chain order
	Connections []Connection
}

// Routed mirrors the router's fallback: errors and sinkholes fall through;
// a successful empty answer or NXDOMAIN stops the chain.
func Routed(chain []string, answers []Answer, typ string, isRule bool) []Answer {
	out := []Answer{}
	for _, id := range chain {
		for _, a := range answers {
			if a.Provider != id || a.Type != typ {
				continue
			}
			out = append(out, a)
			if a.Status == "pending" || (a.Status == "ok" && (a.Rcode == "NOERROR" || a.Rcode == "NXDOMAIN") && !(isRule && a.Blocked)) {
				return out
			}
			break
		}
	}
	return out
}

func Decide(e Evidence) Verdict {
	result := func(code, stage, action, text string) Verdict { return Verdict{code, stage, Action{action, text}} }
	if e.OSMismatch {
		return result("os_dns_mismatch", "os_dns", "reapply_dns", "Повторно примените DNS клиента")
	}
	if !e.Matched {
		// a domain without a rule is fine as long as it connects directly: the rule is
		// only the suggested fix when it does not
		for _, c := range e.Connections {
			if c.TLS == "ok" {
				return result("transport_ok", "connect", "check_browser_dns", "TCP и TLS исправны (домен не в правилах); проверьте Secure DNS браузера")
			}
		}
		return result("no_rule", "rule", "add_rule", "Добавьте точное правило для домена")
	}
	type familyEvidence struct{ usable, blocked, failed, pending bool }
	families := map[string]familyEvidence{}
	for _, a := range e.Answers {
		f := families[a.Type]
		switch {
		case a.Status == "pending":
			f.pending = true
		case a.Status != "ok" || (a.Rcode != "NOERROR" && a.Rcode != "NXDOMAIN"):
			f.failed = true
		case a.Blocked:
			f.blocked = true
		default:
			// An accepted reply stops fallback for this family, replacing previous
			// errors or sinkholes even when the accepted reply contains no addresses.
			f = familyEvidence{usable: len(Addresses(a.Addrs)) > 0}
		}
		families[a.Type] = f
	}
	usable, blocked, failed, pending := false, false, false, false
	for _, f := range families {
		usable = usable || f.usable
		blocked = blocked || f.blocked
		failed = failed || f.failed
		pending = pending || f.pending
	}
	dnsPending := pending

	tcp, tlsFailed := false, false
	for _, c := range e.Connections {
		if c.TLS == "ok" {
			return result("transport_ok", "connect", "check_browser_dns", "TCP и TLS исправны; проверьте Secure DNS браузера")
		}
		if c.TCP == "ok" {
			tcp = true
		}
		if c.TLS == "failed" {
			tlsFailed = true
		}
		if c.TCP == "pending" || c.TLS == "pending" {
			pending = true
		}
	}
	if pending || len(e.Answers) == 0 {
		stage := "connect"
		if dnsPending {
			stage = "dns"
		} else if len(e.Answers) == 0 {
			stage = "chain"
		}
		return result("inconclusive", stage, "retry", "Недостаточно данных; повторите диагностику")
	}
	if usable {
		if tlsFailed {
			return result("tls_failed", "connect", "check_tls", "Проверьте сертификаты и фильтрацию TLS")
		}
		if !tcp && len(e.Connections) > 0 {
			return result("tcp_failed", "connect", "check_network", "Проверьте доступность адресов и сетевую фильтрацию")
		}
		return result("inconclusive", "connect", "retry", "Недостаточно данных; повторите диагностику")
	}
	if blocked {
		return result("provider_blocked", "dns", "change_provider", "Провайдер вернул нулевой адрес; выберите другой провайдер")
	}
	if failed {
		return result("provider_unreachable", "dns", "check_provider", "Проверьте доступность DoH-провайдера или выберите другой")
	}
	return result("no_answer", "dns", "check_domain", "Проверьте домен и ответы DNS-провайдера")
}
