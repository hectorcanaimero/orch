import { useQuery } from "@tanstack/react-query"
import { getTunnelCapabilities } from "@/lib/api"
import type { TunnelCapabilities } from "@/lib/types"

/**
 * Sprint E-5 — capability probe for `/api/tunnel/*`.
 *
 * `/api/tunnel/capabilities` is intentionally auth-free (TUN-4): the endpoint
 * always returns 200 and reports which of the three gates (config → profile →
 * host) is currently closed. This hook drives the conditional mount from the
 * parent — when `can_control === false` the panel is NOT rendered at all
 * (design D#9, non-negotiable per proposal).
 *
 * Cadence:
 * - `staleTime: Infinity` — capabilities only change when config or the caller
 *   changes profile/network posture; both events force a full page reload in
 *   practice.
 * - `refetchOnWindowFocus: true` — cheap way to re-probe when the user comes
 *   back after a config edit + dashboard restart without a full reload.
 * - `retry: false` — the endpoint returns 200 in every reachable scenario;
 *   any error is a network or CORS issue and retrying won't help.
 */
export function useTunnelCapabilities() {
  return useQuery<TunnelCapabilities, Error>({
    queryKey: ["tunnel-capabilities"],
    queryFn: getTunnelCapabilities,
    staleTime: Infinity,
    refetchOnWindowFocus: true,
    retry: false,
  })
}
