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

const USD = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

const INT = new Intl.NumberFormat("en-US")

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
          <h1 className="text-2xl font-semibold tracking-tight">Metrics</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? `Total cost: ${USD.format(data.total_cost_usd)} across ${modelsCount} model${modelsCount === 1 ? "" : "s"}`
              : "Loading metrics…"}
          </p>
        </div>
        <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
      </header>

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load metrics</AlertTitle>
          <AlertDescription>
            {error?.message ?? "Unknown error"}
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
              label="Total spend"
              value={USD.format(data.total_cost_usd)}
              icon={DollarSign}
            />
            <KpiCard
              label="Tokens (in+out)"
              value={INT.format(totalTokens)}
              icon={Activity}
            />
            <KpiCard
              label="Models used"
              value={modelsCount}
              icon={Cpu}
            />
            <KpiCard
              label="Estimate hours"
              value={`${data.estimate_hours_total}h`}
              icon={Clock}
            />
          </>
        )}
      </div>

      {/* Charts row */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Cost by day (last 14)</CardTitle>
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
            <CardTitle className="text-base">Cost by model</CardTitle>
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
          <CardTitle className="text-base">By model</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Model</TableHead>
                <TableHead className="text-right">Tasks</TableHead>
                <TableHead className="text-right">Tokens in</TableHead>
                <TableHead className="text-right">Tokens out</TableHead>
                <TableHead className="text-right">Cost (USD)</TableHead>
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
                    No model spend yet — run some tasks to populate this view.
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
                      {INT.format(row.tokens_in)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {INT.format(row.tokens_out)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {USD.format(row.cost_usd)}
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
