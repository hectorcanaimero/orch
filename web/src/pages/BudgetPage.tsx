import type { ReactNode } from "react"
import { AlertTriangle } from "lucide-react"
import { describeLoadError } from "@/lib/errors"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { BudgetChart } from "@/components/charts/BudgetChart"
import { Explain } from "@/components/Explain"
import { useBudgetSummary, type BudgetRow, type BudgetSummary } from "@/hooks/useBudgetSummary"
import { fmt, t } from "@/i18n"

function Code({ children }: { children: ReactNode }) {
  return <code className="font-mono text-[0.9em] break-all">{children}</code>
}

function Explanation() {
  return (
    <div className="max-w-3xl space-y-2 text-sm text-muted-foreground">
      <p>
        {t("budget.explain_window_before")} <Explain term="budget_window">{t("budget.explain_window_term")}</Explain>{" "}
        {t("budget.explain_window_middle")} <Explain term="weighted_tokens">{t("budget.explain_weighted_term")}</Explain>
        {t("budget.explain_window_after")}
      </p>
      <p>
        <Code>budget.per_dispatch_usd</Code> {t("budget.explain_per_dispatch")} <Code>--max-budget-usd</Code>.
      </p>
    </div>
  )
}

function NotConfigured({ data }: { data?: BudgetSummary }) {
  let detail: ReactNode
  switch (data?.reason) {
    case "not_configured":
      detail = (
        <>
          <Code>budgets_config</Code> {t("budget.off_not_configured")}
        </>
      )
      break
    case "invalid":
      detail = t("budget.off_invalid", { error: data.error ?? "" })
      break
    default:
      detail = (
        <>
          {t("budget.off_missing_before")} <Code>{data?.path || "budgets.yaml"}</Code>. {t("budget.off_missing_init")}{" "}
          <Code>orch init</Code>; {t("budget.off_missing_existing")} <Code>budgets_config</Code>{" "}
          {t("budget.off_missing_after")}
        </>
      )
  }
  return (
    <Alert className="border-status-blocked/40 bg-status-blocked/10 [&>svg]:text-status-blocked">
      <AlertTriangle className="h-4 w-4" />
      <AlertTitle>{t("budget.off_title")}</AlertTitle>
      <AlertDescription>{detail}</AlertDescription>
    </Alert>
  )
}

function CostCell({ row }: { row: BudgetRow }) {
  switch (row.cost_source) {
    case "reported":
      return (
        <span className="inline-flex items-center gap-2 tabular-nums">
          {fmt.usd(row.cost_usd)}
          <Badge variant="success">{t("budget.source.reported")}</Badge>
        </span>
      )
    case "estimated":
      return (
        <span className="inline-flex items-center gap-2 tabular-nums" title={t("budget.source.estimated_hint")}>
          ~{fmt.usd(row.estimated_cost_usd)}
          <Badge variant="info">{t("budget.source.estimated")}</Badge>
        </span>
      )
    case "no_data":
      return (
        <span className="inline-flex items-center gap-2" title={t("budget.source.no_data_hint")}>
          —<Badge variant="warning">{t("budget.source.no_data")}</Badge>
        </span>
      )
    default:
      return <span className="text-muted-foreground">{t("budget.source.none")}</span>
  }
}

function Header({ children }: { children?: ReactNode }) {
  return (
    <header className="space-y-2">
      <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("budget.title")}</h1>
      {children}
    </header>
  )
}

export function BudgetPage() {
  const { data, isLoading, isError, error } = useBudgetSummary()

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Header />
        <Skeleton className="h-48 w-full rounded-xl" />
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>{t("budget.load_failed")}</AlertTitle>
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  if (!data || !data.available) {
    return (
      <div className="space-y-6">
        <Header />
        <NotConfigured data={data} />
        <Explanation />
      </div>
    )
  }

  return (
    <div className="space-y-8">
      <Header>
        <p className="text-sm text-muted-foreground">
          {t("budget.preset")} <span className="font-medium text-foreground">{data.preset}</span> {t("budget.from")}{" "}
          <Code>{data.path}</Code>
        </p>
        <Explanation />
      </Header>

      <section className="space-y-3 rounded-xl border bg-card p-5" aria-labelledby="window-heading">
        <h2 id="window-heading" className="text-base font-semibold">
          {t("budget.chart_title")}
        </h2>
        <div className="overflow-x-auto">
          <div className="min-w-[36rem]">
            <BudgetChart rows={data.rows} />
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("budget.chart_bars")} <Code>token_budget</Code>; {t("budget.chart_line")} <Code>threshold_pct</Code>,{" "}
          {t("budget.chart_line_after")}
        </p>
      </section>

      <section className="space-y-3" aria-labelledby="providers-heading">
        <h2 id="providers-heading" className="text-lg font-semibold">
          {t("budget.providers")}
        </h2>
        <div className="overflow-x-auto rounded-xl border bg-card [&_th]:whitespace-nowrap">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("budget.col.provider")}</TableHead>
                <TableHead>
                  <Explain term="budget_window">{t("budget.col.window")}</Explain>
                </TableHead>
                <TableHead className="text-right">
                  <Explain term="weighted_tokens">{t("budget.col.used")}</Explain>
                </TableHead>
                <TableHead>{t("budget.col.resets")}</TableHead>
                <TableHead>
                  <Explain term="cost_source">{t("budget.col.spend")}</Explain>
                </TableHead>
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
                        {r.over_threshold ? <Badge variant="warning">{t("budget.paused")}</Badge> : null}
                      </span>
                    </TableCell>
                    <TableCell className="whitespace-nowrap tabular-nums">{t("budget.hours", { hours: r.window_hours })}</TableCell>
                    <TableCell className="whitespace-nowrap text-right tabular-nums">
                      {fmt.number(r.tokens_used)} / {fmt.number(cap)}
                      {r.raw_tokens_used !== r.tokens_used ? (
                        <span className="block text-xs text-muted-foreground">
                          {t("budget.raw", { tokens: fmt.number(r.raw_tokens_used) })}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="whitespace-nowrap tabular-nums">{r.reset_at ? fmt.dateTime(r.reset_at) : "—"}</TableCell>
                    <TableCell className="whitespace-nowrap">
                      <CostCell row={r} />
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </section>

      <section className="space-y-3" aria-labelledby="waiting-heading">
        <h2 id="waiting-heading" className="text-lg font-semibold">
          {t("budget.waiting")}
        </h2>
        {data.waiting.length === 0 ? (
          <p className="rounded-xl border border-dashed p-6 text-sm text-muted-foreground">{t("budget.waiting_none")}</p>
        ) : (
          <ul className="divide-y rounded-xl border bg-card px-5 text-sm">
            {data.waiting.map((w) => (
              <li key={w.task_id} className="flex flex-wrap items-baseline justify-between gap-2 py-3">
                <span>
                  <span className="font-mono">{w.task_id}</span> {t("budget.waits_on")}{" "}
                  <span className="font-medium">{w.provider}</span>
                </span>
                <span className="text-muted-foreground tabular-nums">
                  {t("budget.until", { time: fmt.dateTime(w.reset_at) })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
