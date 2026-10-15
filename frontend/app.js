// DoH Client UI. Calls Go methods on main.App through the Wails runtime.
import { RouteMap } from "./map.js";
import { suggest } from "./suggest.js";

let rt = null;
try { rt = await import("/wails/runtime.js"); } catch { /* browser preview with a mock */ }
const call = (m, ...a) => (window.__mockCall ? window.__mockCall(m, ...a) : rt.Call.ByName("main.App." + m, ...a));

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const errText = (e) => (e && (e.message || e.toString())) || "неизвестная ошибка";
const motionQuery = matchMedia("(prefers-reduced-motion: reduce)");
const fmt = new Intl.NumberFormat("ru-RU");

function toast(msg, bad = false) {
  const t = document.createElement("div");
  t.className = "t" + (bad ? " bad" : "");
  t.textContent = msg;
  $("#toast").append(t);
  setTimeout(() => t.remove(), bad ? 8000 : 3000);
}

// warnings that opened a popup in this window: each text pops up once per GUI run
const shownPopups = new Set();
function showConflicts(list) {
  const fresh = list.filter((w) => !shownPopups.has(w));
  if (!fresh.length || $("#popup")) return;
  fresh.forEach((w) => shownPopups.add(w));
  const box = document.createElement("div");
  box.id = "popup";
  box.innerHTML = `<div class="popup-card" role="alertdialog" aria-labelledby="popupTitle" aria-describedby="popupBody"><h2 id="popupTitle">Возможен конфликт DNS</h2><div id="popupBody">${fresh.map((w) => `<p>${esc(w)}</p>`).join("")}</div><button class="btn" type="button">Понятно</button></div>`;
  const close = () => { box.remove(); document.removeEventListener("keydown", onKey); };
  const onKey = (e) => { if (e.key === "Escape") close(); };
  $("button", box).addEventListener("click", close);
  document.addEventListener("keydown", onKey);
  document.body.append(box);
  $("button", box).focus();
}

const S = { state: null, cfg: null, presets: [], tab: "overview", logSeq: 0, log: [], logPaused: false, board: [], provTest: {}, provTestAt: 0, provTesting: false };
// numbers roll to their new value instead of jumping (skipped with reduced motion)
function tween(node, to) {
  to = to || 0;
  const from = Number(node.dataset.v ?? 0);
  node.dataset.v = to;
  cancelAnimationFrame(node._raf); // a stale animation must never overwrite the new value
  if (motionQuery.matches || from === to || Math.abs(to - from) < 2) { node.textContent = fmt.format(to); return; }
  const t0 = performance.now(), dur = 600;
  const step = (now) => {
    const k = Math.min(1, (now - t0) / dur), e = 1 - Math.pow(1 - k, 3);
    node.textContent = fmt.format(Math.round(from + (to - from) * e));
    if (k < 1) node._raf = requestAnimationFrame(step);
  };
  node._raf = requestAnimationFrame(step);
}

const map = new RouteMap($("#map"), { reducedMotion: motionQuery.matches });
motionQuery.addEventListener?.("change", (e) => map.setReducedMotion(e.matches));

// ---------- provider colours ----------
const KNOWN = ["comss", "geohide", "malw", "malw-cf", "dnsai", "mafioznik", "bezmezhau", "cloudflare", "google"];
const EXTRA = ["#4fb8d9", "#b7cf3e", "#e07ad0", "#58c472", "#d9a04f"];
function color(id) {
  if (KNOWN.includes(id)) return `var(--c-${id})`;
  let h = 0;
  for (const ch of String(id)) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return EXTRA[h % EXTRA.length];
}
const prov = (id) => S.cfg?.providers.find((p) => p.id === id);
const provName = (id) => prov(id)?.name || id || "";
const healthOf = (id) => S.state?.status?.health?.find((h) => h.id === id);

// ---------- navigation ----------
$("#nav").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-tab]");
  if (!b) return;
  S.tab = b.dataset.tab;
  $$("#nav button").forEach((x) => {
    x.classList.toggle("on", x === b);
    if (x === b) x.setAttribute("aria-current", "page"); else x.removeAttribute("aria-current");
  });
  $$(".tab").forEach((x) => x.classList.toggle("on", x.id === "tab-" + S.tab));
  if (S.tab === "settings") renderSettings();
  if (S.tab === "log") renderLog();
});

// ---------- state / overview ----------
function plural(n, one, few, many) {
  const m10 = n % 10, m100 = n % 100;
  return m10 === 1 && m100 !== 11 ? one : m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14) ? few : many;
}

