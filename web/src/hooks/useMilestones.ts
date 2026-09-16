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

// A milestone is a phase of tasks.json — the same rows the client portal
// shows. The server derives it (snapshot.PhaseMilestones); nothing stores it.
export interface Milestone {
  phase: number
  name: string
  status: "done" | "active" | "pending"
  progress: MilestoneProgress
  in_progress: number
  blocked: number
  // Null when no unblocked work remains or no task finished in the last 7 days.
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
