import { useQuery } from "@tanstack/react-query"
import { getProjectConfig } from "@/lib/api"
import type { ProjectConfig } from "@/lib/types"

export function useProjectConfig() {
  return useQuery<ProjectConfig, Error>({
    queryKey: ["project-config"],
    queryFn: getProjectConfig,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