async function refresh() {
  try { S.state = await call("State"); } catch (e) { S.state = { service: "unknown", error: errText(e) }; }
  const st = S.state;
  const running = st.service === "running";
  $("#noService").classList.toggle("hidden", running);
  updateQuick(running);
  $("#main").classList.toggle("hidden", !running);
  const on = running && st.status.active;
  $("#railStatus").innerHTML = `<span class="dot ${on ? "ok" : running ? "" : "bad"}"></span>${
    on ? "Обход работает" : running ? "Обход выключен" : st.service === "starting" ? "Служба запускается" : st.service === "no_access" ? "Нет доступа к службе" : "Служба не запущена"}`;
  if (!running) {
    const t = {
      stopped: ["Служба остановлена", "Служба установлена, но не запущена. Переустановите её — она запустится и будет стартовать вместе с системой."],
      starting: ["Служба запускается", "Подождите пару секунд."],
      no_access: ["Нет доступа к службе", st.error || ""],
    }[st.service];
    if (t) { $("#setupTitle").textContent = t[0]; $("#setupText").textContent = t[1]; }
    $("#btnInstall").textContent = st.service === "not_installed" || st.service === "unknown" ? "Установить службу" : "Переустановить службу";
    $("#btnInstall").classList.toggle("hidden", st.service === "starting");
    S.cfg = null; // no stale config may be edited while the service is unreachable
    return;
  }
  const s = st.status;
  if (S.instance && S.instance !== s.instance) {
    // the service restarted: drop everything cached from the old instance
    S.cfg = null; S.logSeq = 0; S.log = []; S.board = []; renderBoard(); renderLog();
  }
  S.instance = s.instance;
  if (!S.cfg) await loadConfig();
  updateProviderHealth();
  updateProvTestUI();
  const sw = $("#power");
  sw.setAttribute("aria-checked", String(s.active));
  $(".switch-lbl", sw).textContent = s.active ? "Включено" : "Включить";
  const nRules = S.cfg ? S.cfg.rules.filter((r) => r.enabled).length : 0;
  $("#heroTitle").textContent = s.active ? "Обход работает" : "Обход выключен";
  $("#heroSub").textContent = !s.active
    ? "Включите, чтобы DNS-запросы шли через провайдеров с прокси за рубежом."
    : s.mode === "hosts"
      ? `Режим hosts: записано ${s.hosts_domains?.length || 0} ${plural(s.hosts_domains?.length || 0, "домен", "домена", "доменов")}.`
      : nRules
        ? `В правилах ${nRules} ${plural(nRules, "домен", "домена", "доменов")}. Все запросы идут через выбранный DoH-туннель.`
        : "Правил пока нет: все запросы идут через выбранный DoH-туннель. Добавьте наборы на вкладке «Правила».";
  let al = "";
  if (s.last_error) al += `<div class="alert bad">${esc(s.last_error)}</div>`;
  const conflicts = s.conflicts || [];
  for (const w of s.warnings || []) if (!conflicts.includes(w)) al += `<div class="alert">${esc(w)}</div>`;
  showConflicts(conflicts);
  $("#alerts").innerHTML = al;
  renderEvents(s.events || []);
  const q = s.stats.queries || 0;
  tween($("#sQ"), q); tween($("#sC"), s.stats.cache_hits); tween($("#sR"), s.stats.rule_hits); tween($("#sF"), s.stats.failures);
  for (const [id, v] of [["sC", s.stats.cache_hits], ["sR", s.stats.rule_hits], ["sF", s.stats.failures]]) {
    $("#" + id).parentElement.parentElement.style.setProperty("--p", q ? Math.min(100, (v / q) * 100) + "%" : "0%");
  }
  $("#sQ").parentElement.parentElement.style.setProperty("--p", q ? "100%" : "0%");
  $("#ver").textContent = "версия " + s.version;
  updateMap();
}

// latency / availability cells refresh in place (no rebuild, focus stays put)
function updateProviderHealth() {
  for (const cell of $$("#provList .lat[data-id]")) {
    const id = cell.dataset.id, p = prov(id), h = healthOf(id);
    const txt = !p?.enabled || !h?.checked ? "" : h.ok ? `${Math.round(h.latency_ms)} мс` : "недоступен";
    if (cell.textContent !== txt) cell.textContent = txt;
    cell.classList.toggle("bad", !!(h && h.checked && !h.ok));
  }
}

function updateMap() {
  if (!S.cfg) return;
  map.isGeo = (id) => !!prov(id)?.geo;
  const ps = S.cfg.providers.filter((p) => p.enabled).map((p) => {
    const h = healthOf(p.id);
    return { id: p.id, name: p.name, color: color(p.id), geo: p.geo, down: !!h?.down, latency: h?.ok ? Math.round(h.latency_ms) : 0 };
  });
  const live = !!S.state?.status?.active && S.state.status.mode === "proxy";
  map.setModel(ps, live);
  $("#radar").classList.toggle("on", live);
  document.querySelector(".hero").classList.toggle("on", live);
  tween($("#radarNum"), live ? ps.filter((p) => !p.down).length : 0);
  $("#mapMode").textContent = `DoH · ${S.cfg.listen?.[0] || "127.0.0.1:53"}`;
}

$("#power").addEventListener("click", async () => {
  const b = $("#power");
  b.classList.add("busy");
  try { await call("SetEnabled", !S.state.status.active); } catch (e) { toast(errText(e), true); }
  b.classList.remove("busy");
  refresh();
});

// ---------- quick setup ----------
let quickRan = false, quickBusy = false;
function updateQuick(running) {
  // offered while nothing is set up yet: no service, or a service without any rule
  const fresh = !running || (S.cfg && !S.cfg.rules.length && !S.state?.status?.active);
  $("#quick").classList.toggle("hidden", quickBusy ? false : !(fresh && !quickRan) && !$("#quickStages").children.length);
  const box = $("#quickPresets");
  if (!box.children.length && S.presets.length) {
    box.innerHTML = S.presets.map((p, i) => `<label class="qp"><input type="checkbox" value="${esc(p.id)}" ${i === 0 ? "checked" : ""}><span><b>${esc(p.name)}</b><small>${esc(p.note)}</small></span></label>`).join("");
  }
}
$("#quickGo").addEventListener("click", async (e) => {
  const btn = e.currentTarget;
  const ids = $$("#quickPresets input:checked").map((x) => x.value);
  if (!ids.length) return toast("Выберите хотя бы один набор", true);
  btn.disabled = true; quickBusy = true; cfgGen++; // blocks new saves; saves already queued must finish first
  await saveQueue.catch(() => {});
  $("#quickMsg").textContent = "Подтвердите запрос прав администратора, если он появится";
  $("#quickStages").innerHTML = "";
  let res;
  try { res = await call("QuickSetup", ids); } catch (err) { toast(errText(err), true); btn.disabled = false; quickBusy = false; $("#quickMsg").textContent = ""; S.cfg = null; refresh(); return; }
  $("#quickMsg").textContent = "";
  const icon = { ok: "✓", warn: "!", failed: "✕", info: "i" };
  $("#quickStages").innerHTML = (res.stages || []).map((s, i) =>
    `<li class="qs ${esc(s.status)}" style="--i:${i}"><i aria-hidden="true">${icon[s.status] || "·"}</i><div><b>${esc(s.title)}</b><span>${esc(s.detail || "")}</span></div></li>`).join("");
  cfgGen++; S.cfg = null;
  try { await refresh(); } finally { btn.disabled = false; quickBusy = false; quickRan = true; }
});

