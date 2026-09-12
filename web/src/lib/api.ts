import axios from "axios"
import type {
  Portfolio,
  ProjectConfig,
  TunnelCapabilities,
  TunnelStatus,
} from "@/lib/types"

const TOKEN_KEY = "orch_token"

// API base URL resolution:
// 1. If VITE_API_BASE_URL is set in .env.local (dev split-origin): use it
// 2. Otherwise (production): use window.location.origin (same-origin, port-agnostic)
// 3. Server-side fallback (for build-time or non-browser contexts): use 7420
//
// IMPORTANT: .env.local should ONLY be used for development. The production
// build must NOT have VITE_API_BASE_URL set to prevent hardcoding 7420 into
// the bundle (which breaks multi-port and cross-origin scenarios).
export const API_BASE_URL =
  import.meta.env.VITE_API_BASE_URL ??
  (typeof window !== "undefined" ? window.location.origin : "http://127.0.0.1:7420")

/**
 * Returns the current bearer token from localStorage (or null).
 *
 * Shared between the axios request interceptor and manual fetch call sites
 * (e.g. SSE streaming, which cannot use axios). Do NOT read the token from
 * localStorage anywhere else — always route through this helper so the token
 * key stays defined in one place.
 */
export function getToken(): string | null {
  if (typeof window === "undefined") return null
  return window.localStorage.getItem(TOKEN_KEY)
}

export const apiClient = axios.create({
  baseURL: API_BASE_URL,
})

apiClient.interceptors.request.use((config) => {
  const token = getToken()
  if (token) {
    config.headers = config.headers ?? {}
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

apiClient.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error?.response?.status === 401) {
      if (typeof window !== "undefined") {
        window.localStorage.removeItem(TOKEN_KEY)
        if (window.location.pathname !== "/login") {
          window.location.href = "/login"
        }
      }
    }
    return Promise.reject(error)
  },
)

export async function getProjectConfig(): Promise<ProjectConfig> {
  const { data } = await apiClient.get<ProjectConfig>("/api/config")
  return data
}

/**
 * Sprint E-5 — Tunnel status API surface.
 *
 * G5.5 trimmed the operator SPA's tunnel manager down to a read-only status
 * panel — `startTunnel`/`stopTunnel`/`buildTunnelLogsUrl` (and
 * `TunnelConflictHttpError`, which only their conflict handling needed) are
 * gone along with it; see docs/brainstorm/go-migration-notes/sonnet-2.md.
 *
 * `/capabilities` is intentionally auth-free on the backend (always 200) so
 * we surface it via `apiClient` without special-casing 401/403 — the axios
 * interceptor still won't fire because the endpoint never returns those.
 */
export async function getTunnelCapabilities(): Promise<TunnelCapabilities> {
  const { data } = await apiClient.get<TunnelCapabilities>(
    "/api/tunnel/capabilities",
  )
  return data
}

export async function getTunnelStatus(): Promise<TunnelStatus> {
  const { data } = await apiClient.get<TunnelStatus>("/api/tunnel/status")
  return data
}

/**
 * Thrown by `getPortfolio` when `/api/portfolio` answered with something
 * that isn't a portfolio payload — a single-project dashboard has no
 * route registered for it at all, so the request falls through to the
 * SPA's own catch-all and comes back **200 with the HTML shell**, not a
 * 404. Caught this via opus's review of this PR: axios treats a 200 as
 * success regardless of body, so without this check `getPortfolio` would
 * resolve with a string where a `Portfolio` was promised, and the very
 * first read of `.projects.length` downstream would throw a TypeError
 * instead of the page ever reaching its "portfolio mode is off" branch.
 *
 * Deliberately not an `AxiosError` — the failure isn't in the transport,
 * it's in the body — but `isPortfolioDisabled` (usePortfolio.ts) treats
 * this exactly like the real 404 the dashboard is supposed to send once
 * it registers the route, so this is defense in depth against "the
 * server sent something that isn't my type" generally (a proxy, a
 * redirect, an older binary), not only against today's specific gap.
 */
export class PortfolioShapeError extends Error {
  constructor() {
    super(
      "`/api/portfolio` did not return a portfolio payload — likely the " +
        "SPA's own HTML shell, meaning this dashboard has no portfolio " +
        "route registered (not started with --portfolio)",
    )
    this.name = "PortfolioShapeError"
  }
}

/**
 * G8.5 — `/api/portfolio`. Rejects (via apiClient, so as an axios error
 * carrying `response.status === 404`, OR a `PortfolioShapeError` — see
 * its own doc comment) when the dashboard wasn't started with
 * `--portfolio` — see usePortfolio.isPortfolioDisabled, the one place
 * either shape is interpreted rather than treated as a generic fetch
 * failure.
 */
export async function getPortfolio(): Promise<Portfolio> {
  const { data } = await apiClient.get<Portfolio | unknown>("/api/portfolio")
  if (!data || !Array.isArray((data as Portfolio).projects)) {
    throw new PortfolioShapeError()
  }
  return data as Portfolio
}
