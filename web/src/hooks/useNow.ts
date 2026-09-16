import { useEffect, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { BudgetWaiting } from "@/hooks/useBudgetSummary"
import type { SprintBlocker, SprintHealth } from "@/lib/types"

export interface NowRun {
  run_id: string
  started_at: string
  updated_at: string
  mode: string
  /** "live" or "done", as the run recorded it. */
  status: string
}

export interface NowDispatch {
  task_id: string
  title: string
  phase: number
  provider: string
  model: string
  attempt: number
  /** retry.max_attempts; 0 when config.yaml does not load. */
  max_attempts: number
  started_at: string
}

export interface Now {
  project: string
  run: NowRun | null
  working: NowDispatch[]
  slots: { used: number; max: number }
  summary: { total: number; done: number; in_progress: number; blocked: number; backlog: number }
  pace: SprintHealth
  attention: SprintBlocker[]
  waiting_for_budget: BudgetWaiting[]
}

async function fetchNow(): Promise<Now> {
  const { data } = await apiClient.get<Now>("/api/now")
  return data
}

export function useNow() {
  return useQuery({
    // Under "tasks" on purpose: the event stream (and its polling fallback)
    // invalidates ["tasks"], so the Now page moves with every event without a
    // second subscription.
    queryKey: ["tasks", "now"],
    queryFn: fetchNow,
    refetchInterval: 30_000,
  })
}

/** The current time, ticking once a second — what live elapsed timers read. */
export function useTicker(intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(id)
  }, [intervalMs])
  return now
}
