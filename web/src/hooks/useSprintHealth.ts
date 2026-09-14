import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { SprintHealth } from "@/lib/types"

async function fetchSprintHealth(): Promise<SprintHealth> {
  const { data } = await apiClient.get<SprintHealth>("/api/sprint")
  return data
}

export function useSprintHealth() {
  return useQuery({
    queryKey: ["sprint-health"],
    queryFn: fetchSprintHealth,
    refetchInterval: 30_000,
  })
}
