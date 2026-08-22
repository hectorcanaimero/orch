import { useQuery } from "@tanstack/react-query"
import { getDoctorReport } from "@/lib/api"
import type { DoctorReport } from "@/lib/types"

/**
 * Fetch the preflight doctor report from `/api/doctor`.
 *
 * The endpoint runs the same probe logic as `orch doctor --json` and each
 * probe caps at ~5s server-side (see `preflight._PROBE_TIMEOUT_S`), so a
 * cold call is bounded — no client-side timeout gymnastics required.
 *
 * `staleTime: 10_000` matches the "Re-run" button expectation: the operator
 * clicks refresh, we invalidate and refetch immediately (via
 * `queryClient.invalidateQueries({queryKey: ['doctor']})`). Between clicks
 * the page keeps a fresh-ish cached view without hammering the backend.
 */
export function useDoctor() {
  return useQuery<DoctorReport, Error>({
    queryKey: ["doctor"],
    queryFn: getDoctorReport,
    staleTime: 10_000,
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      // Same 401/403 escape as every other operator hook — auth failures
      // don't get retried; the axios interceptor already redirects to /login.
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
