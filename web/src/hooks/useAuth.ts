import { useCallback, useSyncExternalStore } from "react"

export const TOKEN_KEY = "orch_token"

type Listener = () => void
const listeners = new Set<Listener>()

function emit() {
  listeners.forEach((l) => l())
}

function subscribe(listener: Listener) {
  listeners.add(listener)
  const onStorage = (e: StorageEvent) => {
    if (e.key === TOKEN_KEY) listener()
  }
  window.addEventListener("storage", onStorage)
  return () => {
    listeners.delete(listener)
    window.removeEventListener("storage", onStorage)
  }
}

function getSnapshot(): string | null {
  if (typeof window === "undefined") return null
  return window.localStorage.getItem(TOKEN_KEY)
}

function getServerSnapshot(): string | null {
  return null
}

/**
 * Adopt a `?token=<value>` query param into localStorage, then scrub it from
 * the address bar with `history.replaceState`.
 *
 * This is how the "send the client one URL" flow actually works: the operator
 * shares `https://<tunnel>/?token=s3cr3t`, the browser loads the shell (which
 * the server serves without a token — see `_is_public_shell` in
 * `orchestrator/dashboard/middleware.py`), and this runs before the first
 * render so the app boots authenticated instead of bouncing to /login.
 *
 * The token is removed from the URL afterwards so it does not linger in the
 * address bar, in `document.referrer`, or in a screenshot of the tab. It is
 * already in localStorage at that point, which is where the API client reads
 * it from.
 *
 * Must be called once, before `createRoot(...).render(...)`.
 */
export function adoptTokenFromQuery(): void {
  if (typeof window === "undefined") return
  const url = new URL(window.location.href)
  const fromQuery = url.searchParams.get("token")?.trim()
  if (!fromQuery) return
  try {
    window.localStorage.setItem(TOKEN_KEY, fromQuery)
  } catch {
    // Private mode / storage disabled: leave the URL alone so the token is
    // still carried on a manual reload rather than silently lost.
    return
  }
  url.searchParams.delete("token")
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`)
  emit()
}


export function useAuth() {
  const token = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)

  const setToken = useCallback((next: string) => {
    window.localStorage.setItem(TOKEN_KEY, next)
    emit()
  }, [])

  const clearToken = useCallback(() => {
    window.localStorage.removeItem(TOKEN_KEY)
    emit()
  }, [])

  return {
    token,
    setToken,
    clearToken,
    isAuthenticated: Boolean(token),
  }
}
