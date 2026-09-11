import { useQuery } from "@tanstack/react-query"
import { getTunnelStatus } from "@/lib/api"
import type { TunnelStatus } from "@/lib/types"

// 3s while idle — the operator only cares about status because they're
// about to click Start; polling faster wastes cycles for a UI that isn't
// changing.
const IDLE_INTERVAL_MS = 3_000
// 1s during transitions (starting/running/stopping) so URL parsing and
// state hand-offs land in the panel within a single tick.
const ACTIVE_INTERVAL_MS = 1_000

interface UseTunnelStatusOptions {
  /**
   * When false, the query is disabled outright — the parent already decided
   * (via `useTunnelCapabilities`) that the panel MUST NOT mount, so status
   * polling would just generate 403/404 noise.
   */
  enabled: boolean
}

/**
 * Sprint E-5 — polling status for `/api/tunnel/status` (TUN-5).
 *
 * `enabled` gates the entire query. The parent is expected to derive it from
 * `useTunnelCapabilities().data?.can_control` and NEVER mount this hook when
 * that is false — but we keep the guard here as belt-and-suspenders in case
 * a future consumer forgets.
 */
export function useTunnelStatus({ enabled }: UseTunnelStatusOptions) {
  return useQuery<TunnelStatus, Error>({
    queryKey: ["tunnel-status"],
    queryFn: getTunnelStatus,
    enabled,
    staleTime: 500,
    refetchOnWindowFocus: false,
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return ACTIVE_INTERVAL_MS
      return data.state === "idle" || data.state === "error"
        ? IDLE_INTERVAL_MS
        : ACTIVE_INTERVAL_MS
    },
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403 || status === 404) return false
      return failureCount < 2
    },
  })
}
