import { AlertTriangle } from "lucide-react"
import { describeLoadError } from "@/lib/errors"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { BudgetChart } from "@/components/charts/BudgetChart"
import {
  useBudgetSummary,
  type BudgetRow,
  type BudgetSummary,
} from "@/hooks/useBudgetSummary"

const USD = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})
const INT = new Intl.NumberFormat("en-US")

/** "2026-09-12T15:00:00Z" → "Sep 12, 15:00" in the viewer's time zone. */
function formatReset(iso: string | null): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  })
}

function Explanation() {
  return (
    <p className="max-w-3xl text-sm text-muted-foreground">
      The guardrail counts the tokens each provider used in its rolling window:
      input and output at full weight, prompt-cache reads at 10% and cache
      writes at 125%, as Anthropic bills them. When a provider
      reaches its threshold, orch stops sending it new tasks until older usage
      leaves the window; tasks routed elsewhere keep running.{" "}
      <code className="font-mono">budget.per_dispatch_usd</code> is separate:
      it decides whether a failing task may escalate to a pricier model, and,
      only when written in config.yaml, caps each claude dispatch (
      <code className="font-mono">--max-budget-usd</code>).
    </p>
  )
}

function NotConfigured({ data }: { data?: BudgetSummary }) {
  let detail: React.ReactNode
  switch (data?.reason) {
    case "not_configured":
      detail = (
        <>
          <code className="font-mono">budgets_config</code> is empty in
          config.yaml, so no budgets file is read.
        </>
      )
      break
    case "invalid":
      detail = (
        <>
          The budgets file could not be read: {data.error}
        </>
      )
      break
    default:
      detail = (
        <>
          There is no file at{" "}
          <code className="font-mono break-all">{data?.path || "budgets.yaml"}</code>
          . New projects get it from <code className="font-mono">orch init</code>;
          for an existing one, copy a presets file there or point{" "}
          <code className="font-mono">budgets_config</code> at one.
        </>
      )
  }
  return (
    <Alert className="border-amber-300 bg-amber-50 text-amber-900">
      <AlertTriangle className="h-4 w-4" />
      <AlertTitle>Budget guardrail is off — runs are not rationed</AlertTitle>
      <AlertDescription>{detail}</AlertDescription>
    </Alert>
  )
}

function CostCell({ row }: { row: BudgetRow }) {
  switch (row.cost_source) {
    case "reported":
      return (
        <span className="inline-flex items-center gap-2">
          {USD.format(row.cost_usd)}
          <Badge variant="success">reported</Badge>
        </span>
      )
    case "estimated":
      return (
        <span
          className="inline-flex items-center gap-2"
          title="The CLI reports tokens but no price; priced from pricing.yaml"
        >
          ~{USD.format(row.estimated_cost_usd)}
          <Badge variant="info">estimated</Badge>
        </span>
      )
    case "no_data":
      return (
        <span
          className="inline-flex items-center gap-2"
          title="The CLI reports neither tokens nor cost; each dispatch counts as typical_dispatch_tokens in the window"
        >
          —<Badge variant="warning">no data</Badge>
        </span>
      )
    default:
      return <span className="text-muted-foreground">nothing today</span>
  }
}

export function BudgetPage() {
  const { data, isLoading, isError, error } = useBudgetSummary()

  if (isLoading) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Budget</h1>
        <Skeleton className="h-48 w-full" />
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load budget</AlertTitle>
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  if (!data || !data.available) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Budget</h1>
        <NotConfigured data={data} />
        <Explanation />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Budget</h1>
        <p className="text-sm text-muted-foreground">
          Preset <span className="font-medium text-foreground">{data.preset}</span>{" "}
          from <code className="font-mono break-all">{data.path}</code>
        </p>
        <Explanation />
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">
            Tokens used in the window, per provider
          </CardTitle>
        </CardHeader>
        <CardContent>
          <BudgetChart rows={data.rows} />
          <p className="mt-3 text-xs text-muted-foreground">
            Bars compare tokens used against{" "}
            <code className="font-mono">token_budget</code>; the dashed line
            is <code className="font-mono">threshold_pct</code>, where
            dispatching stops.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">Providers</CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Provider</TableHead>
                <TableHead>Window</TableHead>
                <TableHead className="text-right">Used / cap</TableHead>
                <TableHead>Resets</TableHead>
                <TableHead>Spend today (UTC)</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.rows.map((r) => {
                const cap = Math.floor((r.token_budget * r.threshold_pct) / 100)
                return (
                  <TableRow key={r.provider}>
                    <TableCell className="font-medium">
                      <span className="inline-flex items-center gap-2">
                        {r.provider}
                        {r.over_threshold ? (
                          <Badge variant="danger">paused</Badge>
                        ) : null}
                      </span>
                    </TableCell>
                    <TableCell>{r.window_hours}h</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {INT.format(r.tokens_used)} / {INT.format(cap)}
                      {r.raw_tokens_used !== r.tokens_used ? (
                        <span className="block text-xs text-muted-foreground">
                          {INT.format(r.raw_tokens_used)} raw
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell>{formatReset(r.reset_at)}</TableCell>
                    <TableCell>
                      <CostCell row={r} />
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">Waiting for budget</CardTitle>
        </CardHeader>
        <CardContent>
          {data.waiting.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No task is waiting for a provider's window to reset.
            </p>
          ) : (
            <ul className="divide-y text-sm">
              {data.waiting.map((w) => (
                <li
                  key={w.task_id}
                  className="flex flex-wrap items-baseline justify-between gap-2 py-2"
                >
                  <span>
                    <span className="font-mono">{w.task_id}</span> waits on{" "}
                    <span className="font-medium">{w.provider}</span>
                  </span>
                  <span className="text-muted-foreground">
                    until {formatReset(w.reset_at)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
