import type { BudgetRow } from "@/hooks/useBudgetSummary"

export interface BudgetChartProps {
  rows: BudgetRow[]
}

// Fixed viewBox, outer <svg> stretches to container width — mirrors
// GanttChart / BarChartByDay. One horizontal bar per provider: track =
// token_budget, fill = tokens_used, a dashed rule marks threshold_pct.
const VB_WIDTH = 900
const ROW_H = 40
const BAR_H = 18
const PAD_LEFT = 110 // provider label gutter
const PAD_RIGHT = 150 // room for "pct% · ~$cost" on the right
const PAD_TOP = 12

const USD = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

/** 12345 → "12.3k", 1200000 → "1.2M". */
function shortTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

/**
 * Hand-rolled SVG budget-vs-actual chart (Sprint G-5). Compares tokens used
 * against the configured token_budget per provider — the unit the guardrail
 * enforces. USD spend rides along as an informational label, NOT the axis.
 */
export function BudgetChart({ rows }: BudgetChartProps) {
  if (rows.length === 0) return null

  const plotW = VB_WIDTH - PAD_LEFT - PAD_RIGHT
  const height = PAD_TOP + rows.length * ROW_H + 8

  return (
    <svg
      viewBox={`0 0 ${VB_WIDTH} ${height}`}
      width="100%"
      role="img"
      aria-label="Budget vs actual by provider"
    >
      {rows.map((r, i) => {
        const rowY = PAD_TOP + i * ROW_H
        const barY = rowY + (ROW_H - BAR_H) / 2
        const fillRatio = Math.min(Math.max(r.pct / 100, 0), 1)
        const fillW = plotW * fillRatio
        const thresholdX = PAD_LEFT + plotW * Math.min(r.threshold_pct / 100, 1)
        return (
          <g key={r.provider}>
            {/* provider label */}
            <text
              x={PAD_LEFT - 10}
              y={barY + BAR_H - 4}
              textAnchor="end"
              className="fill-zinc-700 dark:fill-zinc-300 text-[11px]"
            >
              {r.provider}
            </text>
            {/* track */}
            <rect
              x={PAD_LEFT}
              y={barY}
              width={plotW}
              height={BAR_H}
              rx={3}
              className="fill-zinc-200 dark:fill-zinc-800"
            />
            {/* used fill */}
            <rect
              x={PAD_LEFT}
              y={barY}
              width={fillW}
              height={BAR_H}
              rx={3}
              className={
                r.over_threshold
                  ? "fill-amber-500 dark:fill-amber-400"
                  : "fill-emerald-500 dark:fill-emerald-400"
              }
            />
            {/* threshold rule */}
            <line
              x1={thresholdX}
              x2={thresholdX}
              y1={barY - 3}
              y2={barY + BAR_H + 3}
              className="stroke-zinc-500 dark:stroke-zinc-400"
              strokeWidth={1}
              strokeDasharray="2 2"
            />
            {/* right-hand labels: pct + tokens + informational USD */}
            <text
              x={PAD_LEFT + plotW + 8}
              y={barY + BAR_H - 4}
              className="fill-zinc-600 dark:fill-zinc-400 text-[10px]"
            >
              {r.pct}% · {shortTokens(r.tokens_used)}/{shortTokens(r.token_budget)}
              {r.cost_usd > 0 ? ` · ~${USD.format(r.cost_usd)}` : ""}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