async function install(btn, msgEl) {
  btn.disabled = true;
  if (msgEl) msgEl.textContent = "Подтвердите запрос прав администратора";
  try { await call("InstallService"); toast("Служба установлена и запущена"); S.cfg = null; }
  catch (e) { toast(errText(e), true); }
  if (msgEl) msgEl.textContent = "";
  btn.disabled = false;
  refresh();
}
$("#btnInstall").addEventListener("click", (e) => install(e.currentTarget, $("#installMsg")));
$("#btnReinstall").addEventListener("click", (e) => install(e.currentTarget));
$("#btnUninstall").addEventListener("click", async (e) => {
  const b = e.currentTarget;
  if (!confirmTwice(b)) return;
  b.disabled = true;
  try { await call("UninstallService"); toast("Служба удалена, системный DNS восстановлен"); S.cfg = null; }
  catch (err) { toast(errText(err), true); }
  b.disabled = false;
  refresh();
});

// ---------- live log: feeds the map, the board and the journal ----------
let logTimer = null;
async function pollLog() {
  clearTimeout(logTimer);
  if (S.state?.service === "running") {
    try {
      const batch = (await call("Log", S.logSeq)) || [];
      const live = batch.filter((e) => e.client !== "hosts");
      if (batch.length) S.logSeq = batch[batch.length - 1].seq;
      if (live.length) {
        // spread a batch over the poll interval so packets don't leave in one clump
        const t0 = new Date(live[0].time).getTime();
        const span = Math.max(1, new Date(live[live.length - 1].time).getTime() - t0);
        live.slice(-36).forEach((e) => map.emit(e, Math.min(850, ((new Date(e.time).getTime() - t0) / span) * 850)));
        if (!S.logPaused) {
          S.log.push(...live);
          if (S.log.length > 2000) S.log.splice(0, S.log.length - 2000);
          if (S.tab === "log") renderLog();
        }
        S.board = [...[...live].reverse(), ...S.board].slice(0, 12);
        renderSuggest();
        renderBoard(Math.min(live.length, 12));
      }
    } catch { /* service restarting */ }
  }
  logTimer = setTimeout(pollLog, 900);
}

function route(e) {
  if (e.synth) return e.rule ? ["IPv6 отключён", ""] : ["отклонён", "bad"];
  if (e.cached) return ["из кэша", ""];
  if (e.error && !(e.answers || []).length) return ["нет ответа", "bad"];
  if ((e.answers || []).length && e.answers.every((a) => a === "0.0.0.0")) return ["заблокирован провайдером", "bad"];
  if (!e.rule) return ["напрямую", ""];
  return prov(e.provider)?.geo ? ["через прокси", "proxy"] : ["по правилу", ""];
}
function lineBadge(e) {
  if (e.cached) return `<span class="lb cache"><i></i>кэш</span>`;
  if (e.synth) return `<span class="lb synth"><i></i>клиент</span>`;
  if (!e.provider) return `<span class="lb"><i></i>—</span>`;
  return `<span class="lb" style="--c:${color(e.provider)}"><i></i>${esc(provName(e.provider))}</span>`;
}
function renderBoard(fresh = 0) {
  $("#boardEmpty").classList.toggle("hidden", S.board.length > 0);
  $("#board").innerHTML = S.board.map((e, i) => {
    const [r, cls] = route(e);
    return `<li class="${i < fresh ? "fresh" : ""}"><span class="tm">${new Date(e.time).toLocaleTimeString("ru-RU")}</span><span>${lineBadge(e)}</span><span class="d" title="${esc(e.name)}"><span>${esc(e.name)}</span></span><span class="route ${cls}">${r}</span><span class="ms">${Math.round(e.duration_ns / 1e6)} мс</span></li>`;
  }).join("");
}

// ---------- health events from the service: toast when something changes ----------
let evSeen = null; // keys of events already shown (a time watermark loses events with different fractional digits)
const EV_BAD = new Set(["provider_down", "chain_down", "dns_lost", "enable_failed"]);
function renderEvents(list) {
  list = Array.isArray(list) ? list : [];
  const key = (e) => `${e.time}|${e.kind}|${e.provider || ""}|${e.text}`;
  const first = evSeen === null; // what happened before the window opened is history, not news
  if (first) evSeen = new Set();
  for (const e of list) {
    const k = key(e);
    if (evSeen.has(k)) continue;
    evSeen.add(k);
    if (!first) toast(e.text, EV_BAD.has(e.kind));
  }
  if (evSeen.size > 500) evSeen = new Set(list.map(key));
  $("#events").classList.toggle("hidden", !list.length);
  $("#eventList").innerHTML = list.slice(-5).reverse().map((e) =>
    `<li class="${EV_BAD.has(e.kind) ? "bad" : ""}"><span class="tm mono">${new Date(e.time).toLocaleTimeString("ru-RU")}</span><span>${esc(e.text)}</span></li>`).join("");
}

// ---------- suggestions from the log ----------
const ignored = new Set();
let suggestDoms = "";
function renderSuggest() {
  const list = S.cfg ? suggest(S.log, S.cfg.rules, ignored) : [];
  const doms = list.map((s) => s.domain).join("\n");
  const text = (s) => `${s.reason} · ${s.count} ${plural(s.count, "запрос", "запроса", "запросов")}`;
  if (doms === suggestDoms) { // same rows: update counters in place so keyboard focus survives the poll
    $$("#suggestList li").forEach((li, i) => { const sp = $("span.muted", li); if (sp && list[i]) sp.textContent = text(list[i]); });
    return;
  }
  const focus = document.activeElement?.closest?.("#suggestList") ? document.activeElement.dataset : null;
  suggestDoms = doms;
  $("#suggest").classList.toggle("hidden", !list.length);
  $("#suggestList").innerHTML = list.map((s) =>
    `<li><div><b class="mono">${esc(s.domain)}</b><span class="muted small">${esc(text(s))}</span></div>
     <div class="row"><button class="btn sm" data-diag="${esc(s.domain)}">Диагностика</button><button class="icon-btn" data-ignore="${esc(s.domain)}" title="Больше не предлагать до перезапуска" aria-label="Скрыть ${esc(s.domain)}">скрыть</button></div></li>`).join("");
  if (focus) ($("#suggestList [data-diag]") || $("#nav button.on, #nav button"))?.focus();
}
$("#suggestList").addEventListener("click", (e) => {
  const d = e.target.closest("[data-diag]"), i = e.target.closest("[data-ignore]");
  if (i) { ignored.add(i.dataset.ignore); if (ignored.size > 1024) ignored.delete(ignored.values().next().value); renderSuggest(); return; }
  if (!d) return;
  $('#nav button[data-tab="test"]').click();
  $("#testForm").domain.value = d.dataset.diag;
  $("#testForm").domain.focus();
  $("#diagGo").click();
});

