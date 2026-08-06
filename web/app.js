"use strict";
/* Argus frontend — terminal/HUD build.
   Connects to the SSE stream and renders every panel. No framework, no chart
   lib: charts are hand-drawn SVG, meters are segmented block bars. Dynamic
   values are applied as inline styles so nothing depends on runtime class gen. */

// ------------------------------------------------------------------ config
const HIST = 80;
const NAV = [
  ["overview", "Overview", "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z"],
  ["cpu", "CPU", "M6 6h12v12H6zM9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"],
  ["memory", "Memory", "M3 8h18v9H3zM7 12v3M11 12v3M15 12v3M19 12v3M6 8V6M18 8V6"],
  ["disk", "Disk", "M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"],
  ["network", "Network", "M3 12h4l3 8 4-16 3 8h4"],
  ["procs", "Processes", "M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"],
  ["docker", "Docker", "M3 13h18v3a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4zM6 13V9h3v4M11 13V9h3v4M11 9V5h3v4"],
  ["system", "System", "M9 3h6l1 4 4 1v6l-4 1-1 4H9l-1-4-4-1V8l4-1zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z"],
];

// e-ink grayscale palette (chart series distinguished by brightness)
const ACC = "#e6e6e6", CYAN = "#8f8f8f", LIME = "#bdbdbd", AMBER = "#d6d6d6", RED = "#f2f2f2", VIOLET = "#707070";

// -------------------------------------------------------------- utilities
const $ = (id) => document.getElementById(id);
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

