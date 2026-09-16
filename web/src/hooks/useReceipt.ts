import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"

// GET /api/receipt and GET /api/onboarding (internal/dashboard/receiptroutes.go).

export type CostSource = "reported" | "estimated" | "no_data"

export interface ReceiptProvider {
  provider: string
  dispatches: number
  tokens_in: number
  tokens_out: number
  /** What the budget gate counts: cache reads at 10%, writes at 125%. */
  weighted_tokens: number
  cost_usd: number
  estimated_cost_usd: number
  cost_source: CostSource
}

export interface TaskRef {
  task_id: string
  title: string
}

export interface Receipt {
  run_id: string
  started_at: string
  finished_at: string
  finished: boolean
  wall_seconds: number
  agent_seconds: number
  dispatches: number
  failed_attempts: number
  retries: number
  done: TaskRef[]
  blocked: TaskRef[]
  providers: ReceiptProvider[]
  total_cost_usd: number
  estimated_cost_usd: number
  prs: { task_id: string; title: string; url: string; ci_status: string }[]
}

export interface ReceiptPayload {
  /** The latest finished run; null before any run has finished. */
  receipt: Receipt | null
  /** `orch report receipt`'s Markdown, without the Built-with footer. */
  markdown: string
  built_with: string
}

/** The Markdown a person copies: the receipt, then the footer unless they unticked it. */
export function receiptMarkdown(payload: ReceiptPayload, builtWith: boolean): string {
  return builtWith ? `${payload.markdown}\n${payload.built_with}\n` : payload.markdown
}

export function useReceipt() {
  return useQuery({
    // Under "tasks" so the event stream's invalidation refreshes it: a run
    // finishing is an event.
    queryKey: ["tasks", "receipt"],
    queryFn: async () => (await apiClient.get<ReceiptPayload>("/api/receipt")).data,
  })
}

export type OnboardingId = "providers" | "budget" | "tasks" | "vcs" | "tunnel" | "first_run"

export interface OnboardingItem {
  id: OnboardingId
  done: boolean
  optional: boolean
  detail: string
  command?: string
  link?: string
}

export interface Onboarding {
  /** A run has finished: the checklist has done its job. */
  complete: boolean
  items: OnboardingItem[]
}

export function useOnboarding() {
  return useQuery({
    // Not under "tasks": its checks run the provider CLIs, so it refreshes on
    // "Check again", not on every event.
    queryKey: ["onboarding"],
    queryFn: async () => (await apiClient.get<Onboarding>("/api/onboarding")).data,
    staleTime: Infinity,
  })
}
