import { useQuery } from "@tanstack/react-query"

export interface BudgetRow {
  provider: string
  token_budget: number
  tokens_used: number
  pct: number
  threshold_pct: number
  over_threshold: boolean
  cost_usd: number
}

export interface BudgetSummary {
  available: boolean
  rows: BudgetRow[]
}

async function fetchBudgetSummary(): Promise<BudgetSummary> {
  const resp = await fetch("/api/budget/summary")
  if (!resp.ok) throw new Error(`budget summary failed: ${resp.status}`)
  return (await resp.json()) as BudgetSummary
}

export function useBudgetSummary() {
  return useQuery({
    queryKey: ["budget-summary"],
    queryFn: fetchBudgetSummary,
    refetchInterval: 15_000,
  })
}
