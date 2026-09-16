import { useEffect } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Activity, AlertTriangle, Clock, Cpu, DollarSign } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { BarChartByDay } from "@/components/charts/BarChartByDay"
import { HorizontalBarByModel } from "@/components/charts/HorizontalBarByModel"
import { KpiCard } from "@/components/charts/KpiCard"
import { LiveStatusPill } from "@/components/LiveStatusPill"
import { useEventStream } from "@/hooks/useEventStream"
import { useMetrics } from "@/hooks/useMetrics"
import { fmt, t } from "@/i18n"


export function MetricsPage() {
  const queryClient = useQueryClient()
  const { data, isLoading, isError, error } = useMetrics()
  const { status: streamStatus, lastEventAt } = useEventStream()

  // Live invalidation: `useEventStream` already refreshes ['tasks'] on every
  // event. Metrics change when a run happens too, so mirror that here — this
  // keeps the shared hook DRY (no cross-page concern leaks into it) while the
  // page still reacts to live activity.
  useEffect(() => {
    if (lastEventAt) {
      void queryClient.invalidateQueries({ queryKey: ["metrics"] })
    }
  }, [lastEventAt, queryClient])

  const modelsCount = data?.by_model.length ?? 0
  const totalTokens =
    data?.by_model.reduce((acc, m) => acc + m.tokens_in + m.tokens_out, 0) ?? 0
  const sortedByModel = data
    ? [...data.by_model].sort((a, b) => b.cost_usd - a.cost_usd)
    : []

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("metrics.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground tabular-nums">
            {data
              ? t("metrics.total", { cost: fmt.usd(data.total_cost_usd), count: modelsCount }) +
                (data.estimated_cost_usd > 0
                  ? ` — ${t("metrics.total_estimated", { estimated: fmt.usd(data.estimated_cost_usd) })}`
                  : "")
              : t("metrics.loading")}
          </p>
        </div>
        <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
      </header>

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("metrics.load_failed")}</AlertTitle>
          <AlertDescription>
            {error?.message ?? t("common.unknown_error")}
          </AlertDescription>
        </Alert>
      ) : null}

      {/* KPI row */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {isLoading || !data ? (
          <>
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </>
        ) : (
          <>
            <KpiCard
              label={data.estimated_cost_usd > 0 ? t("metrics.kpi.spend_estimated") : t("metrics.kpi.spend")}
              value={fmt.usd(data.total_cost_usd)}
              icon={DollarSign}
            />
            <KpiCard
              label={t("metrics.kpi.tokens")}
              value={fmt.number(totalTokens)}
              icon={Activity}
            />
            <KpiCard
              label={t("metrics.kpi.models")}
              value={modelsCount}
              icon={Cpu}
            />
            <KpiCard
              label={t("metrics.kpi.hours")}
              value={t("task.hours", { hours: data.estimate_hours_total })}
              icon={Clock}
            />
          </>
        )}
      </div>

      {/* Charts row */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("metrics.by_day")}</CardTitle>
          </CardHeader>
          <CardContent>
            {isLoading || !data ? (
              <Skeleton className="h-60" />
            ) : (
              <BarChartByDay data={data.by_day} />
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("metrics.by_model")}</CardTitle>
          </CardHeader>
          <CardContent>
            {isLoading || !data ? (
              <Skeleton className="h-60" />
            ) : (
              <HorizontalBarByModel data={data.by_model} />
            )}
          </CardContent>
        </Card>
      </div>

      {/* Detailed table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("metrics.table")}</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("metrics.col.model")}</TableHead>
                <TableHead className="text-right">{t("metrics.col.tasks")}</TableHead>
                <TableHead className="text-right">{t("metrics.col.tokens_in")}</TableHead>
                <TableHead className="text-right">{t("metrics.col.tokens_out")}</TableHead>
                <TableHead className="text-right">{t("metrics.col.cost")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading || !data ? (
                Array.from({ length: 3 }).map((_, i) => (
                  <TableRow key={`skeleton-${i}`}>
                    <TableCell colSpan={5}>
                      <Skeleton className="h-8 w-full" />
                    </TableCell>
                  </TableRow>
                ))
              ) : sortedByModel.length === 0 ? (
                <TableRow>
                  <TableCell
                    colSpan={5}
                    className="py-8 text-center text-sm text-muted-foreground"
                  >
                    {t("metrics.none")}
                  </TableCell>
                </TableRow>
              ) : (
                sortedByModel.map((row) => (
                  <TableRow key={row.model}>
                    <TableCell className="font-mono text-xs">
                      {row.model}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {row.tasks_total}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {fmt.number(row.tokens_in)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {fmt.number(row.tokens_out)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {fmt.usd(row.cost_usd)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}
