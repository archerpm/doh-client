// Route map: a swimlane "live plan" of how DNS queries travel.
//   этот компьютер → DoH-клиент → провайдер (карточка на линию) → прокси за рубежом / напрямую
// Orthogonal connectors with rounded corners; real log entries become dots flowing along them,
// cards count their hits and flash when a query passes through.

const NS = "http://www.w3.org/2000/svg";
const W = 1000;
const X_PC = 20, W_PC = 150, X_CLIENT = 232, W_CLIENT = 150, X_PROV = 470, W_PROV = 230, X_BUS = 752, X_TERM = 806, W_TERM = 174;
const CARD_H = 38, TERM_OFF = 74, TOP = 44;

const el = (tag, attrs = {}, parent) => {
  const e = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  if (parent) parent.append(e);
  return e;
};

// polyline with rounded corners (r px); pts = [[x,y],...]
function rpath(pts, r = 12) {
  let d = `M${pts[0][0]},${pts[0][1]}`;
  for (let i = 1; i < pts.length - 1; i++) {
    const [px, py] = pts[i - 1], [cx, cy] = pts[i], [nx, ny] = pts[i + 1];
    const l1 = Math.hypot(cx - px, cy - py), l2 = Math.hypot(nx - cx, ny - cy);
    const rr = Math.min(r, l1 / 2, l2 / 2);
    if (rr < 0.5) { d += ` L${cx},${cy}`; continue; }
    const ax = cx - ((cx - px) / l1) * rr, ay = cy - ((cy - py) / l1) * rr;
    const bx = cx + ((nx - cx) / l2) * rr, by = cy + ((ny - cy) / l2) * rr;
    d += ` L${ax},${ay} Q${cx},${cy} ${bx},${by}`;
  }
  const last = pts[pts.length - 1];
  return d + ` L${last[0]},${last[1]}`;
}

export class RouteMap {
  constructor(svg, { reducedMotion = false } = {}) {
    this.isGeo = () => true;
    this.svg = svg;
    this.reduced = reducedMotion;
    this.packets = [];
    this.providers = [];
    this.active = false;
    this.paths = {};
    this.hits = {};
    this.lastTerm = { proxy: [], direct: [] };
    this.raf = null;
    this.tick = this.tick.bind(this);
  }

  // providers: [{id, name, color, geo, down, latency}]
  setModel(providers, active) {
    const sig = JSON.stringify(providers.map((p) => [p.id, p.color, p.geo, p.name]));
    if (sig !== this.sig) {
      this.sig = sig;
      this.providers = providers;
      this.build();
    }
    this.providers = providers;
    this.updateHealth();
    this.setActive(active);
  }

