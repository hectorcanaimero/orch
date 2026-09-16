import { fmt, t } from "@/i18n"
export interface HorizontalBarByModelDatum {
  model: string
  cost_usd: number
  tasks_total: number
}

export interface HorizontalBarByModelProps {
  data: HorizontalBarByModelDatum[]
}


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
 * fills 100 % of the plot area. Bars are a neutral ink on a muted track, the
 * same pairing as the day chart.
 */
export function HorizontalBarByModel({ data }: HorizontalBarByModelProps) {
  if (data.length === 0) {
    return (
      <div className="flex h-60 items-center justify-center">
        <p className="text-sm text-muted-foreground">{t("metrics.no_spend")}</p>
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
      aria-label={t("metrics.by_model")}
    >
      {sorted.map((d, i) => {
        const ratio = maxCost > 0 ? d.cost_usd / maxCost : 0
        const width = Math.max(BAR_W * ratio, d.cost_usd > 0 ? 2 : 0)
        const rowY = 4 + i * ROW_H
        const barY = rowY + (ROW_H - BAR_HEIGHT) / 2
        const textY = rowY + ROW_H / 2 + 4
        return (
          <g key={d.model}>
            <title>{t("metrics.model_tasks", { model: d.model, cost: fmt.usd(d.cost_usd), count: d.tasks_total })}</title>
            <text
              x={LABEL_W}
              y={textY}
              textAnchor="end"
              className="fill-foreground font-mono text-[11px]"
            >
              {truncate(d.model)}
            </text>
            <rect
              x={BAR_LEFT}
              y={barY}
              width={BAR_W}
              height={BAR_HEIGHT}
              rx={2}
              className="fill-muted"
            />
            <rect
              x={BAR_LEFT}
              y={barY}
              width={width}
              height={BAR_HEIGHT}
              rx={2}
              className="fill-muted-foreground/70"
            />
            <text
              x={BAR_RIGHT + 8}
              y={textY}
              className="fill-foreground text-[11px] tabular-nums"
            >
              {fmt.usd(d.cost_usd)}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
