import { useQuery } from "@tanstack/react-query"
import { apiClient } from "@/lib/api"
import type { CiResponse } from "@/lib/ci"

async function fetchCI(): Promise<CiResponse> {
  const { data } = await apiClient.get<CiResponse>("/api/ci")
  return data
}

// /api/ci (operator only): the pipeline as config.yaml and the repo's
// workflows set it up. It changes when a file is edited, not during a run.
export function useCI() {
  return useQuery<CiResponse, Error>({
    queryKey: ["ci"],
    queryFn: fetchCI,
    staleTime: 60_000,
  })
}
