import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { GraphResponse } from "@/lib/types"

async function fetchGraph(): Promise<GraphResponse> {
  const { data } = await apiClient.get<GraphResponse>("/api/graph")
  return data
}

export function useGraph() {
  return useQuery<GraphResponse, Error>({
    queryKey: ["graph"],
    queryFn: fetchGraph,
    staleTime: 10_000,
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