function renderLog() {
  const q = $("#logFilter").value.trim().toLowerCase();
  const onlyRules = $("#logRules").checked;
  const rows = [];
  for (let i = S.log.length - 1; i >= 0 && rows.length < 500; i--) {
    const e = S.log[i];
    if (onlyRules && !e.rule) continue;
    if (q && !(e.name.toLowerCase().includes(q) || provName(e.provider).toLowerCase().includes(q) || (e.rule || "").includes(q))) continue;
    const ans = e.error && !e.answers?.length ? e.error : (e.answers || []).join(", ") || e.rcode;
    const fb = "";
    rows.push(`<tr class="${e.error && e.rcode !== "NOERROR" ? "err" : ""}"><td class="muted">${new Date(e.time).toLocaleTimeString("ru-RU")}</td><td class="dom">${esc(e.name)}<button class="icon-btn probe" data-dom="${esc(e.name)}" title="Проверить домен через выбранный туннель" aria-label="Проверить домен ${esc(e.name)}">проверить</button>${e.rule ? '<span class="ruletag">правило</span>' : ""}</td><td>${esc(e.type)}</td><td class="ans" title="${esc(ans)}">${esc(ans)}</td><td>${lineBadge(e)}${fb}</td><td class="num">${Math.round(e.duration_ns / 1e6)}</td></tr>`);
  }
  $("#logBody").innerHTML = rows.join("") || `<tr><td colspan="6" class="empty-state">Журнал пуст.</td></tr>`;
}
$("#logFilter").addEventListener("input", renderLog);
// "why doesn't it open?" in one click: hand the domain to the check tab
$("#logBody").addEventListener("click", (e) => {
  const b = e.target.closest("button.probe");
  if (!b) return;
  $('#nav button[data-tab="test"]').click();
  const f = $("#testForm");
  f.domain.value = b.dataset.dom.replace(/\.$/, "");
  f.requestSubmit();
});
$("#logRules").addEventListener("change", renderLog);
$("#logPause").addEventListener("click", (e) => { S.logPaused = !S.logPaused; e.currentTarget.textContent = S.logPaused ? "Продолжить" : "Пауза"; });
$("#logClear").addEventListener("click", () => { S.log = []; renderLog(); renderSuggest(); });
$("#flush").addEventListener("click", async () => { try { await call("FlushCache"); toast("Кэш DNS сброшен в клиенте и системе"); } catch (e) { toast(errText(e), true); } });

// ---------- config ----------
let cfgGen = 0; // bumped whenever the config is replaced elsewhere: a slower, older GetConfig must not overwrite it
async function loadConfig() {
  const gen = cfgGen;
  let c;
  try { c = await call("GetConfig"); } catch (e) { toast(errText(e), true); return; }
  if (gen !== cfgGen) return;
  S.cfg = c;
  S.cfg.rules ??= []; S.cfg.providers ??= []; S.cfg.default_chain ??= [];
  renderAll();
}
function renderAll() {
  const fk = document.activeElement?.dataset?.fk;
  renderProviders(); renderRules(); updateMap(); renderSuggest();
  if (fk) {
    const el = document.querySelector(`[data-fk="${CSS.escape(fk)}"]`);
    (el && !el.disabled ? el : el?.closest(".chain")?.querySelector("button:not([disabled]), select"))?.focus();
  }
}

// Saves run one at a time and each mutation is applied to the latest saved
// config, so two quick edits can't overwrite each other.
let saveQueue = Promise.resolve();
function save(mut, okMsg) {
  // quick setup rewrites the config on the service: an edit made from the stale copy would drop its rules
  if (quickBusy) { toast("Идёт быстрая настройка — подождите, пока она закончится", true); return Promise.resolve(false); }
  if (!S.cfg) { toast("Служба недоступна: настройки сейчас нельзя изменить", true); return Promise.resolve(false); }
  const run = async () => {
    if (!S.cfg) { toast("Служба недоступна: настройки сейчас нельзя изменить", true); return false; }
    const next = structuredClone(S.cfg);
    next.rules ??= [];
    try {
      mut(next);
      S.cfg = await call("SaveConfig", next); cfgGen++;
      if (okMsg) toast(okMsg);
      return true;
    } catch (e) { toast(errText(e), true); return false; }
    finally { renderAll(); }
  };
  const p = saveQueue.then(run, run);
  saveQueue = p.catch(() => {});
  return p;
}

// result of the "Проверить обход" test next to a provider: works / does not work
function provTestTag(id) {
  const r = S.provTest[id];
  if (!r) return "";
  const [cls, txt] = r.status === "works" ? ["ok", "работает"] : r.status === "unknown" ? ["na", "не проверено"] : r.status === "unreachable" ? ["bad", "прокси недоступен"] : ["bad", "не работает"];
  const ms = r.ms && r.status !== "error" ? ` · ${r.ms} мс` : "";
  return `<span class="tag res ${cls}" title="${esc(r.detail + ms)}">${txt}</span>`;
}

function updateProvTestUI() {
  const btn = $("#provTest"), hint = $("#provTestHint");
  if (!btn) return;
  const active = !!S.state?.status?.active;
  btn.disabled = S.provTesting || active || !S.cfg;
  btn.textContent = S.provTesting ? "Проверяем…" : "Проверить обход";
  hint.textContent = S.provTesting ? "Опрашиваем всех провайдеров, это займёт несколько секунд."
    : active ? "Выключите обход, чтобы проверить провайдеров."
    : S.provTestAt ? `Проверено в ${new Date(S.provTestAt).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })}. Наведите на результат, чтобы увидеть подробности.`
    : "";
}

