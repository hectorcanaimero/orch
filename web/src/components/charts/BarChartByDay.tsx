import { useState } from "react"

export interface BarChartByDayDatum {
  date: string
  cost_usd: number
}

export interface BarChartByDayProps {
  data: BarChartByDayDatum[]
}

/**
 * Returns a "nice" ceiling ≥ `max`, rounded up to 1/2/5 × 10^n. Used to pick a
 * readable y-axis top when the raw max is an awkward number (e.g. `12.37` →
 * `20`, `0.041` → `0.05`). Falls back to 1 when max ≤ 0 so the axis still
 * renders sensibly on an all-zero series (defensive — parent already renders
 * an empty state).
 */
function niceCeil(max: number): number {
  if (!Number.isFinite(max) || max <= 0) return 1
  const exp = Math.floor(Math.log10(max))
  const pow = Math.pow(10, exp)
  const norm = max / pow // in [1, 10)
  let niceNorm: number
  if (norm <= 1) niceNorm = 1
  else if (norm <= 2) niceNorm = 2
  else if (norm <= 5) niceNorm = 5
  else niceNorm = 10
  return niceNorm * pow
}

/** `YYYY-MM-DD` → `MM-DD`. Falls back to the raw string on parse failure. */
function shortDate(iso: string): string {
  if (!iso || iso.length < 10) return iso
  return iso.slice(5, 10)
}

const USD = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

// SVG geometry — viewBox is fixed, the outer <svg> stretches to container
// width. Padding leaves room for y-axis labels (left) and rotated x-labels
// (bottom).
const VB_WIDTH = 560
const VB_HEIGHT = 240
const PAD_LEFT = 42
const PAD_RIGHT = 12
const PAD_TOP = 12
const PAD_BOTTOM = 42
const CHART_W = VB_WIDTH - PAD_LEFT - PAD_RIGHT
const CHART_H = VB_HEIGHT - PAD_TOP - PAD_BOTTOM

/**
 * Hand-rolled SVG vertical bar chart for cost-by-day (up to 14 days). No
 * third-party charting lib — we keep the SPA bundle lean. Y-axis auto-scales
 * via `niceCeil`, gridlines are subtle, and hovering a bar shows a floating
 * tooltip anchored to the mouse position.
 */
export function BarChartByDay({ data }: BarChartByDayProps) {
  const [hover, setHover] = useState<{
    index: number
    x: number
    y: number
  } | null>(null)

  if (data.length === 0) {
    return (
      <div className="flex h-60 items-center justify-center">
        <p className="text-sm text-muted-foreground">No spend recorded yet.</p>
      </div>
    )
  }

  const maxCost = Math.max(...data.map((d) => d.cost_usd), 0)
  const yMax = niceCeil(maxCost)
  const barSlot = CHART_W / data.length
  const barW = Math.max(barSlot * 0.7, 2)

  // 4 gridlines (25/50/75/100 % of yMax) + baseline.
  const gridSteps = [0, 0.25, 0.5, 0.75, 1] as const

  return (
    <div className="relative w-full">
      <svg
        viewBox={`0 0 ${VB_WIDTH} ${VB_HEIGHT}`}
        width="100%"
        height={240}
        role="img"
        aria-label="Cost by day, last 14 days"
        onMouseLeave={() => setHover(null)}
      >
        {/* Gridlines + y-axis labels */}
        {gridSteps.map((step) => {
          const y = PAD_TOP + CHART_H * (1 - step)
          const value = yMax * step
          return (
            <g key={step}>
              <line
                x1={PAD_LEFT}
                x2={PAD_LEFT + CHART_W}
                y1={y}
                y2={y}
                className="stroke-zinc-200 dark:stroke-zinc-800"
                strokeWidth={1}
              />
              <text
                x={PAD_LEFT - 6}
                y={y + 3}
                textAnchor="end"
                className="fill-zinc-500 text-[9px]"
              >
                {value >= 1
                  ? `$${value.toFixed(0)}`
                  : `$${value.toFixed(2)}`}
              </text>
            </g>
          )
        })}

        {/* Bars */}
        {data.map((d, i) => {
          const ratio = yMax > 0 ? d.cost_usd / yMax : 0
          const h = CHART_H * ratio
          const x = PAD_LEFT + barSlot * i + (barSlot - barW) / 2
          const y = PAD_TOP + CHART_H - h
          const isHover = hover?.index === i
          return (
            <g key={d.date}>
              {/* Invisible full-height hit target so slim/zero bars remain
                  hoverable. */}
              <rect
                x={PAD_LEFT + barSlot * i}
                y={PAD_TOP}
                width={barSlot}
                height={CHART_H}
                fill="transparent"
                onMouseMove={(e) => {
                  const rect = (
                    e.currentTarget.ownerSVGElement as SVGSVGElement
                  ).getBoundingClientRect()
                  setHover({
                    index: i,
                    x: e.clientX - rect.left,
                    y: e.clientY - rect.top,
                  })
                }}
              />
              <rect
                x={x}
                y={y}
                width={barW}
                height={Math.max(h, d.cost_usd > 0 ? 1 : 0)}
                className={
                  isHover
                    ? "fill-zinc-950 dark:fill-zinc-100"
                    : "fill-zinc-800 dark:fill-zinc-300"
                }
                pointerEvents="none"
              />
            </g>
          )
        })}

        {/* X-axis baseline */}
        <line
          x1={PAD_LEFT}
          x2={PAD_LEFT + CHART_W}
          y1={PAD_TOP + CHART_H}
          y2={PAD_TOP + CHART_H}
          className="stroke-zinc-300 dark:stroke-zinc-700"
          strokeWidth={1}
        />

        {/* X-axis labels (rotated 45°). To avoid clutter on 14 dense bars we
            skip every other label past 10 items. */}
        {data.map((d, i) => {
          const showEvery = data.length > 10 ? 2 : 1
          if (i % showEvery !== 0) return null
          const cx = PAD_LEFT + barSlot * i + barSlot / 2
          const cy = PAD_TOP + CHART_H + 12
          return (
            <text
              key={`x-${d.date}`}
              x={cx}
              y={cy}
              textAnchor="end"
              transform={`rotate(-45 ${cx} ${cy})`}
              className="fill-zinc-500 text-[9px]"
            >
              {shortDate(d.date)}
            </text>
          )
        })}
      </svg>

      {/* Tooltip — positioned relative to the wrapper. */}
      {hover ? (
        <div
          className="pointer-events-none absolute z-10 rounded-md border bg-white px-2 py-1 text-xs shadow-sm dark:bg-zinc-900"
          style={{
            left: Math.min(hover.x + 12, 9999),
            top: Math.max(hover.y - 36, 0),
          }}
        >
          <div className="font-mono text-[10px] text-muted-foreground">
            {data[hover.index]?.date}
          </div>
          <div className="font-medium">
            {USD.format(data[hover.index]?.cost_usd ?? 0)}
          </div>
        </div>
      ) : null}
    </div>
  )
}
