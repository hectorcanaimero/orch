import axios from "axios"
import type { ProjectConfig, TunnelCapabilities, TunnelStatus } from "@/lib/types"

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