$("#provTest").addEventListener("click", async () => {
  if (S.provTesting || S.state?.status?.active) return;
  S.provTesting = true;
  updateProvTestUI();
  try {
    const res = await call("TestProviders");
    S.provTest = Object.fromEntries((res || []).map((r) => [r.provider, r]));
    S.provTestAt = Date.now();
  } catch (err) {
    toast(errText(err), true);
  } finally {
    S.provTesting = false;
    renderProviders();
    updateProvTestUI();
  }
});

// ---------- what a provider supports (under a disclosure) ----------
S.provLists = {}; // provider id -> { listId: { name, domains } }, fetched in this session
S.provSel = {};   // provider id -> Set of selected list ids
S.provOpen = new Set();
S.provBusy = new Set();

// a rule (or list entry) for "example.com" also matches every subdomain
function coversHost(set, host) {
  for (let h = host; h;) {
    if (set.has(h)) return true;
    const i = h.indexOf(".");
    if (i < 0) return false;
    h = h.slice(i + 1);
  }
  return false;
}

function listSummary(p) {
  const fetched = S.provLists[p.id];
  if (!fetched) return null;
  const rules = new Set((S.cfg?.rules || []).map((r) => r.domain));
  const all = new Set(), fresh = new Set(), lines = [];
  for (const l of p.lists || []) {
    const f = fetched[l.id];
    if (!f) continue;
    const news = f.domains.filter((d) => !coversHost(rules, d));
    news.forEach((d) => fresh.add(d));
    f.domains.forEach((d) => all.add(d));
    lines.push(`${l.name}: ${fmt.format(f.domains.length)} ${plural(f.domains.length, "домен", "домена", "доменов")}, новых для ваших правил: ${fmt.format(news.length)}`);
  }
  const preset = (S.presets || []).map((ps) => {
    const n = ps.domains.filter((d) => coversHost(all, d)).length;
    return n ? `${ps.name}: ${n} из ${ps.domains.length}` : "";
  }).filter(Boolean);
  return { lines, fresh: [...fresh], preset };
}

function provDetail(p) {
  const parts = [];
  if (p.services?.length) parts.push(`<div class="pd-row"><b>Заявлено провайдером:</b> ${p.services.map((s) => `<span class="chip">${esc(s)}</span>`).join("")}</div>`);
  if (p.tested?.length) {
    const d = (p.tested_at || "").split("-").reverse().join(".");
    parts.push(`<div class="pd-row"><b>Подтверждено тестами (${p.tested.length}):</b> ${p.tested.map((s) => `<span class="chip tested">${esc(s)}</span>`).join("")}</div>`);
    parts.push(`<div class="pd-row muted">Получено на основе реальных проверок${d ? ` от ${esc(d)}` : ""}: мы спросили провайдера про хосты каждого сервиса (по спискам GeoHide и нашим наборам) и сравнили ответы с обычным DNS. Сервис засчитан, если не менее половины его хостов получили адреса прокси провайдера; нестабильные ответы не засчитывались. Сервисов вне этого перечня провайдер может поддерживать больше.</div>`);
  }
  const t = S.provTest[p.id];
  if (t) parts.push(`<div class="pd-row"><b>Последняя проверка:</b> ${esc(t.detail)}</div>`);
  if (p.lists?.length) {
    const sel = (S.provSel[p.id] ??= new Set(p.lists.map((l) => l.id)));
    parts.push(`<div class="pd-row"><b>Опубликованные списки:</b> ${p.lists.map((l) => `<label class="chk sm"><input type="checkbox" class="pl-sel" data-id="${esc(p.id)}" data-list="${esc(l.id)}" ${sel.has(l.id) ? "checked" : ""}> ${esc(l.name)}</label>`).join(" ")}</div>`);
    const busy = S.provBusy.has(p.id), sum = listSummary(p);
    parts.push(`<div class="pd-row"><button type="button" class="btn sm pl-refresh" data-id="${esc(p.id)}" ${busy || !S.cfg ? "disabled" : ""}>${busy ? "Загружаем…" : sum ? "Обновить списки" : "Загрузить списки"}</button>${sum && sum.fresh.length ? `<button type="button" class="btn sm primary pl-add" data-id="${esc(p.id)}">Добавить ${fmt.format(sum.fresh.length)} ${plural(sum.fresh.length, "домен", "домена", "доменов")} в правила</button>` : ""}</div>`);
    if (sum) parts.push(`<div class="pd-row muted">${sum.lines.map(esc).join("<br>")}${sum.fresh.length ? "" : "<br>Все домены списка уже покрыты вашими правилами."}${sum.preset.length ? `<br>Покрытие наборов: ${sum.preset.map(esc).join("; ")}` : ""}</div>`);
  } else if (p.geo) {
    parts.push(`<div class="pd-row muted">Провайдер не публикует список доменов, обновлять нечего. Наборы правил обновляются вместе с программой.</div>`);
  }
  if (!parts.length) return "";
  return `<details class="pdetail" data-id="${esc(p.id)}" ${S.provOpen.has(p.id) ? "open" : ""}><summary>Что поддерживает</summary>${parts.join("")}</details>`;
}

async function refreshProviderLists(id) {
  const p = prov(id);
  if (!p?.lists?.length || S.provBusy.has(id)) return;
  const sel = [...(S.provSel[id] ?? p.lists.map((l) => l.id))];
  if (!sel.length) return toast("Отметьте хотя бы один список", true);
  S.provBusy.add(id);
  renderProviders();
  const got = {};
  for (const lid of sel) {
    try {
      const r = await call("ProviderList", id, lid);
      got[lid] = { name: r.name, domains: r.domains || [] };
    } catch (err) { toast(errText(err), true); }
  }
  S.provBusy.delete(id);
  if (Object.keys(got).length) S.provLists[id] = got;
  renderProviders();
}

async function addProviderDomains(id) {
  const p = prov(id), sum = p && listSummary(p);
  if (!sum?.fresh.length) return;
  let added = 0;
  const ok = await save((c) => {
    const have = new Set(c.rules.map((r) => r.domain));
    for (const d of sum.fresh) if (!coversHost(have, d)) { c.rules.push({ domain: d, providers: [], enabled: true }); have.add(d); added++; }
  });
  if (ok) toast(`Добавлено правил: ${added}`);
  renderAll();
}

