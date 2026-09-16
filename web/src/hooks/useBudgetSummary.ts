import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"

/** Where a provider's USD figure for today comes from. */
export type CostSource = "reported" | "estimated" | "no_data" | "none"

export interface BudgetRow {
  provider: string
  token_budget: number
  /** What the gate compares with the cap: cache reads weighted 10%, cache writes 125%. */
  tokens_used: number
  /** The same window as the CLIs reported it, unweighted. */
  raw_tokens_used: number
  pct: number
  threshold_pct: number
  over_threshold: boolean
  window_hours: number
  /** When a capped window is estimated to free; null while under the cap. */
  reset_at: string | null
  /** Today's cost the CLI reported (0 when it reported none). */
  cost_usd: number
  cost_source: CostSource
  /** Today's cost priced from pricing.yaml, for rows the CLI did not price. */
  estimated_cost_usd: number
}

export interface BudgetWaiting {
  task_id: string
  provider: string
  reset_at: string
  reason: string
}

export interface BudgetSummary {
  available: boolean
  /** The preset and file `orch run` enforces. */
  preset: string
  path: string
  /** Why `available` is false. */
  reason?: "not_configured" | "missing" | "invalid"
  error?: string
  rows: BudgetRow[]
  waiting: BudgetWaiting[]
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
