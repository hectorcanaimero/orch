import { useQuery } from "@tanstack/react-query"
import type { SprintHealth } from "@/lib/types"

async function fetchSprintHealth(): Promise<SprintHealth> {
  const resp = await fetch("/api/sprint")
  if (!resp.ok) throw new Error(`sprint fetch failed: ${resp.status}`)
  return resp.json() as Promise<SprintHealth>
}

export function useSprintHealth() {
  return useQuery({
    queryKey: ["sprint-health"],
    queryFn: fetchSprintHealth,
    refetchInterval: 30_000,
  })
}