  build() {
    const n = Math.max(1, this.providers.length);
    const GAP = n > 1 ? Math.min(54, 380 / (n - 1)) : 54;
    const ph = Math.min(CARD_H, GAP - 8);
    const H = Math.max(300, TOP + 70 + (n - 1) * GAP + 70);
    const cy = TOP + (H - TOP) / 2;
    this.cy = cy;
    this.svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
    this.svg.innerHTML = "";
    const defs = el("defs", {}, this.svg);
    const f = el("filter", { id: "glow", x: "-200%", y: "-200%", width: "500%", height: "500%" }, defs);
    el("feGaussianBlur", { stdDeviation: "2.6", result: "b" }, f);
    const m = el("feMerge", {}, f);
    el("feMergeNode", { in: "b" }, m);
    el("feMergeNode", { in: "SourceGraphic" }, m);

    this.gLines = el("g", { class: "lines" }, this.svg);
    this.gStations = el("g", { class: "stations" }, this.svg);
    this.gPackets = el("g", { class: "packets" }, this.svg);
    this.paths = {};
    this.hitEls = {};
    const line = (key, pts, cls, color) => {
      const p = el("path", { d: rpath(pts), class: "line " + cls, style: `--c:${color}` }, this.gLines);
      this.paths[key] = p;
      return p;
    };

    const proxyY = cy - TERM_OFF, directY = cy + TERM_OFF;
    const xIn = X_PROV - 34;
    line("trunk", [[X_PC + W_PC, cy], [X_CLIENT, cy]], "trunk", "var(--fg)");
    this.providers.forEach((p, i) => {
      const y = cy + (i - (n - 1) / 2) * GAP;
      p._y = y;
      line("in:" + p.id, [[X_CLIENT + W_CLIENT, cy], [xIn, cy], [xIn, y], [X_PROV, y]], "prov", p.color);
      line("out:" + p.id, [[X_PROV + W_PROV, y], [X_BUS, y], [X_BUS, cy]], "prov", p.color);
    });
    line("term:proxy", [[X_BUS, cy], [X_BUS, proxyY], [X_TERM, proxyY]], "term proxy", "var(--accent)");
    line("term:direct", [[X_BUS, cy], [X_BUS, directY], [X_TERM, directY]], "term direct", "var(--muted)");

    const text = (x, y, t, cls = "", anchor = "start", parent = this.gStations) => {
      const e = el("text", { x, y, class: "lbl " + cls, "text-anchor": anchor }, parent);
      e.textContent = t;
      return e;
    };
    const card = (x, y, w, h, cls, color) => el("rect", { x, y: y - h / 2, width: w, height: h, rx: 10, class: "card " + cls, style: color ? `--c:${color}` : "" }, this.gStations);

    // lane headers
    [[X_PC, "Приложение"], [X_CLIENT, "Клиент"], [X_PROV, "Провайдер"], [X_TERM, "Выход"]].forEach(([x, t]) => {
      el("circle", { cx: x + 3, cy: 16, r: 3, class: "lane-dot" }, this.gStations);
      text(x + 14, 20, t, "lane");
    });

    card(X_PC, cy, W_PC, 46, "pc");
    text(X_PC + 16, cy - 2, "Этот компьютер", "cap");
    text(X_PC + 16, cy + 14, "браузер · системный DNS", "sub");

    this.hub = card(X_CLIENT, cy, W_CLIENT, 58, "hub");
    text(X_CLIENT + 16, cy - 4, "DoH-клиент", "cap");
    this.clientNote = text(X_CLIENT + 16, cy + 14, "127.0.0.1", "sub");
    this.hubCount = text(X_CLIENT + W_CLIENT - 14, cy - 4, "", "sub", "end");

    this.stationEls = {};
    for (const p of this.providers) {
      const c = card(X_PROV, p._y, W_PROV, ph, "prov", p.color);
      el("rect", { x: X_PROV + 12, y: p._y - 7, width: 3, height: 14, rx: 1.5, class: "mark", style: `--c:${p.color}` }, this.gStations);
      const name = text(X_PROV + 26, p._y + 4.5, p.name.length > 17 ? p.name.replace(" (Cloudflare)", " CF").slice(0, 17) : p.name, "pname");
      el("title", {}, name).textContent = p.name;
      const meta = text(X_PROV + W_PROV - 12, p._y + 4, "", "sub", "end");
      this.stationEls[p.id] = { c, name, meta, hits: 0 };
    }

    const tp = card(X_TERM, proxyY, W_TERM, 50, "term-proxy");
    const td = card(X_TERM, directY, W_TERM, 50, "term-direct");
    text(X_TERM + 14, proxyY - 5, "Прокси за рубежом", "cap");
    text(X_TERM + 14, directY - 5, "Напрямую", "cap");
    this.termText = {
      proxy: text(X_TERM + 14, proxyY + 13, "", "tlast"),
      direct: text(X_TERM + 14, directY + 13, "", "tlast"),
    };
    this.termCards = { proxy: tp, direct: td, hub: this.hub };
    this.termY = { proxy: proxyY, direct: directY };
    this.svg.classList.toggle("on", this.active);
  }

  updateHealth() {
    for (const p of this.providers) {
      const s = this.stationEls?.[p.id];
      if (!s) continue;
      s.c.classList.toggle("down", !!p.down);
      s.base = p.down ? "недоступен" : p.latency ? `${p.latency} мс` : "";
      s.meta.textContent = s.hits && !p.down ? `${s.hits} · ${s.base}` : s.base;
      s.meta.classList.toggle("bad", !!p.down);
    }
  }

  setActive(on) {
    if (on === this.active) return;
    this.active = on;
    // the draw-on sequence restarts only on a real off→on change
    this.svg.classList.remove("on", "drawn");
    void this.svg.getBoundingClientRect();
    this.svg.classList.toggle("on", on);
    if (on) setTimeout(() => this.svg.classList.add("drawn"), this.reduced ? 0 : 1400);
    if (!on) this.clearPackets();
  }

  setReducedMotion(on) {
    this.reduced = on;
    if (on) this.clearPackets();
  }

  clearPackets() {
    for (const p of this.packets) p.node.remove();
    this.packets = [];
  }

