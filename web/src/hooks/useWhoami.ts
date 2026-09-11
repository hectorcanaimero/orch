import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"

export type DashboardProfile = "operator" | "stakeholder" | "both"

interface Whoami {
  profile: DashboardProfile
}

async function fetchWhoami(): Promise<Whoami> {
  const { data } = await apiClient.get<Whoami>("/api/whoami")
  return data
}

/**
 * Sprint E-6 UX: the SPA reads its own profile so it can hide operator-only
 * nav entries (Doctor, Tunnel, Metrics, Logs) when serving a stakeholder
 * session. The endpoint is intentionally on the stakeholder allow-list —
 * profile is not a secret; the token is.
 *
 * Staler than `useProjectConfig` on purpose (the profile only changes when
 * the dashboard is relaunched) and never retried on 4xx (the SPA falls
 * back to "operator" so we don't accidentally hide UI from operators when
 * a transient error scrubs the profile field).
 */
export function useWhoami() {
  return useQuery<Whoami, Error>({
    queryKey: ["whoami"],
    queryFn: fetchWhoami,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
