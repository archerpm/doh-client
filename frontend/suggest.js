// Suggestions from the query log: domains that keep failing and have no rule yet.
// Pure (no DOM) so it can be tested with node.

const SLD = new Set(["co", "com", "org", "net", "gov", "ac", "edu"]);

// "a.b.example.com" -> "example.com"; "x.example.co.uk" -> "example.co.uk"
export function baseDomain(name) {
  const p = String(name || "").toLowerCase().replace(/\.$/, "").split(".").filter(Boolean);
  if (p.length <= 2) return p.join(".");
  const n = p.length >= 3 && p[p.length - 1].length === 2 && SLD.has(p[p.length - 2]) ? 3 : 2;
  return p.slice(-n).join(".");
}

const covered = (dom, rules) => rules.some((r) => r.enabled && (dom === r.domain || dom.endsWith("." + r.domain)));
const exact = (name) => String(name || "").toLowerCase().replace(/\.$/, "");

// log: resolver entries; rules: config rules; returns [{domain, count, reason}] (max 5)
export function suggest(log, rules, ignored = new Set(), { window = 400, min = 3, slowMs = 3000 } = {}) {
  const groups = new Map();
  log = Array.isArray(log) ? log : []; rules = Array.isArray(rules) ? rules : [];
  for (const e of log.slice(-window)) {
    if (!e || e.cached || e.synth || e.rule || !e.name) continue; // rule traffic is already handled
    const answers = e.answers || [];
    if (e.rcode === "NXDOMAIN" && !e.error) continue; // a nonexistent name is not a failure, however slow
    let kind = "";
    if (e.error && !answers.length) kind = "fail";
    else if (e.rcode === "SERVFAIL") kind = "fail";
    else if (answers.length && answers.every((a) => a === "0.0.0.0")) kind = "blocked";
    else if (e.duration_ns / 1e6 > slowMs) kind = "slow";
    if (!kind) continue; // a lone NXDOMAIN is normal and never a reason
    const dom = exact(e.name); // the exact name queried, so "Diagnose" checks what really failed
    if (!dom || ignored.has(dom) || covered(dom, rules)) continue;
    const g = groups.get(dom) || { domain: dom, count: 0, fail: 0, blocked: 0, slow: 0 };
    g.count++; g[kind]++;
    groups.set(dom, g);
  }
  return [...groups.values()]
    .filter((g) => g.count >= min)
    .sort((a, b) => b.count - a.count)
    .slice(0, 5)
    .map((g) => ({
      domain: g.domain,
      count: g.count,
      reason: g.blocked >= g.fail && g.blocked >= g.slow ? "провайдер отвечает 0.0.0.0 — возможно, домен заблокирован для вашего региона"
        : g.fail >= g.slow ? "запросы заканчиваются ошибкой или таймаутом" : "ответы приходят очень медленно",
    }));
}