$("#provList").addEventListener("toggle", (e) => {
  const d = e.target.closest?.("details.pdetail");
  if (d) { if (d.open) S.provOpen.add(d.dataset.id); else S.provOpen.delete(d.dataset.id); }
}, true);
$("#provList").addEventListener("change", (e) => {
  const c = e.target.closest?.("input.pl-sel");
  if (!c) return;
  const set = (S.provSel[c.dataset.id] ??= new Set());
  if (c.checked) set.add(c.dataset.list); else set.delete(c.dataset.list);
});
$("#provList").addEventListener("click", (e) => {
  const rb = e.target.closest?.("button.pl-refresh"), ab = e.target.closest?.("button.pl-add");
  if (rb) refreshProviderLists(rb.dataset.id); else if (ab) addProviderDomains(ab.dataset.id);
});

function renderProviders() {
  if (!S.cfg) return;
  const list = $("#provList");
  list.innerHTML = "";
  for (const p of S.cfg.providers) {
    const h = healthOf(p.id);
    const row = document.createElement("div");
    row.className = "prov" + (p.enabled ? "" : " off");
    row.style.setProperty("--c", color(p.id));
    const lat = !p.enabled || !h?.checked ? "" : h.ok ? `${Math.round(h.latency_ms)} мс` : "недоступен";
    row.innerHTML = `
      <input type="radio" name="active" ${p.enabled ? "checked" : ""} data-fk="prov:${esc(p.id)}" aria-label="Использовать ${esc(p.name)}">
      <span class="sw"></span>
      <div><span class="nm">${esc(p.name)}</span>${p.geo ? '<span class="tag geo">обход</span>' : ""}${p.builtin ? "" : '<span class="tag">свой</span>'}${provTestTag(p.id)}
        <div class="url">${esc(p.url)}</div>${p.note ? `<div class="note">${esc(p.note)}</div>` : ""}${provDetail(p)}</div>
      <div class="lat ${h && !h.ok && h.checked ? "bad" : ""}" data-id="${esc(p.id)}">${lat}</div>
      <div>${p.builtin ? "" : `<button class="icon-btn" aria-label="Удалить ${esc(p.name)}">✕</button>`}</div>`;
    $("input", row).addEventListener("change", (e) => {
      if (!e.target.checked) return;
      save((c) => { c.active_provider = p.id; }, `Туннель: ${p.name}`).then(renderAll);
    });
    $(".icon-btn", row)?.addEventListener("click", () => {
      save((c) => {
        // checked against the latest saved config, not the stale render
        if (c.active_provider === p.id) throw new Error("Это активный туннель: сначала выберите другой");
        c.providers = c.providers.filter((x) => x.id !== p.id);
      }, "Провайдер удалён");
    });
    list.append(row);
  }
}

$("#provForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const url = f.get("url").trim();
  let id;
  try {
    const u = new URL(url);
    if (u.protocol !== "https:") throw new Error("https only");
    // the whole endpoint identifies a provider: several profiles/ports on one host are legal
    // host for readability + hash of the exact URL: path/query are case-sensitive and "/a/b" != "/a-b"
    let h = 5381;
    for (const ch of u.href) h = ((h * 33) ^ ch.charCodeAt(0)) >>> 0;
    id = "custom-" + u.hostname.toLowerCase().replace(/[^a-z0-9.]+/g, "-") + "-" + h.toString(36);
  } catch { return toast("Адрес должен выглядеть как https://сервер/dns-query", true); }
  if (!S.cfg) return toast("Служба недоступна", true);
  if (S.cfg.providers.some((p) => p.id === id)) return toast("Такой провайдер уже добавлен", true);
  const bootstrap = f.get("bootstrap").split(/[\s,;]+/).filter(Boolean);
  save((c) => c.providers.push({ id, name: f.get("name").trim(), url, bootstrap, enabled: false, builtin: false, geo: !!f.get("geo"), use_get: !!f.get("get") }), "Провайдер добавлен. Чтобы пользоваться им, выберите его в списке")
    .then((ok) => ok && e.target.reset());
});

// ---------- rules ----------
function renderRules() {
  if (!S.cfg) return;
  const ps = $("#presets");
  ps.innerHTML = "";
  for (const p of S.presets) {
    const have = p.domains.filter((d) => S.cfg.rules.some((r) => r.domain === d)).length;
    const all = have === p.domains.length;
    const el = document.createElement("div");
    el.className = "preset";
    el.innerHTML = `<b>${esc(p.name)}</b><small>${esc(p.note)}</small>
      <button class="btn sm ${all ? "" : "primary"}" ${all ? "disabled" : ""}>${all ? "Добавлено" : have ? `Добавить ещё ${p.domains.length - have}` : `Добавить ${p.domains.length} ${plural(p.domains.length, "домен", "домена", "доменов")}`}</button>`;
    $("button", el).addEventListener("click", () => save((c) => {
      for (const d of p.domains) if (!c.rules.some((r) => r.domain === d)) c.rules.push({ domain: d, providers: [], enabled: true });
    }, `Набор «${p.name}» добавлен`));
    ps.append(el);
  }

  const q = $("#ruleFilter").value.trim().toLowerCase();
  const list = $("#ruleList");
  list.innerHTML = "";
  const rules = S.cfg.rules.map((r, i) => ({ r, i })).filter(({ r }) => !q || r.domain.includes(q));
  $("#ruleCount").textContent = S.cfg.rules.length ? `${S.cfg.rules.length} ${plural(S.cfg.rules.length, "правило", "правила", "правил")}` : "";
  $("#rulesClear").classList.toggle("hidden", !S.cfg.rules.length);
  if (!rules.length) list.innerHTML = `<p class="empty-state">${S.cfg.rules.length ? "Ничего не нашлось." : "Правил нет. Добавьте набор выше или впишите домен."}</p>`;
  for (const { r, i } of rules) {
    const row = document.createElement("div");
    row.className = "rule" + (r.enabled ? "" : " off");
    row.innerHTML = `<input type="checkbox" ${r.enabled ? "checked" : ""} aria-label="Правило ${esc(r.domain)} включено"><span class="d">${esc(r.domain)}</span><span class="c"></span><button class="icon-btn del" aria-label="Удалить ${esc(r.domain)}">✕</button>`;
    $("input", row).dataset.fk = "rule-on:" + r.domain;
    $("input", row).addEventListener("change", (e) => save((c) => { const x = c.rules.find((y) => y.domain === r.domain); if (x) x.enabled = e.target.checked; }));
    $(".del", row).addEventListener("click", () => save((c) => { c.rules = c.rules.filter((y) => y.domain !== r.domain); }));
    list.append(row);
  }
}
$("#ruleFilter").addEventListener("input", renderRules);
$("#rulesClear").addEventListener("click", (e) => {
  if (!confirmTwice(e.currentTarget)) return;
  save((c) => (c.rules = []), "Все правила удалены");
});