  // entry: resolver log entry; delay: ms to stagger a batch
  emit(e, delay = 0) {
    if (!this.active || this.reduced) {
      this.note(e);
      return;
    }
    if (this.packets.length > 60) return; // keep it readable under load
    const segs = [this.paths.trunk];
    let end = "client", color = "var(--fg)";
    const prov = this.providers.find((p) => p.id === e.provider);
    if (e.cached || e.synth) {
      end = e.cached ? "cache" : "drop";
    } else if (prov && this.paths["in:" + prov.id]) {
      color = prov.color;
      segs.push(this.paths["in:" + prov.id]);
      if (e.error && !(e.answers || []).length) {
        end = "fail";
      } else {
        segs.push(this.paths["out:" + prov.id]);
        const proxied = !!e.rule && this.isGeo(e.provider) && !(e.answers || []).every((a) => a === "0.0.0.0");
        segs.push(this.paths[proxied ? "term:proxy" : "term:direct"]);
        end = proxied ? "proxy" : "direct";
      }
    } else {
      end = "fail";
    }
    const node = el("g", { class: "pk", style: `--c:${color}` });
    const trail = [0, 1, 2].map((k) => el("circle", { r: 2.8 - k * 0.7, class: "tr", opacity: 0.45 - k * 0.13 }, node));
    const head = el("circle", { r: 3.2, class: "hd", filter: "url(#glow)" }, node);
    const lens = segs.map((s) => s.getTotalLength());
    const total = lens.reduce((a, b) => a + b, 0);
    this.packets.push({ node, head, trail, segs, lens, total, t0: performance.now() + delay, speed: 0.42, hist: [], end, entry: e, color });
    this.gPackets.append(node);
    node.style.opacity = 0;
    if (!this.raf) this.raf = requestAnimationFrame(this.tick);
  }

  pointAt(pk, dist) {
    for (let i = 0; i < pk.segs.length; i++) {
      if (dist <= pk.lens[i] || i === pk.segs.length - 1) return pk.segs[i].getPointAtLength(Math.min(dist, pk.lens[i]));
      dist -= pk.lens[i];
    }
  }

  tick(now) {
    this.raf = null;
    const alive = [];
    for (const pk of this.packets) {
      const t = now - pk.t0;
      if (t < 0) { alive.push(pk); continue; }
      pk.node.style.opacity = 1;
      const d = Math.min(pk.total, t * pk.speed);
      const pt = this.pointAt(pk, d);
      pk.hist.unshift(pt);
      if (pk.hist.length > 12) pk.hist.length = 12;
      pk.head.setAttribute("cx", pt.x); pk.head.setAttribute("cy", pt.y);
      pk.trail.forEach((c, k) => { const h = pk.hist[(k + 1) * 3] || pt; c.setAttribute("cx", h.x); c.setAttribute("cy", h.y); });
      if (d >= pk.total) { this.arrive(pk, pt); pk.node.remove(); continue; }
      alive.push(pk);
    }
    this.packets = alive;
    if (alive.length) this.raf = requestAnimationFrame(this.tick);
  }

  flash(node) {
    if (!node || this.reduced) return;
    node.classList.remove("hit");
    void node.getBoundingClientRect();
    node.classList.add("hit");
  }

  arrive(pk, pt) {
    const pid = pk.entry.provider, st = this.stationEls?.[pid];
    if (st && !pk.entry.cached && !pk.entry.synth) {
      st.hits++;
      st.meta.textContent = `${st.hits} · ${st.base || ""}`.replace(/ · $/, "");
      this.flash(st.c);
    }
    if (pk.end === "proxy" || pk.end === "direct") this.flash(this.termCards?.[pk.end]);
    const cls = { cache: "cache", drop: "drop", fail: "fail", proxy: "proxy", direct: "direct", client: "drop" }[pk.end];
    const ring = el("circle", { cx: pt.x, cy: pt.y, r: 5, class: "burst " + cls, style: `--c:${pk.color}` }, this.gPackets);
    // removal must not depend on the animation running (reduced motion disables it)
    setTimeout(() => ring.remove(), 800);
    this.note(pk.entry);
  }

  note(e) {
    const where = e.cached || e.synth || !this.termText ? null
      : e.rule && this.isGeo(e.provider) && !(e.answers || []).every((a) => a === "0.0.0.0") ? "proxy" : e.error ? null : "direct";
    if (!where) return;
    const list = this.lastTerm[where];
    if (list[0] !== e.name) list.unshift(e.name);
    list.length = Math.min(list.length, 1);
    this.termText[where].textContent = list[0] || "";
  }
}
