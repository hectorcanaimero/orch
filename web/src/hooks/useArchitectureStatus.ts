import { useQuery } from "@tanstack/react-query"
import { getArchitectureStatus } from "@/lib/api"
import type { ArchitectureStatus } from "@/lib/types"

const IDLE_INTERVAL_MS = 30_000
// 1s while a run is live — matches the elapsed counter tick and keeps
// phase transitions (dispatching → claude_working → finalizing) responsive.
const REGEN_INTERVAL_MS = 1_000

export function useArchitectureStatus() {
  return useQuery<ArchitectureStatus, Error>({
    queryKey: ["arch-status"],
    queryFn: getArchitectureStatus,
    staleTime: 10_000,
    // Poll fast while a regeneration is in flight so the UI reflects
    // completion within a few seconds; drop back to a lazy cadence otherwise.
    refetchInterval: (query) => {
      const data = query.state.data
      return data?.regenerate_in_progress ? REGEN_INTERVAL_MS : IDLE_INTERVAL_MS
    },
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
