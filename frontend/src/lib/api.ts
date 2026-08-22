import axios from "axios"
import type {
  ArchitectureHistory,
  ArchitectureRegenerateResponse,
  ArchitectureStatus,
  DoctorReport,
  ProjectConfig,
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
