import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { EventsHistoryResponse } from "@/lib/types"

export interface UseEventsHistoryOptions {
  /** Optional task_id filter — passed to the backend, not applied client-side. */
  taskId?: string
  /** Max rows to fetch. Backend caps at 1000. Default 200. */
  limit?: number
  /** Set to false to skip the fetch (e.g. before auth is available). */
  enabled?: boolean
}

async function fetchEventsHistory(
  taskId: string | undefined,
  limit: number,
): Promise<EventsHistoryResponse> {
  const params: Record<string, string | number> = { limit }
  if (taskId) params.task_id = taskId
  const { data } = await apiClient.get<EventsHistoryResponse>("/api/events", {
    params,
  })
  return data
}

/**
 * Fetches recent formatted events from `GET /api/events`.
 *
 * Cache key: `['events', { taskId, limit }]` — a change to either param
 * triggers a re-fetch. `staleTime: 10_000` (10s) keeps the SPA from
 * hammering the backend when the operator toggles between pages, while
 * still refreshing fast enough after task_id changes.
 *
 * The live tail is layered on top by `useLiveLogs()` — this hook is only
 * for the initial history seed.
 */
export function useEventsHistory(opts: UseEventsHistoryOptions = {}) {
  const { taskId, limit = 200, enabled = true } = opts
  return useQuery<EventsHistoryResponse, Error>({
    queryKey: ["events", { taskId: taskId ?? null, limit }],
    queryFn: () => fetchEventsHistory(taskId, limit),
    enabled,
    staleTime: 10_000,
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
