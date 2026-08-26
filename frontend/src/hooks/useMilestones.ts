import { useQuery } from "@tanstack/react-query"

export interface MilestoneProgress {
  total: number
  done: number
  pct: number
}

export interface Milestone {
  id: string
  title: string
  description: string | null
  target_date: string | null
  status: "open" | "completed" | "cancelled"
  created_at: string
  progress: MilestoneProgress
}

async function fetchMilestones(): Promise<Milestone[]> {
  const resp = await fetch("/api/milestones")
  if (!resp.ok) throw new Error(`milestones fetch failed: ${resp.status}`)
  const data = await resp.json()
  return data.milestones as Milestone[]
}

export function useMilestones() {
  return useQuery({
    queryKey: ["milestones"],
    queryFn: fetchMilestones,
    refetchInterval: 10_000,
  })
}
