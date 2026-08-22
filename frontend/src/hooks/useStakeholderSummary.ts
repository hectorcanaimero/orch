import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { StakeholderSummary } from "@/lib/types"

const DEFAULT_INTERVAL_MS = 10000

async function fetchStakeholderSummary(): Promise<StakeholderSummary> {
  const { data } = await apiClient.get<StakeholderSummary>("/stakeholder/summary")
  return data
}

export function useStakeholderSummary() {
  return useQuery<StakeholderSummary, Error>({
    queryKey: ["stakeholder", "summary"],
    queryFn: fetchStakeholderSummary,
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return DEFAULT_INTERVAL_MS
      const seconds = data.refresh_interval_s
      if (!seconds || seconds <= 0) return DEFAULT_INTERVAL_MS
      return seconds * 1000
    },
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      // don't retry auth errors
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
