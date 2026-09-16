// The playground: loads orch's parser and graph checks compiled to
// WebAssembly (cmd/orch-wasm), then renders what orchBuild(spec) returns.
// Nothing leaves the browser.
(function () {
  "use strict";

  var spec = document.getElementById("spec");
  var build = document.getElementById("build");
  var out = document.getElementById("out");

  function el(tag, attrs, text) {
    var node = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) { node.setAttribute(k, attrs[k]); });
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function code(text) { return el("code", null, text); }

  function hours(h) { return (Math.round(h * 10) / 10) + " h"; }

  function table(head, rows) {
    var wrap = el("div", { class: "table-wrap", tabindex: "0" });
    var t = el("table", { class: "tasks" });
    var tr = el("tr");
    function cls(h) { return h.num ? "num" : (h.cls || ""); }
    head.forEach(function (h) { tr.appendChild(el("th", { scope: "col", class: cls(h) }, h.label)); });
    t.appendChild(el("thead")).appendChild(tr);
    var body = t.appendChild(el("tbody"));
    rows.forEach(function (cells) {
      var row = body.appendChild(el("tr"));
      cells.forEach(function (c, i) {
        var td = row.appendChild(el("td", { class: cls(head[i]) }));
        if (c instanceof Node) td.appendChild(c); else td.textContent = c;
      });
    });
    wrap.appendChild(t);
    return wrap;
  }

  function render(res) {
    out.textContent = "";
    var errors = res.problems.filter(function (p) { return p.severity === "error"; }).length;
    out.appendChild(el("p", { class: "pg-summary " + (errors ? "is-bad" : "is-ok") }, "orch validate · " + res.summary));

    var phases = {};
    res.tasks.forEach(function (t) { phases[t.phase] = true; });
    var facts = out.appendChild(el("dl", { class: "facts" }));
    [["Tasks", String(res.tasks.length)], ["Phases", String(Object.keys(phases).length)], ["Estimated", hours(res.total_hours)]]
      .forEach(function (f) {
        var d = facts.appendChild(el("div"));
        d.appendChild(el("dt", null, f[0]));
        d.appendChild(el("dd", null, f[1]));
      });

    if (res.problems.length) {
      out.appendChild(el("h2", null, "Problems"));
      out.appendChild(table(
        [{ label: "SEVERITY" }, { label: "KIND" }, { label: "TASK" }, { label: "FIELD" }, { label: "MESSAGE" }],
        res.problems.map(function (p) { return [p.severity, code(p.kind), p.task_id || "—", p.field, p.message]; })
      ));
    }

    if (res.warnings.length) {
      out.appendChild(el("h2", null, "Warnings: " + res.warnings.length));
      var ul = out.appendChild(el("ul", { class: "pg-list" }));
      res.warnings.forEach(function (w) { ul.appendChild(el("li", null, w)); });
    }

    if (!res.tasks.length) {
      out.appendChild(el("p", { class: "pg-empty" }, "No tasks found. A task is a ### heading like “F0.1.T1 — Title” under a # phase and a ## package, with a **Model** line."));
      return;
    }

    out.appendChild(el("h2", null, "The plan"));
    if (res.svg) {
      if (res.critical_path.length && res.critical_path.length < res.tasks.length) {
        var path = out.appendChild(el("p", { class: "pg-path" }, "Critical path: "));
        path.appendChild(code(res.critical_path.join(" → ")));
      }
      var wrap = out.appendChild(el("div", { class: "dag-wrap", tabindex: "0", role: "group", "aria-label": "Dependency graph, scrollable" }));
      wrap.innerHTML = res.svg; // built by DAGSVG, which escapes every text it writes
    } else {
      out.appendChild(el("p", { class: "pg-empty" }, "The graph has a cycle, so it cannot be drawn. Fix the dep.cycle problem above."));
    }

    out.appendChild(el("h2", null, "Tasks"));
    out.appendChild(table(
      [{ label: "ID" }, { label: "PHASE", num: true }, { label: "TITLE", cls: "title" }, { label: "MODEL" }, { label: "EST", num: true }, { label: "DEPS" }],
      res.tasks.map(function (t) {
        return [code(t.id), String(t.phase), t.title, t.model ? code(t.model) : "—", hours(t.estimate_hours),
          t.dependencies.length ? code(t.dependencies.join(", ")) : "—"];
      })
    ));
  }

  function run() {
    var res = JSON.parse(window.orchBuild(spec.value));
    if (res.error) {
      out.textContent = res.error;
      return;
    }
    render(res);
  }

  build.addEventListener("click", run);
  spec.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter" && !build.disabled) run();
  });

  window.addEventListener("orch-ready", function () {
    build.disabled = false;
    build.textContent = "Build DAG";
    run();
  });

  if (!("WebAssembly" in window) || typeof Go === "undefined") {
    build.textContent = "Needs WebAssembly";
    return;
  }
  var go = new Go();
  WebAssembly.instantiateStreaming(fetch("orch.wasm"), go.importObject).then(function (r) {
    go.run(r.instance);
  }, function () {
    build.textContent = "Engine failed to load";
    out.textContent = "The engine (orch.wasm) could not be loaded. Reload the page, or run `orch atomize --list` locally.";
  });
})();
