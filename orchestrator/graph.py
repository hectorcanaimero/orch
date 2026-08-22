"""Self-contained HTML/SVG plan graph — Sprint C `orch graph`.

We render the full DAG as inline SVG inside a single HTML file so the
output has ZERO external dependencies: no CDN, no CSS include, no
JavaScript. Open the file in Chrome/Safari and it works offline.

Layout in one paragraph:
    - Phase swimlanes flow left → right. Each phase is a column, x-position
      derived from phase index * (NODE_W + PHASE_GAP_X).
    - Inside a phase, tasks stack top → bottom sorted by task_id (stable).
    - Nodes are 140×44 rounded rectangles with status color + backend badge.
    - Dependency edges are cubic Béziers with an arrowhead marker.
    - A small legend in the top-right maps status → color.

Sizing ceiling (documented in help + README): ~500 tasks. Beyond that,
recommend `--only` — SVG stops being usable and layout starts overlapping.

We deliberately do NOT do edge-overlap avoidance in Sprint C. If two edges
cross, they cross — it's readable enough for planning-level DAGs.
"""

from __future__ import annotations

from html import escape
from typing import Any


# ---- Layout constants ---------------------------------------------------

NODE_W = 140
NODE_H = 44
PHASE_GAP_X = 60      # horizontal gap between phase columns
ROW_GAP_Y = 12        # vertical gap between rows in the same phase
MARGIN = 40           # canvas margin on all sides
LEGEND_W = 190
LEGEND_ROW_H = 22

STATUS_COLORS: dict[str, str] = {
    "done": "#22c55e",
    "in-progress": "#3b82f6",
    "todo": "#475569",
    "backlog": "#334155",
    "blocked": "#ef4444",
    "blocked-by-budget": "#f59e0b",
}

DEFAULT_STATUS_COLOR = "#64748b"


# ---- Public entry point -------------------------------------------------


def build_html(snapshot: dict[str, Any], *, title: str | None = None) -> str:
    """Render the whole HTML page as a single string.

    `snapshot` is the dict returned by
    `orchestrator.observability.build_status_snapshot`. We derive both the
    node metadata and the phase columns from `snapshot["tasks"]`.
    """
    task_rows: list[dict[str, Any]] = list(snapshot.get("tasks") or [])
    layout, canvas_w, canvas_h = _layout(task_rows)
    project = snapshot.get("project") or {}
    resolved_title = title or (
        f"orch graph · {project.get('project_id', 'project')}"
    )
    svg_defs = _svg_defs()
    svg_edges = _svg_edges(task_rows, layout)
    svg_nodes = _svg_nodes(task_rows, layout)
    svg_legend = _svg_legend(canvas_w)
    header_h = 32  # simple header strip at the top of the SVG for the title
    total_h = canvas_h + header_h + MARGIN
    total_w = canvas_w
    svg = (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {total_w} {total_h}" '
        f'font-family="Menlo, Consolas, monospace" font-size="12">'
        f'{svg_defs}'
        f'<text x="{MARGIN}" y="24" font-size="16" fill="#0f172a" '
        f'font-weight="bold">{escape(resolved_title)}</text>'
        f'<g transform="translate(0,{header_h})">'
        f'{svg_edges}{svg_nodes}{svg_legend}'
        f'</g>'
        f'</svg>'
    )
    style = (
        "body{margin:0;padding:24px;background:#f8fafc;color:#0f172a;"
        "font-family:system-ui,sans-serif;}"
        "h1{font-size:18px;margin:0 0 12px 0;}"
        ".meta{color:#475569;font-size:12px;margin-bottom:20px;}"
    )
    total_n = (snapshot.get("totals") or {}).get("_total", len(task_rows))
    backend_kind = project.get("backend", "?")
    meta = (
        f'Project {escape(project.get("project_id","?"))} · backend={escape(backend_kind)} '
        f'· {total_n} tasks total · {len(task_rows)} shown'
    )
    html = (
        "<!doctype html><html><head><meta charset='utf-8'>"
        f"<title>{escape(resolved_title)}</title>"
        f"<style>{style}</style></head><body>"
        f"<h1>{escape(resolved_title)}</h1>"
        f"<div class='meta'>{meta}</div>"
        f"{svg}"
        "</body></html>"
    )
    return html


# ---- Layout -------------------------------------------------------------


def _layout(
    task_rows: list[dict[str, Any]],
) -> tuple[dict[str, tuple[int, int]], int, int]:
    """Return `{task_id: (x, y)}` plus the canvas width/height.

    Phase columns × id-sorted rows within each phase.
    """
    # Group by phase.
    by_phase: dict[int, list[dict[str, Any]]] = {}
    for row in task_rows:
        phase = int(row.get("phase", 0) or 0)
        by_phase.setdefault(phase, []).append(row)
    for phase, rows in by_phase.items():
        rows.sort(key=lambda r: str(r.get("id", "")))

    positions: dict[str, tuple[int, int]] = {}
    ordered_phases = sorted(by_phase.keys())
    x_cursor = MARGIN
    tallest_column_h = 0
    for phase in ordered_phases:
        y_cursor = MARGIN
        for row in by_phase[phase]:
            positions[str(row["id"])] = (x_cursor, y_cursor)
            y_cursor += NODE_H + ROW_GAP_Y
        col_h = y_cursor - MARGIN
        if col_h > tallest_column_h:
            tallest_column_h = col_h
        x_cursor += NODE_W + PHASE_GAP_X

    canvas_w = max(x_cursor + LEGEND_W, MARGIN * 2 + NODE_W)
    canvas_h = tallest_column_h + MARGIN
    return positions, canvas_w, canvas_h


