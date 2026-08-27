import { AlertTriangle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { BudgetChart } from "@/components/charts/BudgetChart"
import { useBudgetSummary } from "@/hooks/useBudgetSummary"

const USD = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

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
        <AlertDescription>{(error as Error)?.message}</AlertDescription>
      </Alert>
    )
  }

  if (!data || !data.available) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Budget</h1>
        <Card>
          <CardHeader>
            <CardTitle>No budget configured</CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            Add a{" "}
            <code className="font-mono">budgets.yaml</code> preset to track
            per-provider token quota vs actual usage.
          </CardContent>
        </Card>
      </div>
    )
  }

  const totalCost = data.rows.reduce((sum, r) => sum + r.cost_usd, 0)

  return (
    <div className="space-y-6">
      <div className="flex items-baseline justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Budget</h1>
        <span className="text-sm text-muted-foreground">
          Spent today: {USD.format(totalCost)}
        </span>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">
            Token budget vs used (per provider)
          </CardTitle>
        </CardHeader>
        <CardContent>
          <BudgetChart rows={data.rows} />
          <p className="mt-3 text-xs text-muted-foreground">
            Bars compare tokens used against the configured{" "}
            <code className="font-mono">token_budget</code> in the rolling
            window — the unit the guardrail enforces. USD is the real spend,
            shown for reference.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
