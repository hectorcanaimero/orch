import { useQuery } from "@tanstack/react-query"
import { getArchitectureHistory } from "@/lib/api"
import type { ArchitectureHistory } from "@/lib/types"

export function useArchitectureHistory() {
  return useQuery<ArchitectureHistory, Error>({
    queryKey: ["arch-history"],
    queryFn: getArchitectureHistory,
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