function fmtBytes(n, digits = 1) {
  if (!n || n < 1) return "0 B";
  const u = ["B", "K", "M", "G", "T", "P"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return (n / Math.pow(1024, i)).toFixed(i ? digits : 0) + u[i];
}
function fmtRate(bps) { return fmtBytes(bps) + "/s"; }
function fmtDuration(sec) {
  sec = Math.floor(sec);
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
// grayscale intensity ramp — dim when idle, bright/white under load
function heat(pct) {
  if (pct >= 90) return "#ffffff";
  if (pct >= 75) return "#d4d4d4";
  if (pct >= 50) return "#a6a6a6";
  return "#6f6f6f";
}
function push(arr, v) { arr.push(v); if (arr.length > HIST) arr.shift(); }

const H = { cpu: [], mem: [], rx: [], tx: [] };

// ------------------------------------------------------------------ charts
function lineChart(svg, series, opts = {}) {
  const W = 100, Ht = 100;
  let max = opts.max;
  if (max == null) {
    max = 1;
    for (const s of series) for (const v of s.data) if (v > max) max = v;
    max *= 1.15;
  }
  // Fixed spacing across the full history window so the line grows in from the
  // left as samples accumulate, rather than stretching a few points wide.
  const xstep = W / (HIST - 1);
  const parts = ["<defs>"];
  series.forEach((s, i) => {
    parts.push(`<linearGradient id="g${i}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="${s.color}" stop-opacity="0.28"/>
      <stop offset="100%" stop-color="${s.color}" stop-opacity="0"/></linearGradient>`);
  });
  parts.push("</defs>");
  for (let g = 1; g < 4; g++) {
    const y = (Ht / 4) * g;
    parts.push(`<line x1="0" y1="${y}" x2="${W}" y2="${y}" stroke="rgba(255,255,255,0.08)" stroke-width="0.5" vector-effect="non-scaling-stroke"/>`);
  }
  series.forEach((s, i) => {
    const d = s.data;
    if (!d.length) return;
    let line = "";
    d.forEach((v, j) => {
      const x = j * xstep, y = Ht - clamp(v / max, 0, 1) * Ht;
      line += (j === 0 ? "M" : "L") + x.toFixed(2) + " " + y.toFixed(2) + " ";
    });
    const area = line + `L${((d.length - 1) * xstep).toFixed(2)} ${Ht} L0 ${Ht} Z`;
    if (s.fill !== false) parts.push(`<path d="${area}" fill="url(#g${i})" stroke="none"/>`);
    parts.push(`<path d="${line}" fill="none" stroke="${s.color}" stroke-width="1.5" vector-effect="non-scaling-stroke" stroke-linejoin="round"/>`);
  });
  svg.setAttribute("viewBox", `0 0 ${W} ${Ht}`);
  svg.innerHTML = parts.join("");
}

// Segmented block meter — the terminal analogue of a gauge.
class Meter {
  constructor(host, { segments = 36, label = "" } = {}) {
    this.n = segments;
    host.innerHTML =
      `<div class="flex items-end justify-between mb-2">
         <div class="leading-none"><span class="mv text-4xl font-bold tabnum" style="color:${ACC}">0</span><span class="text-xl text-[#6a6a6a]">%</span></div>
         <div class="ml text-[10px] uppercase tracking-widest text-[#6a6a6a]">${label}</div>
       </div>
       <div class="ms flex gap-[3px] h-6"></div>`;
    this.mv = host.querySelector(".mv");
    this.ml = host.querySelector(".ml");
    const row = host.querySelector(".ms");
    this.cells = [];
    for (let i = 0; i < this.n; i++) {
      const c = document.createElement("div");
      c.style.flex = "1";
      c.style.background = "rgba(255,255,255,0.08)";
      c.style.transition = "background .3s";
      row.appendChild(c);
      this.cells.push(c);
    }
  }
  update(pct, centerText, sub) {
    pct = clamp(pct || 0, 0, 100);
    const on = Math.round((pct / 100) * this.n);
    const col = heat(pct);
    for (let i = 0; i < this.n; i++)
      this.cells[i].style.background = i < on ? col : "rgba(255,255,255,0.08)";
    this.mv.textContent = centerText != null ? centerText : Math.round(pct);
    this.mv.style.color = col;
    if (sub != null) this.ml.textContent = sub;
  }
}

function barRow(label, value, pct, color) {
  return `<div>
    <div class="flex justify-between text-[11px] mb-1">
      <span class="text-[#7d7d7d] uppercase tracking-wider">${label}</span>
      <span class="text-[#b0b0b0] tabnum">${value}</span>
    </div>
    <div class="h-1.5 bg-[rgba(255,255,255,0.08)]"><div class="h-full" style="width:${clamp(pct,0,100)}%;background:${color};transition:width .4s"></div></div>
  </div>`;
}

let memGauge, memGauge2, swapGauge;

// ------------------------------------------------------------------ nav
let current = "overview";
function buildNav() {
  const nav = $("nav");
  nav.innerHTML = NAV.map(([id, label, path]) =>
    `<a class="nav-item ${id === current ? "active" : ""}" data-go="${id}">
      <svg viewBox="0 0 24 24" class="w-[16px] h-[16px]" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="${path}"/></svg>
      <span>${label}</span>
    </a>`).join("");
  nav.querySelectorAll("[data-go]").forEach((a) => a.addEventListener("click", () => go(a.dataset.go)));
}
function go(id) {
  current = id;
  document.querySelectorAll(".tab").forEach((s) => s.classList.toggle("hidden", s.dataset.tab !== id));
  document.querySelectorAll("[data-go]").forEach((a) => a.classList.toggle("active", a.dataset.go === id));
  $("pageTitle").textContent = ((NAV.find((n) => n[0] === id) || [])[1] || "").toLowerCase();
  if (last) render(last);
}

// ------------------------------------------------------------------ render
let last = null;

function render(s) {
  $("hostname").textContent = s.host.hostname || "—";
  $("platform").textContent = `${s.host.platform || ""} ${s.host.platformVersion || ""} ${s.host.arch}`.trim();
  $("uptime").textContent = fmtDuration(s.host.uptime);
  $("footHost").textContent = s.host.hostname || "";

  push(H.cpu, s.cpu.total);
  push(H.mem, s.memory.usedPercent);
  const rx = sum(s.network, "rxRate"), tx = sum(s.network, "txRate");
  push(H.rx, rx); push(H.tx, tx);

  switch (current) {
    case "overview": renderOverview(s, rx, tx); break;
    case "cpu": renderCPU(s); break;
    case "memory": renderMemory(s); break;
    case "disk": renderDisk(s); break;
    case "network": renderNetwork(s, rx, tx); break;
    case "procs": renderProcs(s); break;
    case "docker": renderDocker(s); break;
    case "system": renderSystem(s); break;
  }
}

function renderOverview(s, rx, tx) {
  const disk = rootDisk(s.disks);
  $("kpiRow").innerHTML = [
    kpi("CPU", s.cpu.total.toFixed(0) + "%", `${s.host.cpuLogical} threads`, s.cpu.total),
    kpi("MEM", s.memory.usedPercent.toFixed(0) + "%", fmtBytes(s.memory.used) + "/" + fmtBytes(s.memory.total), s.memory.usedPercent),
    kpi("SWAP", s.swap.usedPercent.toFixed(0) + "%", fmtBytes(s.swap.used) + "/" + fmtBytes(s.swap.total), s.swap.usedPercent),
    kpi("LOAD", s.load.load1.toFixed(2), `${s.load.load5.toFixed(2)} ${s.load.load15.toFixed(2)}`, clamp(s.load.load1 / (s.host.cpuLogical || 1) * 100, 0, 100)),
    kpi("DISK", disk ? disk.usedPercent.toFixed(0) + "%" : "—", disk ? fmtBytes(disk.used) + "/" + fmtBytes(disk.total) : "", disk ? disk.usedPercent : 0),
    kpi("NET", fmtRate(rx + tx), `D${fmtRate(rx)} U${fmtRate(tx)}`, clamp((rx + tx) / (12 * 1024 * 1024) * 100, 2, 100)),
  ].join("");

  $("cpuNow").textContent = s.cpu.total.toFixed(1) + "%";
  lineChart($("cpuChart"), [{ data: H.cpu, color: ACC }], { max: 100 });
  $("coreBars").innerHTML = (s.cpu.perCore || []).map((v, i) =>
    `<div title="core ${i}: ${v.toFixed(0)}%">
      <div class="h-16 flex items-end bg-[rgba(255,255,255,0.06)] border border-[rgba(255,255,255,0.1)]"><div class="w-full" style="height:${clamp(v,2,100)}%;background:${heat(v)};transition:height .35s"></div></div>
      <div class="text-center text-[9px] text-[#6a6a6a] mt-1 tabnum">${v.toFixed(0)}</div>
    </div>`).join("");

  $("memNow").textContent = fmtBytes(s.memory.used) + "/" + fmtBytes(s.memory.total);
  memGauge.update(s.memory.usedPercent, null, "in use");
  $("memBreakdown").innerHTML = memBars(s);

  $("netNow").textContent = `D ${fmtRate(rx)} U ${fmtRate(tx)}`;
  lineChart($("netChart"), [{ data: H.rx, color: ACC }, { data: H.tx, color: CYAN }]);
  $("netLegend").innerHTML = legendDot(ACC, "rx/dl", fmtRate(rx)) + legendDot(CYAN, "tx/ul", fmtRate(tx));

  $("procMini").innerHTML = miniProcs(s.procs.slice(0, 6));
}

function renderCPU(s) {
  $("cpuKpi").innerHTML = [
    kpi("UTIL", s.cpu.total.toFixed(1) + "%", "aggregate", s.cpu.total),
    kpi("LOAD.1", s.load.load1.toFixed(2), "1 minute", clamp(s.load.load1 / (s.host.cpuLogical || 1) * 100, 0, 100)),
    kpi("CORES", String(s.host.cpuPhysical || "—"), "physical", 0),
    kpi("THREADS", String(s.host.cpuLogical || "—"), "logical", 0),
  ].join("");
  $("cpuNow2").textContent = s.cpu.total.toFixed(1) + "%";
  lineChart($("cpuChart2"), [{ data: H.cpu, color: ACC }], { max: 100 });
  $("coreGrid").innerHTML = (s.cpu.perCore || []).map((v, i) =>
    `<div class="stat !p-2.5">
      <div class="flex justify-between text-[11px]"><span class="text-[#7d7d7d]">CORE ${String(i).padStart(2,"0")}</span><span class="tabnum" style="color:${heat(v)}">${v.toFixed(0)}%</span></div>
      <div class="h-1.5 bg-[rgba(255,255,255,0.08)]"><div class="h-full" style="width:${clamp(v,0,100)}%;background:${heat(v)};transition:width .35s"></div></div>
    </div>`).join("");
}

function renderMemory(s) {
  $("memNow2").textContent = fmtBytes(s.memory.used) + "/" + fmtBytes(s.memory.total);
  memGauge2.update(s.memory.usedPercent, null, "in use");
  $("memBreakdown2").innerHTML = memBars(s);
  $("swapNow").textContent = fmtBytes(s.swap.used) + "/" + fmtBytes(s.swap.total);
  swapGauge.update(s.swap.usedPercent, null, "in use");
  $("swapBreakdown").innerHTML =
    barRow("used", fmtBytes(s.swap.used), s.swap.usedPercent, RED) +
    barRow("free", fmtBytes(s.swap.free), s.swap.total ? s.swap.free / s.swap.total * 100 : 0, ACC);
  lineChart($("memChart"), [{ data: H.mem, color: CYAN }], { max: 100 });
}

function renderDisk(s) {
  $("diskGrid").innerHTML = (s.disks || []).map((d) =>
    `<div class="panel">
      <div class="flex items-start justify-between mb-3">
        <div class="min-w-0">
          <div class="font-bold text-[#e6e6e6] truncate">${esc(d.mount)}</div>
          <div class="text-[10px] text-[#6a6a6a] truncate uppercase tracking-wider">${esc(d.device)} · ${esc(d.fstype)}</div>
        </div>
        <span class="badge shrink-0" style="color:${heat(d.usedPercent)};border-color:${heat(d.usedPercent)}44">${d.usedPercent.toFixed(0)}%</span>
      </div>
      ${barRow("used", fmtBytes(d.used) + "/" + fmtBytes(d.total), d.usedPercent, heat(d.usedPercent))}
      <div class="grid grid-cols-3 gap-2 mt-3">
        ${miniStat("free", fmtBytes(d.free))}
        ${miniStat("read", fmtRate(d.readRate))}
        ${miniStat("write", fmtRate(d.writeRate))}
      </div>
    </div>`).join("") || emptyPanel("no mounted filesystems found.");
}

function renderNetwork(s, rx, tx) {
  $("netNow2").textContent = `D ${fmtRate(rx)} U ${fmtRate(tx)}`;
  lineChart($("netChart2"), [{ data: H.rx, color: ACC }, { data: H.tx, color: CYAN }]);
  $("netLegend2").innerHTML = legendDot(ACC, "rx/dl", fmtRate(rx)) + legendDot(CYAN, "tx/ul", fmtRate(tx));
  $("netGrid").innerHTML = (s.network || []).map((n) =>
    `<div class="panel">
      <div class="flex items-center justify-between mb-3">
        <span class="font-bold text-[#e6e6e6]">${esc(n.name)}</span>
        <span class="badge">${fmtRate(n.rxRate + n.txRate)}</span>
      </div>
      <div class="grid grid-cols-2 gap-4">
        <div>${legendDot(ACC, "rx", fmtRate(n.rxRate))}<div class="text-[10px] text-[#6a6a6a] mt-1">total ${fmtBytes(n.rxBytes)}</div></div>
        <div>${legendDot(CYAN, "tx", fmtRate(n.txRate))}<div class="text-[10px] text-[#6a6a6a] mt-1">total ${fmtBytes(n.txBytes)}</div></div>
      </div>
    </div>`).join("") || emptyPanel("no active network interfaces.");
}

// ---- process sorting (client-side over the server's candidate pool) ----
const NUMERIC = new Set(["pid", "cpu", "memPct", "rss"]);
let procSort = { key: "cpu", dir: -1 }; // dir: -1 desc, +1 asc

function sortedProcs(list) {
  const { key, dir } = procSort;
  return (list || []).slice().sort((a, b) => {
    if (NUMERIC.has(key)) return ((a[key] || 0) - (b[key] || 0)) * dir;
    const av = String(a[key] || "").toLowerCase(), bv = String(b[key] || "").toLowerCase();
    return av < bv ? -dir : av > bv ? dir : 0;
  });
}
function updateSortHeaders() {
  document.querySelectorAll("#procHead th[data-key]").forEach((th) => {
    const active = th.dataset.key === procSort.key;
    th.style.color = active ? ACC : "";
    const ar = th.querySelector(".ar");
    if (ar) ar.textContent = active ? (procSort.dir === -1 ? " ▼" : " ▲") : "";
  });
}
function setupProcSort() {
  document.querySelectorAll("#procHead th[data-key]").forEach((th) => {
    th.addEventListener("click", () => {
      const key = th.dataset.key;
      if (procSort.key === key) procSort.dir *= -1;
      else procSort = { key, dir: NUMERIC.has(key) ? -1 : 1 };
      if (last) renderProcs(last);
    });
  });
}

function renderProcs(s) {
  $("procCount").textContent = `${s.host.procCount} total // showing ${s.procs.length}`;
  $("procTable").innerHTML = sortedProcs(s.procs).map((p) =>
    `<tr class="border-b border-[rgba(255,255,255,0.07)] hover:bg-[rgba(255,255,255,0.04)]">
      <td class="py-1.5 pr-4 text-[#6a6a6a]">${p.pid}</td>
      <td class="py-1.5 pr-4 text-[#d0d0d0] truncate max-w-[240px]">${esc(p.name)}</td>
      <td class="py-1.5 pr-4 text-[#7d7d7d]">${esc(p.user)}</td>
      <td class="py-1.5 pr-4"><span class="text-[10px] px-1.5 py-0.5 border border-[rgba(255,255,255,0.15)] text-[#7d7d7d] uppercase">${esc(p.status || "—")}</span></td>
      <td class="py-1.5 pr-4 text-right" style="color:${heat(p.cpu)}">${p.cpu.toFixed(1)}</td>
      <td class="py-1.5 pr-4 text-right text-[#b0b0b0]">${p.memPct.toFixed(1)}</td>
      <td class="py-1.5 text-right text-[#7d7d7d]">${fmtBytes(p.rss)}</td>
    </tr>`).join("");
  updateSortHeaders();
}

function renderDocker(s) {
  const has = Array.isArray(s.docker) && s.docker.length > 0;
  $("dockerEmpty").classList.toggle("hidden", has);
  if (!has) { $("dockerGrid").innerHTML = ""; return; }
  $("dockerGrid").innerHTML = s.docker.map((c) => {
    const running = c.state === "running";
    const dot = running ? ACC : "#6a6a6a";
    return `<div class="panel">
      <div class="flex items-center justify-between mb-2">
        <div class="flex items-center gap-2 min-w-0">
          <span class="w-2 h-2 shrink-0" style="background:${dot}"></span>
          <span class="font-bold text-[#e6e6e6] truncate">${esc(c.name)}</span>
        </div>
        <span class="badge shrink-0">${esc(c.state)}</span>
      </div>
      <div class="text-[10px] text-[#6a6a6a] truncate mb-3 uppercase tracking-wider">${esc(c.image)}</div>
      ${running ? `${barRow("cpu", c.cpu.toFixed(1) + "%", c.cpu, heat(c.cpu))}
        <div class="mt-2">${barRow("mem", fmtBytes(c.memUsage) + "/" + fmtBytes(c.memLimit), c.memPct, heat(c.memPct))}</div>`
        : `<div class="text-[11px] text-[#6a6a6a]">${esc(c.status)}</div>`}
    </div>`;
  }).join("");
}

function renderSystem(s) {
  const h = s.host;
  const rows = [
    ["hostname", h.hostname], ["os", `${h.platform} ${h.platformVersion}`],
    ["kernel", h.kernel], ["arch", h.arch],
    ["virtualization", h.virtualization || "none"], ["cpu model", h.cpuModel || "—"],
    ["cpu cores", `${h.cpuPhysical} phys / ${h.cpuLogical} logi`],
    ["base clock", h.cpuMHz ? (h.cpuMHz / 1000).toFixed(2) + " GHz" : "—"],
    ["boot time", h.bootTime ? new Date(h.bootTime * 1000).toLocaleString() : "—"],
    ["uptime", fmtDuration(h.uptime)], ["processes", String(h.procCount)],
    ["total memory", fmtBytes(s.memory.total)],
  ];
  $("sysGrid").innerHTML =
    `<div class="panel"><div class="panel-head"><h2 class="panel-title">host</h2></div><dl>${rows.slice(0, 6).map(kv).join("")}</dl></div>
     <div class="panel"><div class="panel-head"><h2 class="panel-title">hardware.runtime</h2></div><dl>${rows.slice(6).map(kv).join("")}</dl></div>`;
  $("sensorGrid").innerHTML = (s.sensors || []).length
    ? s.sensors.map((t) =>
      `<div class="stat !p-2.5"><div class="text-[10px] text-[#6a6a6a] truncate uppercase tracking-wider">${esc(t.key)}</div>
        <div class="text-xl font-bold tabnum" style="color:${heat(clamp(t.temp,0,100))}">${t.temp.toFixed(0)}°C</div></div>`).join("")
    : `<div class="col-span-full text-[#6a6a6a] text-[12px] py-4 text-center">no temperature sensors exposed on this host.</div>`;
}

// -------------------------------------------------------------- fragments
function kpi(label, value, sub, pct) {
  return `<div class="stat">
    <div class="flex items-center justify-between">
      <span class="text-[10px] text-[#7d7d7d] uppercase tracking-[0.16em]">${label}</span>
      <span class="w-1.5 h-1.5" style="background:${heat(pct)}"></span>
    </div>
    <div class="text-2xl font-bold text-[#f2f2f2] tabnum leading-none">${value}</div>
    <div class="text-[10px] text-[#6a6a6a] truncate">${sub}</div>
    <div class="h-1 bg-[rgba(255,255,255,0.08)] mt-auto"><div class="h-full" style="width:${clamp(pct,0,100)}%;background:${heat(pct)};transition:width .4s"></div></div>
  </div>`;
}
function memBars(s) {
  const m = s.memory;
  return barRow("used", fmtBytes(m.used), m.usedPercent, heat(m.usedPercent)) +
    barRow("cached", fmtBytes(m.cached), m.total ? m.cached / m.total * 100 : 0, VIOLET) +
    barRow("available", fmtBytes(m.available), m.total ? m.available / m.total * 100 : 0, ACC);
}
function miniProcs(procs) {
  return `<div class="space-y-1 mt-1">` + procs.map((p) =>
    `<div class="flex items-center gap-3 text-[12px]">
      <span class="text-[#6a6a6a] tabnum w-12 shrink-0">${p.pid}</span>
      <span class="text-[#d0d0d0] truncate flex-1 min-w-0">${esc(p.name)}</span>
      <span class="tabnum w-14 text-right" style="color:${heat(p.cpu)}">${p.cpu.toFixed(1)}%</span>
      <div class="w-20 h-1.5 bg-[rgba(255,255,255,0.08)] shrink-0"><div class="h-full" style="width:${clamp(p.cpu,0,100)}%;background:${heat(p.cpu)}"></div></div>
    </div>`).join("") + `</div>`;
}
function miniStat(label, value) {
  return `<div class="border border-[rgba(255,255,255,0.1)] bg-[rgba(255,255,255,0.03)] py-1.5 px-2"><div class="text-[9px] text-[#6a6a6a] uppercase tracking-wider">${label}</div><div class="text-[12px] text-[#d0d0d0] tabnum mt-0.5">${value}</div></div>`;
}
function legendDot(color, label, value) {
  return `<div class="flex items-center gap-2"><span class="w-2.5 h-2.5" style="background:${color}"></span><span class="text-[#7d7d7d] uppercase tracking-wider">${label}</span><span class="text-[#b0b0b0] tabnum ml-1">${value}</span></div>`;
}
function kv([k, v]) {
  return `<div class="flex items-center justify-between py-2 text-[12px] border-b border-[rgba(255,255,255,0.07)]"><dt class="text-[#6a6a6a] uppercase tracking-wider text-[10px]">${k}</dt><dd class="text-[#d0d0d0] text-right truncate ml-4">${esc(String(v))}</dd></div>`;
}
function emptyPanel(msg) { return `<div class="panel col-span-full text-center text-[#6a6a6a] py-10">${msg}</div>`; }

// ------------------------------------------------------------------ helpers
function esc(s) { return String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c])); }
function sum(arr, key) { return (arr || []).reduce((a, x) => a + (x[key] || 0), 0); }
function rootDisk(disks) {
  if (!disks || !disks.length) return null;
  return disks.find((d) => d.mount === "/") || disks.find((d) => d.mount === "C:\\") || disks[0];
}

