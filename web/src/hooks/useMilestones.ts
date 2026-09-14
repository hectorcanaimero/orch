import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"

export interface MilestoneProgress {
  total: number
  done: number
  pct: number
}

export interface MilestoneEta {
  eta_date: string
  eta_days: number
  confidence: "high" | "low"
}

export interface Milestone {
  id: string
  title: string
  description: string | null
  target_date: string | null
  status: "open" | "completed" | "cancelled"
  created_at: string
  progress: MilestoneProgress
  eta: MilestoneEta | null
}

// Through apiClient, not bare fetch: a stakeholder session's token rides on
// its interceptor, and a 401 is handled like everywhere else.
async function fetchMilestones(): Promise<Milestone[]> {
  const { data } = await apiClient.get<{ milestones: Milestone[] }>("/api/milestones")
  return data.milestones
}

export function useMilestones() {
  return useQuery({
    queryKey: ["milestones"],
    queryFn: fetchMilestones,
    refetchInterval: 10_000,
  })
}