// non-blocking confirmation: the first click arms, a second within 3 s confirms
function confirmTwice(b) {
  if (b.dataset.armed) { delete b.dataset.armed; b.textContent = b.dataset.orig; return true; }
  b.dataset.orig = b.textContent; b.dataset.armed = "1"; b.textContent = "Нажмите ещё раз";
  setTimeout(() => { if (b.dataset.armed) { delete b.dataset.armed; b.textContent = b.dataset.orig; } }, 3000);
  return false;
}

const cleanDomain = (s) => s.trim().toLowerCase().replace(/^[a-z]+:\/\//, "").split(/[/?#:]/)[0].replace(/^\*\./, "").replace(/\.$/, "");

$("#ruleForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const domain = cleanDomain(f.get("domain"));
  if (!domain) return;
  if (!S.cfg) return toast("Служба недоступна", true);
  if (S.cfg.rules.some((r) => r.domain === domain)) return toast("Для этого домена правило уже есть", true);
  // re-check inside the queued mutation: a double submit passed the check above before the first save landed
  save((c) => { if (!c.rules.some((r) => r.domain === domain)) c.rules.unshift({ domain, providers: [], enabled: true }); }, "Правило добавлено").then((ok) => ok && (e.target.domain.value = ""));
});

// ---------- domain test ----------
let outSeq = 0; // the test and diagnosis share one output: only the newest request may write to it
$("#testForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const domain = cleanDomain(new FormData(e.target).get("domain"));
  const out = $("#testOut");
  const btn = $("button", e.target);
  const my = ++outSeq;
  btn.disabled = true;
  out.innerHTML = `<p class="muted">Спрашиваем выбранный туннель. Первая проверка занимает пару секунд: узнаём адреса их прокси.</p>`;
  let res;
  try { res = await call("TestDomain", domain); } catch (err) { btn.disabled = false; if (my === outSeq) out.innerHTML = `<div class="alert bad">${esc(errText(err))}</div>`; return; }
  btn.disabled = false;
  if (my !== outSeq) return;
  const verdict = (r) => r.error ? `<span class="verdict bad">нет ответа</span>`
    : r.blocked ? `<span class="verdict bad">заблокирован</span>`
    : r.proxied ? `<span class="verdict ok">ведёт через прокси</span>`
    : r.differs ? `<span class="verdict warn" title="Адрес отличается от эталонного DNS, но это не известный прокси провайдера: возможно, CDN отдаёт другой узел">другой адрес</span>`
    : r.rcode && r.rcode !== "NOERROR" ? `<span class="verdict warn">${esc(r.rcode)}</span>`
    : `<span class="verdict plain">${r.geo ? "напрямую" : "эталон"}</span>`;
  res = Array.isArray(res) ? res : [];
  const good = res.filter((r) => r.proxied).sort((a, b) => a.ms - b.ms);
  out.innerHTML = `<div class="tbl-wrap"><table class="tbl"><colgroup><col class="c-prov"><col class="c-res"><col><col class="c-ms"></colgroup><thead><tr><th>Провайдер</th><th>Результат</th><th>Адреса</th><th class="num">мс</th></tr></thead><tbody>${
    res.map((r) => `<tr><td><span class="lb" style="--c:${color(r.provider)}"><i></i>${esc(r.name)}</span></td><td>${verdict(r)}</td><td class="ans" title="${esc(r.error || (r.addrs || []).join(", "))}">${esc(r.error || (r.addrs || []).join(", ") || "—")}</td><td class="num">${r.ms}</td></tr>`).join("")
  }</tbody></table></div>` + (good.length
    ? `<div class="row" style="margin-top:14px"><button class="btn primary wrap" id="mkRule">Создать правило для ${esc(domain)}</button></div>`
    : `<p class="muted" style="margin-top:12px">Выбранный провайдер не подтвердил обход для этого домена: возможно, его нет в списках провайдера или он не заблокирован. Другие провайдеры не опрашивались, при необходимости выберите другой туннель на вкладке «Провайдеры».</p>`);
  $("#mkRule")?.addEventListener("click", () => save((c) => {
    const ex = c.rules.find((r) => r.domain === domain);
    if (ex) ex.enabled = true;
    else c.rules.unshift({ domain, providers: [], enabled: true });
  }, "Правило создано"));
});

