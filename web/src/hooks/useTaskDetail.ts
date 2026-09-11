import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { Task } from "@/lib/types"

async function fetchTaskDetail(id: string): Promise<Task> {
  const { data } = await apiClient.get<Task>(
    `/api/task/${encodeURIComponent(id)}`,
  )
  return data
}

export function useTaskDetail(taskId: string | null) {
  return useQuery<Task, Error>({
    queryKey: ["task", taskId],
    queryFn: () => fetchTaskDetail(taskId as string),
    enabled: Boolean(taskId),
    retry: (failureCount, error) => {
      const status = (error as unknown as { response?: { status?: number } })
        ?.response?.status
      if (status === 404 || status === 401 || status === 403) return false
      return failureCount < 2
    },
  })
}