// ------------------------------------------------------------------ clock
setInterval(() => {
  $("clock").textContent = new Date().toLocaleTimeString([], { hour12: false });
}, 1000);

// --------------------------------------------------------------- SSE wire
function setStatus(state) {
  const dot = $("statusDot"), txt = $("statusText");
  if (!dot || !txt) return; // status chip removed from the header
  if (state === "live") { dot.style.background = "#eaeaea"; txt.textContent = "live"; txt.style.color = "#eaeaea"; }
  else if (state === "connecting") { dot.style.background = "#8a8a8a"; txt.textContent = "connecting"; txt.style.color = "#9a9a9a"; }
  else { dot.style.background = "#5a5a5a"; txt.textContent = "reconnecting"; txt.style.color = "#7a7a7a"; }
}

let es;
function connect() {
  setStatus("connecting");
  es = new EventSource("/api/stream");
  es.onmessage = (e) => {
    try { last = JSON.parse(e.data); setStatus("live"); render(last); } catch (_) {}
  };
  es.onerror = () => { setStatus("down"); es.close(); setTimeout(connect, 2000); };
}

// ------------------------------------------------------------------ boot
function boot() {
  buildNav();
  memGauge = new Meter($("memGauge"), { segments: 34, label: "in use" });
  memGauge2 = new Meter($("memGauge2"), { segments: 40, label: "in use" });
  swapGauge = new Meter($("swapGauge"), { segments: 40, label: "in use" });

  setupProcSort();
  $("menuBtn")?.addEventListener("click", () => document.querySelector("aside").classList.toggle("hidden"));
  $("intervalSel").addEventListener("change", (e) => {
    fetch("/api/interval?ms=" + encodeURIComponent(e.target.value), { method: "POST" }).catch(() => {});
  });
  fetch("/api/health").then((r) => r.json()).then((h) => {
    $("ver").textContent = h.version || "1.0";
    if (h.interval) $("intervalSel").value = String(h.interval);
  }).catch(() => {});
  connect();
}
document.addEventListener("DOMContentLoaded", boot);
