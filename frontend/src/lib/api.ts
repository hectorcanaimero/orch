import axios from "axios"
import type {
  ArchitectureHistory,
  ArchitectureRegenerateResponse,
  ArchitectureStatus,
  DoctorReport,
  ProjectConfig,
  TunnelCapabilities,
  TunnelConflictError,
  TunnelStartResponse,
  TunnelStatus,
  TunnelStopResponse,
} from "@/lib/types"

const TOKEN_KEY = "orch_token"

export const API_BASE_URL =
  import.meta.env.VITE_API_BASE_URL || "http://127.0.0.1:7420"

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

export async function getDoctorReport(): Promise<DoctorReport> {
  const { data } = await apiClient.get<DoctorReport>("/api/doctor")
  return data
}

export async function getArchitectureStatus(): Promise<ArchitectureStatus> {
  const { data } = await apiClient.get<ArchitectureStatus>(
    "/api/architecture/status",
  )
  return data
}

export async function getArchitectureHistory(): Promise<ArchitectureHistory> {
  const { data } = await apiClient.get<ArchitectureHistory>(
    "/api/architecture/history",
  )
  return data
}

/**
 * Error thrown when a regenerate is requested while one is already running.
 *
 * The backend returns HTTP 409 in that case — we surface a typed error so the
 * mutation consumer can render a friendly inline/toast message without having
 * to peel apart the axios error shape.
 */
export class ArchitectureAlreadyGeneratingError extends Error {
  constructor() {
    super("Architecture regeneration is already in progress")
    this.name = "ArchitectureAlreadyGeneratingError"
  }
}

export async function regenerateArchitecture(): Promise<ArchitectureRegenerateResponse> {
  try {
    const { data } = await apiClient.post<ArchitectureRegenerateResponse>(
      "/api/architecture/regenerate",
    )
    return data
  } catch (err) {
    const status = (err as { response?: { status?: number } })?.response?.status
    if (status === 409) throw new ArchitectureAlreadyGeneratingError()
    throw err
  }
}

/**
 * Build a URL suitable for an <iframe src> that fetches architecture HTML.
 *
 * Iframes can't send Authorization headers on their initial navigation, so
 * when a bearer token is present we forward it via the `?token=` query param.
 * The backend's TokenAuthMiddleware already accepts that alternative form
 * (see `_extract_token` in orchestrator/dashboard/middleware.py).
 */
export function buildArchitectureIframeUrl(
  variant: "current" | { snapshot: string },
): string {
  const path =
    variant === "current"
      ? "/api/architecture/current"
      : `/api/architecture/snapshot/${encodeURIComponent(variant.snapshot)}`
  const token = getToken()
  const url = new URL(path, API_BASE_URL)
  if (token) url.searchParams.set("token", token)
  return url.toString()
}

/**
 * Sprint E-5 — Tunnel manager API surface.
 *
 * `/capabilities` is intentionally auth-free on the backend (always 200) so
 * we surface it via `apiClient` without special-casing 401/403 — the axios
 * interceptor still won't fire because the endpoint never returns those.
 *
 * `/start` and `/stop` return 202 on success and 409 on conflict; the
 * `TunnelConflictError` union captures the three distinct 409 payloads the
 * panel needs to render distinct toasts for (`already_running` / `locked` /
 * `not_running`).
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
 * Thrown when POST /start or /stop returns 409. Carries the parsed body so
 * the panel can distinguish `already_running` vs `locked` vs `not_running`
 * without having to peel apart the axios error shape.
 */
export class TunnelConflictHttpError extends Error {
  readonly body: TunnelConflictError
  constructor(body: TunnelConflictError) {
    super(`tunnel conflict: ${body.error}`)
    this.name = "TunnelConflictHttpError"
    this.body = body
  }
}

async function postTunnelAction<T>(path: string): Promise<T> {
  try {
    const { data } = await apiClient.post<T>(path)
    return data
  } catch (err) {
    const response = (err as { response?: { status?: number; data?: unknown } })
      ?.response
    if (response?.status === 409) {
      throw new TunnelConflictHttpError(response.data as TunnelConflictError)
    }
    throw err
  }
}

export function startTunnel(): Promise<TunnelStartResponse> {
  return postTunnelAction<TunnelStartResponse>("/api/tunnel/start")
}

export function stopTunnel(): Promise<TunnelStopResponse> {
  return postTunnelAction<TunnelStopResponse>("/api/tunnel/stop")
}

/**
 * SSE-safe URL for the tunnel log stream. Mirrors `buildArchitectureIframeUrl`
 * — EventSource can't send headers, so we forward the bearer as `?token=`
 * (accepted by `TokenAuthMiddleware._extract_token`).
 */
export function buildTunnelLogsUrl(): string {
  const token = getToken()
  const url = new URL("/api/tunnel/logs", API_BASE_URL)
  if (token) url.searchParams.set("token", token)
  return url.toString()
}
