import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"

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
  const { data } = await apiClient.get<BudgetSummary>("/api/budget/summary")
  return data
}

export function useBudgetSummary() {
  return useQuery({
    queryKey: ["budget-summary"],
    queryFn: fetchBudgetSummary,
    refetchInterval: 15_000,
  })
}