# ---- SVG builders -------------------------------------------------------


def _svg_defs() -> str:
    """Global <defs> block — arrowhead marker for edge tails."""
    return (
        '<defs>'
        '<marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" '
        'markerWidth="6" markerHeight="6" orient="auto-start-reverse">'
        '<path d="M0,0 L10,5 L0,10 z" fill="#64748b"/>'
        '</marker>'
        '</defs>'
    )


def _svg_nodes(
    task_rows: list[dict[str, Any]],
    layout: dict[str, tuple[int, int]],
) -> str:
    """Render each task as a rounded rect + text labels."""
    parts: list[str] = []
    for row in task_rows:
        tid = str(row["id"])
        pos = layout.get(tid)
        if pos is None:
            continue
        x, y = pos
        status = str(row.get("status") or "todo")
        color = STATUS_COLORS.get(status, DEFAULT_STATUS_COLOR)
        backend = str(row.get("backend") or "?")
        cli_model = str(row.get("cli_model") or "?")
        badge = f"{backend}/{cli_model}"
        # Tooltip carries the full detail (title + cost + last event).
        tooltip_parts = [
            f"id: {tid}",
            f"phase: {row.get('phase', '?')}",
            f"status: {status}",
            f"backend: {badge}",
            f"cost: ${float(row.get('cost_usd') or 0):.4f}",
        ]
        if row.get("last_event_human"):
            tooltip_parts.append(f"last: {row['last_event_human']}")
        tooltip = "\n".join(tooltip_parts)
        parts.append(
            f'<g class="task" data-id="{escape(tid)}">'
            f'<title>{escape(tooltip)}</title>'
            f'<rect x="{x}" y="{y}" width="{NODE_W}" height="{NODE_H}" rx="6" ry="6" '
            f'fill="{color}" stroke="#0f172a" stroke-width="1"/>'
            f'<text x="{x + 10}" y="{y + 18}" fill="white" font-weight="bold">'
            f'{escape(tid)}</text>'
            f'<text x="{x + 10}" y="{y + 34}" fill="white" font-size="10">'
            f'{escape(badge[:24])}</text>'
            f'</g>'
        )
    return "".join(parts)


def _svg_edges(
    task_rows: list[dict[str, Any]],
    layout: dict[str, tuple[int, int]],
) -> str:
    """Render one cubic Bézier per dependency edge."""
    parts: list[str] = []
    for row in task_rows:
        tid = str(row["id"])
        tgt_pos = layout.get(tid)
        if tgt_pos is None:
            continue
        for dep_id in row.get("dependencies", []) or []:
            src_pos = layout.get(str(dep_id))
            if src_pos is None:
                continue
            x1 = src_pos[0] + NODE_W
            y1 = src_pos[1] + NODE_H // 2
            x2 = tgt_pos[0]
            y2 = tgt_pos[1] + NODE_H // 2
            dx = (x2 - x1) / 2 if x2 > x1 else 30
            cx1 = x1 + dx
            cx2 = x2 - dx
            path = f"M{x1},{y1} C{cx1},{y1} {cx2},{y2} {x2},{y2}"
            parts.append(
                f'<path d="{path}" fill="none" stroke="#64748b" '
                f'stroke-width="1.2" marker-end="url(#arrow)" opacity="0.7"/>'
            )
    return "".join(parts)


def _svg_legend(canvas_w: int) -> str:
    """Static legend that maps status → color."""
    x0 = canvas_w - LEGEND_W
    y0 = MARGIN
    parts = [
        f'<g class="legend" transform="translate({x0},{y0})">',
        '<rect x="0" y="0" width="180" height="'
        f'{LEGEND_ROW_H * (len(STATUS_COLORS) + 1) + 10}" '
        'rx="6" ry="6" fill="white" stroke="#cbd5e1"/>',
        '<text x="10" y="18" font-weight="bold" fill="#0f172a">Status</text>',
    ]
    for i, (status, color) in enumerate(STATUS_COLORS.items()):
        row_y = 30 + i * LEGEND_ROW_H
        parts.append(
            f'<rect x="10" y="{row_y}" width="14" height="14" rx="3" ry="3" '
            f'fill="{color}"/>'
            f'<text x="32" y="{row_y + 12}" fill="#0f172a" font-size="11">'
            f'{escape(status)}</text>'
        )
    parts.append('</g>')
    return "".join(parts)
