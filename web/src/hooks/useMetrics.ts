import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"

export interface MetricsByModelRow {
  model: string
  tasks_total: number
  tokens_in: number
  tokens_out: number
  cost_usd: number
}

export interface MetricsByDayRow {
  date: string
  tokens_in: number
  tokens_out: number
  cost_usd: number
  tasks_total: number
}

export interface MetricsResponse {
  project_id: string
  total_cost_usd: number
  by_model: MetricsByModelRow[]
  by_day: MetricsByDayRow[]
  estimate_hours_total: number
}

async function fetchMetrics(): Promise<MetricsResponse> {
  const { data } = await apiClient.get<MetricsResponse>("/api/metrics")
  return data
}

/**
 * Fetches `/api/metrics` (operator profile). Cache key `['metrics']` is used
 * so the SSE-driven page can call `queryClient.invalidateQueries({queryKey:
 * ['metrics']})` when new events arrive during a run.
 */
export function useMetrics() {
  return useQuery<MetricsResponse, Error>({
    queryKey: ["metrics"],
    queryFn: fetchMetrics,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
