import { useCallback, useSyncExternalStore } from "react"

const TOKEN_KEY = "orch_token"

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
