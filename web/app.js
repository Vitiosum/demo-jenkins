// The Assembly Line: draws the factory floor from /api/floor.
// Everything shown comes from the Go server, which reads Jenkins with the
// token server side. This file never sees a credential.
"use strict";

const $ = (sel, el = document) => el.querySelector(sel);
const STATION_X = [150, 450, 750, 1050]; // machine centres in the drawing
const CRATE_Y = 194;
const STOCK_X = 900; // end of the belt, in front of the closed dock
const WORDS = { done: "Passed", running: "Working", failed: "Failed", skipped: "Skipped", stopped: "Stopped", waiting: "Waiting", idle: "Idle" };

const hall = $("#hall");
const crate = $("#crate");
let view = JSON.parse($("#floor-data").textContent);
let shownBuild = null;
let sampleTimer = null;

// Tiny DOM helper: h("li", {class: "x"}, "text", child)
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("data-")) el.setAttribute(k, v);
    else el[k] = v;
  }
  for (const c of children.flat()) if (c !== null && c !== undefined && c !== "") el.append(c);
  return el;
}

function ago(iso) {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return `${Math.round(s / 3600)} h ago`;
}

function clock(iso) {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// ---- The big line ----

function drawLine(build, { label, sample = false } = {}) {
  hall.classList.toggle("is-sample", sample);
  const stations = build ? build.stations : view.stations.map((s) => ({ key: s.key, status: "idle", duration: "" }));
  const running = build && build.outcome === "running";

  stations.forEach((st, i) => {
    $(`#st-${st.key}`).dataset.status = st.status;
    const lamp = $(`.lamp[data-station="${st.key}"]`);
    lamp.classList.toggle("is-on", ["done", "running", "failed"].includes(st.status));
    lamp.classList.toggle("is-hot", st.status === "running");
    lamp.classList.toggle("is-fail", st.status === "failed");
    const plate = $(`.plate[data-key="${st.key}"]`);
    plate.dataset.status = st.status;
    $(".plate__word", plate).textContent = WORDS[st.status] || st.status;
    $(".plate__time", plate).textContent = st.duration || "";
  });

  hall.classList.toggle("is-moving", Boolean(running));
  hall.classList.toggle("is-shipping", Boolean(build && build.position >= 4));

  if (!build) {
    placeCrate(0, true);
    $("#crate-label").textContent = "—";
    $("#crate-label").classList.remove("is-long");
    crate.classList.remove("is-rejected", "is-shipped");
    shownBuild = null;
    return;
  }
  const key = sample ? "sample" : build.number;
  const isNew = key !== shownBuild;
  if (isNew) {
    // A new build: the crate drops from the hopper at goods in.
    crate.classList.remove("is-shipped", "is-rejected");
    placeCrate(0, true);
    crate.classList.remove("is-dropping");
    void crate.getBBox();
    crate.classList.add("is-dropping");
    shownBuild = key;
  }
  const text = label || `#${build.number}`;
  $("#crate-label").textContent = text;
  $("#crate-label").classList.toggle("is-long", text.length > 4);
  requestAnimationFrame(() => {
    placeCrate(build.position, false, build.outcome === "passed");
    crate.classList.toggle("is-rejected", build.outcome === "rejected");
    crate.classList.toggle("is-shipped", build.position >= 4);
  });
}

// Crate x: a station, the stock spot before the closed dock (tests passed, not shipped), or through the door.
function placeCrate(position, instant, kept = false) {
  const x = position >= 4 ? STATION_X[3] - 32 : kept ? STOCK_X - 32 : STATION_X[Math.max(0, position)] - 32;
  crate.classList.toggle("is-instant", instant);
  crate.style.transform = `translate(${x}px, ${CRATE_Y}px)`;
  if (instant) void crate.getBBox();
}

// ---- Shift log ----

function drawCards(builds) {
  const list = $("#cards");
  const known = new Set([...list.children].map((li) => li.dataset.number));
  list.replaceChildren(
    ...builds.map((b) =>
      h("li", { class: `card${known.size && !known.has(String(b.number)) ? " is-new" : ""}`, "data-number": b.number },
        h("span", { class: "card__num" }, `#${b.number}`),
        h("ol", { class: "card__belt", "aria-label": `Stations of build ${b.number}` },
          b.stations.map((st, i) =>
            h("li", { class: "card__station", "data-status": st.status },
              h("b", {}, view.stations[i].name),
              h("span", {}, st.status === "skipped" ? "skipped" : st.duration || WORDS[st.status].toLowerCase())))),
        h("div", { class: "card__meta" },
          h("span", { class: "stamp", "data-outcome": b.outcome }, b.stamp),
          h("span", {}, b.duration ? `${clock(b.started)}, ran ${b.duration}` : clock(b.started),
            b.commit ? h("br") : null, b.commit ? h("code", {}, b.commit) : null,
            b.cause ? ` ${b.cause}` : "")))));
}

// ---- Commissioning checklist ----

function drawChecks(items) {
  $("#checks").replaceChildren(
    ...items.map((c) =>
      h("li", { class: "check", "data-status": c.status },
        h("span", { class: "check__box", "aria-label": c.status }),
        h("div", {},
          h("p", { class: "check__title" }, c.title, c.owner && c.status !== "done" ? h("span", { class: "check__owner", title: "Needs the owner's Clever Cloud account" }, "Owner") : null),
          h("p", { class: "check__hint" }, c.hint)))));
}

// ---- Behind the wall ----

function drawCalls(calls) {
  const list = $("#calls");
  if (!calls.length) {
    list.replaceChildren(h("li", { class: "call" }, h("span", { class: "call__code", "data-kind": "warn" }, "—"), h("span", { class: "call__path" }, "No Jenkins to call yet."), h("span")));
    return;
  }
  list.replaceChildren(
    ...calls.map((c) => {
      const kind = c.status === 200 ? "ok" : c.status === 0 || c.status >= 400 ? (c.status === 404 ? "warn" : "bad") : "warn";
      const [path, query] = c.path.split("?");
      return h("li", { class: "call" },
        h("span", { class: "call__code", "data-kind": kind }, c.status ? String(c.status) : "—"),
        h("span", { class: "call__path" }, `GET ${path}`, query ? h("b", {}, `?${query}`) : null),
        h("span", { class: "call__ms" }, `${c.ms} ms`));
    }));
}

function drawRunners(runners) {
  $("#runners").replaceChildren(
    ...(runners.length
      ? runners.map((r) => h("li", { class: "runner", "data-online": r.online, "data-busy": r.busy }, h("i"), r.name, h("span", {}, r.busy ? "building" : r.online ? "idle" : "offline")))
      : [h("li", { class: "runner" }, h("i"), "No runner right now", h("span", {}, "started on demand"))]));
}

// ---- Whole page ----

function render(v) {
  view = v;
  hall.dataset.state = v.state;
  if (!sampleTimer) {
    hall.dataset.andon = v.andon;
    $("#andon-text").textContent = v.headline;
    drawLine(v.current);
  }
  $("#andon-next").textContent = v.nextStep;
  $("#andon-next").hidden = !v.nextStep;
  const hasBuilds = v.builds.length > 0;
  $("#shift-log").hidden = !hasBuilds;
  $("#commissioning").hidden = hasBuilds;
  $("#sample-btn").hidden = hasBuilds;
  if (hasBuilds) drawCards(v.builds);
  else drawChecks(v.checklist);
  drawCalls(v.calls);
  drawRunners(v.runners);
  if (v.version) $("#cabinet-sub").textContent = `Jenkins ${v.version}`;
  $("#unit-commit").textContent = v.unit.commit || "local";
  $("#unit-build").textContent = v.unit.byTheLine ? `build #${v.unit.build}` : "by hand";
  $("#unit-verdict").textContent = v.unit.verdict;
  tick();
}

function tick() {
  $("#andon-fresh").textContent = view.state === "unconfigured" ? "No Jenkins linked" : `Read from Jenkins ${ago(view.fetchedAt)}`;
}

async function poll() {
  if (document.hidden) return;
  try {
    const res = await fetch("/api/floor", { cache: "no-store" });
    if (res.ok) render(await res.json());
  } catch {
    /* keep the last reading on screen */
  }
}

// ---- Sample run: an illustration, clearly labelled, only while no real build exists ----

function playSample() {
  const btn = $("#sample-btn");
  btn.disabled = true;
  const keys = view.stations.map((s) => s.key);
  const durations = ["3 s", "41 s", "12 s", "1 min 10 s"];
  const steps = [];
  for (let i = 0; i <= 4; i++) {
    steps.push({
      number: 0,
      outcome: i < 4 ? "running" : "shipped",
      position: i,
      stations: keys.map((key, j) => ({ key, status: j < i ? "done" : j === i ? "running" : "waiting", duration: j < i ? durations[j] : "" })),
    });
  }
  hall.dataset.andon = "amber";
  $("#andon-text").textContent = "Sample run: an illustration, not a Jenkins build.";
  let n = 0;
  const next = () => {
    drawLine(steps[n], { label: "Sample", sample: true });
    n++;
    if (n < steps.length) {
      sampleTimer = setTimeout(next, n === 4 ? 2200 : 1900);
    } else {
      hall.dataset.andon = "green";
      $("#andon-text").textContent = "Sample shipped. Real builds will look exactly like this.";
      sampleTimer = setTimeout(() => {
        sampleTimer = null;
        btn.disabled = false;
        hall.classList.remove("is-sample");
        shownBuild = null;
        render(view);
      }, 3200);
    }
  };
  next();
}

$("#sample-btn").addEventListener("click", playSample);
render(view);
setInterval(poll, 5000);
setInterval(tick, 1000);
document.addEventListener("visibilitychange", poll);
