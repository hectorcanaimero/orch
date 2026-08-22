import { useMutation, useQueryClient } from "@tanstack/react-query"
import { regenerateArchitecture } from "@/lib/api"
import type { ArchitectureRegenerateResponse } from "@/lib/types"

export function useRegenerateArchitecture() {
  const qc = useQueryClient()
  return useMutation<ArchitectureRegenerateResponse, Error, void>({
    mutationFn: regenerateArchitecture,
    onSuccess: () => {
      // Status flips to `regenerate_in_progress=true` almost immediately;
      // history refetch keeps the snapshot list warm for when it lands.
      void qc.invalidateQueries({ queryKey: ["arch-status"] })
      void qc.invalidateQueries({ queryKey: ["arch-history"] })
    },
  })
}