// ---------- diagnosis: why doesn't it open ----------
const STAGE = { rule: "Правило", chain: "Выбранный туннель", dns: "Ответы провайдеров", os_dns: "DNS системы", connect: "Соединение (TCP + TLS)" };
function stageDetail(s) {
  const d = s.data || {};
  switch (s.id) {
    case "rule": return d.matched ? `правило «${d.matched}»` : "правила нет — домен идёт через выбранный туннель";
    case "chain": return (d.effective || []).map(provName).join(" → ") || "нет доступных провайдеров";
    case "dns": {
      const ok = (d.providers || []).filter((p) => p.status === "ok" && !p.blocked).length;
      return `${ok} из ${(d.providers || []).length} ответов без ошибок и без 0.0.0.0`;
    }
    case "os_dns": return d.mismatch ? `ожидали ${(d.expected || []).join(", ") || "—"}, у адаптеров ${(d.actual || []).join(", ") || "—"}` : (d.addrs || []).length ? `система видит ${d.addrs.slice(0, 3).join(", ")}` : (d.lookup_error || "");
    case "connect": {
      const p = d.probes || [];
      return p.length ? `${p.filter((x) => x.tls === "ok").length} из ${p.length} адресов принимают TLS` : "нечего проверять";
    }
  }
  return "";
}
$("#diagGo").addEventListener("click", async (e) => {
  const btn = e.currentTarget, out = $("#testOut");
  const domain = cleanDomain($("#testForm").domain.value);
  if (!domain) return toast("Введите домен", true);
  const my = ++outSeq;
  btn.disabled = true;
  out.innerHTML = `<p class="muted">Диагностика: правило, провайдеры, DNS системы, соединение. До 12 секунд.</p>`;
  let r;
  try { r = await call("DiagnoseDomain", domain); } catch (err) { btn.disabled = false; if (my === outSeq) out.innerHTML = `<div class="alert bad">${esc(errText(err))}</div>`; return; }
  btn.disabled = false;
  if (my !== outSeq) return;
  if (!r || typeof r !== "object") { out.innerHTML = `<div class="alert bad">Служба вернула некорректный ответ</div>`; return; }
  const icon = { ok: "✓", failed: "✕", partial: "!", skipped: "·" };
  const v = r.verdict || {};
  const good = v.code === "transport_ok";
  $("#testStatus").textContent = `Диагностика ${domain}: ${v.action?.text || v.code}`;
  out.innerHTML = `<div class="diag-v ${good ? "ok" : "bad"}"><b>${esc(v.action?.text || v.code)}</b><span class="mono">${esc(v.code)} · ${esc(STAGE[v.stage] || v.stage)}</span></div>
    <ol class="quick-stages">${(r.stages || []).map((s, i) => `<li class="qs ${s.status === "ok" ? "ok" : s.status === "failed" && s.id !== "rule" ? "failed" : s.status === "partial" ? "warn" : "info"}" style="--i:${i}"><i aria-hidden="true">${icon[s.status] || "·"}</i><div><b>${esc(STAGE[s.id] || s.id)}</b><span>${esc(stageDetail(s))}</span></div></li>`).join("")}</ol>
    ${good ? `<p class="muted small">Соединение с адресом работает: если сайт всё равно не открывается, причина выше TLS. Чаще всего это «Безопасный DNS» в браузере или VPN.</p>` : ""}`;
  const act = v.action?.code;
  const ACT = { add_rule: "Добавить правило", change_provider: "К провайдерам", reapply_dns: "Применить DNS заново", retry: "Повторить" };
  if (ACT[act]) {
    const b = document.createElement("button");
    b.type = "button"; b.className = "btn sm primary"; b.textContent = ACT[act];
    b.addEventListener("click", async () => {
      b.disabled = true;
      try {
        if (act === "add_rule") await save((c) => { const ex = c.rules.find((x) => x.domain === domain); if (ex) ex.enabled = true; else c.rules.unshift({ domain, providers: [], enabled: true }); }, `Правило для ${domain} добавлено`);
        else if (act === "change_provider") { $('#nav button[data-tab="providers"]').click(); $("#provList input:checked")?.focus(); }
        else if (act === "reapply_dns") { await call("ReapplyDNS"); toast("DNS клиента применён заново"); }
        else if (act === "retry") { $("#testForm").domain.value = domain; $("#diagGo").click(); }
      } catch (err) { toast(errText(err), true); }
      b.disabled = false;
    });
    $(".diag-v", out).append(b);
  }
});

// ---------- settings ----------
async function renderSettings() {
  try { $("#autostart").checked = await call("GetAutostart"); } catch {}
  try {
    const i = await call("Info");
    const svcLabel = { running: "работает", stopped: "остановлена", not_installed: "не установлена", starting: "запускается", no_access: "нет доступа" }[S.state?.service] || "неизвестно";
    $("#info").innerHTML = `<dt>Версия</dt><dd>${esc(i.version)}, ${esc(i.os)}</dd><dt>Служба</dt><dd>${svcLabel}</dd><dt>Данные службы</dt><dd>${esc(i.state_dir)}</dd><dt>Журнал службы</dt><dd>${esc(i.log_file)}</dd><dt>Файл службы</dt><dd>${esc(i.installed)}</dd>`;
  } catch {}
  if (!S.cfg) return;
  $$("input[name=mode]").forEach((r) => (r.checked = r.value === S.cfg.mode));
  const f = $("#setForm");
  for (const k of ["block_aaaa_for_rules", "block_https_rr_for_rules"]) f[k].checked = !!S.cfg[k];
  for (const k of ["timeout_ms", "cache_size", "min_ttl", "max_ttl", "health_interval_s"]) f[k].value = S.cfg[k];
  f.listen.value = S.cfg.listen.join(", ");
  f.bootstrap_dns.value = S.cfg.bootstrap_dns.join(", ");
}
$$("input[name=mode]").forEach((r) => r.addEventListener("change", () => save((c) => (c.mode = r.value), r.value === "hosts" ? "Режим: файл hosts" : "Режим: локальный DNS").then(() => $$("input[name=mode]").forEach((x) => (x.checked = x.value === S.cfg?.mode)))));
$("#setForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = e.target;
  const list = (s) => s.split(/[\s,;]+/).filter(Boolean);
  save((c) => {
    c.block_aaaa_for_rules = f.block_aaaa_for_rules.checked;
    c.block_https_rr_for_rules = f.block_https_rr_for_rules.checked;
    for (const k of ["timeout_ms", "cache_size", "min_ttl", "max_ttl", "health_interval_s"]) c[k] = Number(f[k].value);
    c.listen = list(f.listen.value);
    c.bootstrap_dns = list(f.bootstrap_dns.value);
  }, "Настройки сохранены").then(renderSettings);
});
$("#autostart").addEventListener("change", async (e) => {
  try { await call("SetAutostart", e.target.checked); } catch (err) { toast(errText(err), true); e.target.checked = !e.target.checked; }
});

// ---------- boot ----------
try { S.presets = (await call("Presets")) || []; } catch {}
updateQuick(S.state?.service === "running");
const safeRefresh = () => refresh().catch((e) => console.error("refresh", e));
setInterval(safeRefresh, 2000); // timers start even if the first refresh fails
pollLog();
safeRefresh();
