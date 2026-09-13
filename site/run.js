// The hero: the six tasks of orch's nextjs-saas template, scheduled the way
// `orch run` schedules them — a task starts on the first free agent once every
// task it depends on is done. Durations are the template's estimateHours.
(function () {
  "use strict";

  var TASKS = [
    { id: "F0.T1", title: "App scaffold", hours: 0.5, deps: [] },
    { id: "F1.T1", title: "Auth middleware", hours: 0.6, deps: ["F0.T1"] },
    { id: "F1.T2", title: "Protected dashboard", hours: 0.5, deps: ["F1.T1"] },
    { id: "F2.T1", title: "Database schema", hours: 0.8, deps: ["F0.T1"] },
    { id: "F2.T2", title: "User sync webhook", hours: 1.0, deps: ["F1.T1", "F2.T1"] },
    { id: "F3.T1", title: "Deploy previews", hours: 0.6, deps: ["F0.T1", "F2.T1"] }
  ];
  var AGENTS = ["claude", "codex", "opencode"];
  var SECONDS = 7;

  var lanesEl = document.getElementById("lanes");
  var fillEl = document.getElementById("client-fill");
  var countEl = document.getElementById("client-count");
  var replayEl = document.getElementById("replay");
  if (!lanesEl) return;

  // ---- schedule (pure) ----------------------------------------------------
  function schedule() {
    var done = {}, placed = [], free = AGENTS.map(function () { return 0; });
    var pending = TASKS.slice();
    var now = 0;
    while (pending.length) {
      var ready = pending.filter(function (t) {
        return t.deps.every(function (d) { return done[d] !== undefined && done[d] <= now; });
      });
      var started = false;
      ready.forEach(function (t) {
        var lane = free.findIndex(function (f) { return f <= now; });
        if (lane === -1) return;
        var entry = { task: t, lane: lane, start: now, end: now + t.hours };
        placed.push(entry);
        free[lane] = entry.end;
        done[t.id] = entry.end;
        pending.splice(pending.indexOf(t), 1);
        started = true;
      });
      if (!started || ready.length === 0) {
        // Advance to the next moment something finishes.
        var next = Infinity;
        placed.forEach(function (p) { if (p.end > now && p.end < next) next = p.end; });
        if (next === Infinity) break; // unreachable with an acyclic graph
        now = next;
      }
    }
    return placed;
  }

  var plan = schedule();
  var total = plan.reduce(function (m, p) { return Math.max(m, p.end); }, 0);

  // ---- render -------------------------------------------------------------
  var nodes = [];
  AGENTS.forEach(function (name, i) {
    var lane = document.createElement("div");
    lane.className = "lane";
    lane.innerHTML = '<span class="lane-name">' + name + '</span><div class="track"></div>';
    var track = lane.querySelector(".track");
    plan.filter(function (p) { return p.lane === i; }).forEach(function (p) {
      var el = document.createElement("div");
      el.className = "task";
      el.style.left = (p.start / total * 100) + "%";
      el.style.width = "calc(" + (p.task.hours / total * 100) + "% - 6px)";
      el.innerHTML = '<i class="fill"></i><span class="label"><b>' + p.task.id + "</b><span>" + p.task.title + "</span></span>";
      track.appendChild(el);
      nodes.push({ p: p, el: el, fill: el.querySelector(".fill") });
    });
    lanesEl.appendChild(lane);
  });

  function draw(hours) {
    var finished = 0;
    nodes.forEach(function (n) {
      var progress = Math.min(1, Math.max(0, (hours - n.p.start) / n.p.task.hours));
      // 2.3 - 1.3 is 0.9999999999999998 in floating point: without this the
      // last task never reads as done and the page ends at "5 of 6".
      if (progress > 1 - 1e-9) progress = 1;
      n.fill.style.width = (progress * 100) + "%";
      n.el.classList.toggle("running", progress > 0 && progress < 1);
      n.el.classList.toggle("done", progress >= 1);
      if (progress >= 1) finished++;
    });
    fillEl.style.width = (finished / TASKS.length * 100) + "%";
    countEl.textContent = finished + " of " + TASKS.length + " done";
  }

  // ---- play once; replay on request -----------------------------------------
  var reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function play() {
    replayEl.hidden = true;
    var t0 = null;
    function frame(ts) {
      if (t0 === null) t0 = ts;
      var elapsed = (ts - t0) / 1000;
      draw(Math.min(total, elapsed / SECONDS * total));
      if (elapsed < SECONDS) {
        requestAnimationFrame(frame);
      } else {
        replayEl.hidden = false;
      }
    }
    requestAnimationFrame(frame);
  }

  replayEl.addEventListener("click", play);

  if (reduced) {
    draw(total);
    replayEl.hidden = false;
  } else {
    draw(0);
    setTimeout(play, 600);
  }
})();
