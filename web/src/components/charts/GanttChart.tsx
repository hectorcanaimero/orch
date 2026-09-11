import { forwardRef } from "react"

import type { Milestone } from "@/hooks/useMilestones"

export interface GanttChartProps {
  milestones: Milestone[]
  /** ISO date (YYYY-MM-DD). Injected so the render is deterministic/testable. */
  today: string
}

// SVG geometry — fixed viewBox, outer <svg> stretches to container width.
// Left gutter holds milestone labels; right padding leaves room for the % and
// ETA-date labels that hang off the end of each bar.
const VB_WIDTH = 900
const ROW_H = 34
const BAR_H = 16
const PAD_LEFT = 170
const PAD_RIGHT = 64
const PAD_TOP = 28
const AXIS_H = 18

/** ISO string → epoch ms, or NaN on parse failure. */
function ms(iso: string | null): number {
  if (!iso) return NaN
  return Date.parse(iso)
}

/** `YYYY-MM-DD...` → `MM-DD`; falls back to the raw slice. */
function monthDay(iso: string): string {
  return iso.length >= 10 ? iso.slice(5, 10) : iso
}

/**
 * Hand-rolled SVG Gantt for milestones (Sprint G-3). No charting lib — keeps
 * the SPA bundle lean, mirroring BarChartByDay. One horizontal bar per
 * milestone: track = created_at → target_date (or ETA when no target),
 * overlay = progress %, colored tick = projected ETA (green high / amber low).
 * `today` is a vertical rule. Milestones with no ETA simply omit the tick.
 */
export const GanttChart = forwardRef<SVGSVGElement, GanttChartProps>(
  function GanttChart({ milestones, today }, ref) {
    if (milestones.length === 0) return null

    const todayMs = ms(today)

    // Domain spans every date we plot so nothing clips off the edges.
    const points: number[] = [todayMs]
    for (const m of milestones) {
      for (const v of [ms(m.created_at), ms(m.target_date), ms(m.eta?.eta_date ?? null)]) {
        if (!Number.isNaN(v)) points.push(v)
      }
    }
    const domainMin = Math.min(...points)
    const domainMax = Math.max(...points)
    const span = domainMax - domainMin || 1
    const plotW = VB_WIDTH - PAD_LEFT - PAD_RIGHT

    const xFor = (value: number): number => {
      if (Number.isNaN(value)) return PAD_LEFT
      const clamped = Math.min(Math.max(value, domainMin), domainMax)
      return PAD_LEFT + ((clamped - domainMin) / span) * plotW
    }

    const height = PAD_TOP + milestones.length * ROW_H + AXIS_H
    const todayX = xFor(todayMs)

    return (
      <svg
        ref={ref}
        viewBox={`0 0 ${VB_WIDTH} ${height}`}
        width="100%"
        role="img"
        aria-label="Milestone timeline"
      >
        {/* today rule */}
        <line
          x1={todayX}
          x2={todayX}
          y1={PAD_TOP - 8}
          y2={PAD_TOP + milestones.length * ROW_H}
          className="stroke-sky-400 dark:stroke-sky-500"
          strokeWidth={1}
          strokeDasharray="3 3"
        />
        <text
          x={todayX}
          y={PAD_TOP - 12}
          textAnchor="middle"
          className="fill-sky-500 text-[9px]"
        >
          today
        </text>

        {milestones.map((m, i) => {
          const rowY = PAD_TOP + i * ROW_H
          const barY = rowY + (ROW_H - BAR_H) / 2
          const endIso = m.target_date ?? m.eta?.eta_date ?? today
          const x0 = xFor(ms(m.created_at))
          const x1 = Math.max(xFor(ms(endIso)), x0 + 2)
          const barW = x1 - x0
          const fillW = Math.max((barW * m.progress.pct) / 100, 0)
          const label =
            m.title.length > 24 ? `${m.title.slice(0, 23)}…` : m.title
          return (
            <g key={m.id}>
              <text
                x={PAD_LEFT - 10}
                y={barY + BAR_H - 4}
                textAnchor="end"
                className="fill-zinc-700 dark:fill-zinc-300 text-[11px]"
              >
                {label}
              </text>
              {/* track */}
              <rect
                x={x0}
                y={barY}
                width={barW}
                height={BAR_H}
                rx={3}
                className="fill-zinc-200 dark:fill-zinc-800"
              />
              {/* progress overlay */}
              <rect
                x={x0}
                y={barY}
                width={fillW}
                height={BAR_H}
                rx={3}
                className="fill-zinc-500 dark:fill-zinc-400"
              />
              <text
                x={x1 + 6}
                y={barY + BAR_H - 4}
                className="fill-zinc-500 text-[9px]"
              >
                {m.progress.pct}%
              </text>
              {/* ETA tick — omitted entirely when there's no projection */}
              {m.eta && (
                <>
                  <circle
                    cx={xFor(ms(m.eta.eta_date))}
                    cy={barY + BAR_H / 2}
                    r={4}
                    className={
                      m.eta.confidence === "high"
                        ? "fill-emerald-500"
                        : "fill-amber-500"
                    }
                  />
                  <text
                    x={xFor(ms(m.eta.eta_date))}
                    y={barY - 3}
                    textAnchor="middle"
                    className={
                      m.eta.confidence === "high"
                        ? "fill-emerald-500 text-[8px]"
                        : "fill-amber-500 text-[8px]"
                    }
                  >
                    {monthDay(m.eta.eta_date)}
                  </text>
                </>
              )}
            </g>
          )
        })}

        {/* domain endpoints */}
        <text
          x={PAD_LEFT}
          y={height - 4}
          textAnchor="start"
          className="fill-zinc-400 text-[9px]"
        >
          {monthDay(new Date(domainMin).toISOString())}
        </text>
        <text
          x={VB_WIDTH - PAD_RIGHT}
          y={height - 4}
          textAnchor="end"
          className="fill-zinc-400 text-[9px]"
        >
          {monthDay(new Date(domainMax).toISOString())}
        </text>
      </svg>
    )
  },
)
