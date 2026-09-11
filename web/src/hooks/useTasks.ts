import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { TaskFilters, TasksResponse } from "@/lib/types"

function buildParams(filters: TaskFilters): Record<string, string | number> {
  const params: Record<string, string | number> = {}
  if (filters.phase !== undefined) params.phase = filters.phase
  if (filters.status) params.status = filters.status
  if (filters.model) params.model = filters.model
  if (filters.q) params.q = filters.q
  if (filters.parallelizable !== undefined) {
    params.parallelizable = filters.parallelizable
  }
  return params
}

async function fetchTasks(filters: TaskFilters): Promise<TasksResponse> {
  const { data } = await apiClient.get<TasksResponse>("/api/tasks", {
    params: buildParams(filters),
  })
  return data
}

export function useTasks(filters: TaskFilters = {}) {
  return useQuery<TasksResponse, Error>({
    // filters is part of the cache key so changing filters triggers a refetch
    // but the object identity doesn't matter — react-query serializes it.
    queryKey: ["tasks", filters],
    queryFn: () => fetchTasks(filters),
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
