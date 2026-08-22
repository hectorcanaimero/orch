export interface HorizontalBarByModelDatum {
  model: string
  cost_usd: number
  tasks_total: number
}

export interface HorizontalBarByModelProps {
  data: HorizontalBarByModelDatum[]
}

const USD = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

// SVG geometry — viewBox stays fixed; the outer <svg> stretches to container
// width. Per-row height is constant so the chart grows vertically with N.
const VB_WIDTH = 560
const ROW_H = 26
const LABEL_W = 180 // left model-name column
const VALUE_W = 78 // right cost column
const BAR_LEFT = LABEL_W + 8
const BAR_RIGHT = VB_WIDTH - VALUE_W - 8
const BAR_W = BAR_RIGHT - BAR_LEFT
const BAR_HEIGHT = 12

/** Truncate long model names so the SVG text doesn't overflow the label column. */
function truncate(s: string, max = 22): string {
  if (s.length <= max) return s
  return s.slice(0, max - 1) + "…"
}

/**
 * Hand-rolled SVG horizontal bar chart for cost-by-model. Sorted by cost
 * desc. Bar length is proportional to the max cost so the top spender always
 * fills 100 % of the plot area. Colors reuse the same zinc-800/200 palette as
 * the day chart to stay minimal.
 */
export function HorizontalBarByModel({ data }: HorizontalBarByModelProps) {
  if (data.length === 0) {
    return (
      <div className="flex h-60 items-center justify-center">
        <p className="text-sm text-muted-foreground">No spend recorded yet.</p>
      </div>
    )
  }

  const sorted = [...data].sort((a, b) => b.cost_usd - a.cost_usd)
  const maxCost = Math.max(...sorted.map((d) => d.cost_usd), 0)
  const vbHeight = sorted.length * ROW_H + 8

  return (
    <svg
      viewBox={`0 0 ${VB_WIDTH} ${vbHeight}`}
      width="100%"
      height={vbHeight}
      role="img"
      aria-label="Cost by model"
    >
      {sorted.map((d, i) => {
        const ratio = maxCost > 0 ? d.cost_usd / maxCost : 0
        const width = Math.max(BAR_W * ratio, d.cost_usd > 0 ? 2 : 0)
        const rowY = 4 + i * ROW_H
        const barY = rowY + (ROW_H - BAR_HEIGHT) / 2
        const textY = rowY + ROW_H / 2 + 4
        return (
          <g key={d.model}>
            <title>{`${d.model} — ${USD.format(d.cost_usd)} (${d.tasks_total} tasks)`}</title>
            <text
              x={LABEL_W}
              y={textY}
              textAnchor="end"
              className="fill-zinc-700 text-[11px] dark:fill-zinc-300"
              style={{ fontFamily: "ui-monospace, SFMono-Regular, monospace" }}
            >
              {truncate(d.model)}
            </text>
            <rect
              x={BAR_LEFT}
              y={barY}
              width={BAR_W}
              height={BAR_HEIGHT}
              rx={2}
              className="fill-zinc-100 dark:fill-zinc-900"
            />
            <rect
              x={BAR_LEFT}
              y={barY}
              width={width}
              height={BAR_HEIGHT}
              rx={2}
              className="fill-zinc-800 dark:fill-zinc-200"
            />
            <text
              x={BAR_RIGHT + 8}
              y={textY}
              className="fill-zinc-700 text-[11px] tabular-nums dark:fill-zinc-300"
            >
              {USD.format(d.cost_usd)}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
