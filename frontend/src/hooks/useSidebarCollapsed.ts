import { useCallback, useEffect, useState } from "react"

const STORAGE_KEY = "orch_sidebar_collapsed"
const MD_BREAKPOINT = "(min-width: 768px)"

function readStored(): boolean {
  if (typeof window === "undefined") return false
  try {
    return window.localStorage.getItem(STORAGE_KEY) === "1"
  } catch {
    return false
  }
}

function writeStored(value: boolean) {
  if (typeof window === "undefined") return
  try {
    window.localStorage.setItem(STORAGE_KEY, value ? "1" : "0")
  } catch {
    // ignore quota / disabled storage
  }
}

function isWide(): boolean {
  if (typeof window === "undefined" || !window.matchMedia) return true
  return window.matchMedia(MD_BREAKPOINT).matches
}

/**
 * Persisted collapsed state for the app sidebar.
 *
 * - Reads localStorage synchronously during first render (no flicker).
 * - On viewports below `md`, forces collapsed and does NOT persist that value
 *   — the persisted preference takes over again once the viewport grows.
 * - Returns [collapsed, setCollapsed, toggle].
 */
export function useSidebarCollapsed(): [
  collapsed: boolean,
  setCollapsed: (v: boolean) => void,
  toggle: () => void,
] {
  const [stored, setStored] = useState<boolean>(() => readStored())
  const [wide, setWide] = useState<boolean>(() => isWide())

  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return
    const mql = window.matchMedia(MD_BREAKPOINT)
    const onChange = (e: MediaQueryListEvent) => setWide(e.matches)
    // addEventListener is the modern API; addListener is the fallback.
    if (typeof mql.addEventListener === "function") {
      mql.addEventListener("change", onChange)
      return () => mql.removeEventListener("change", onChange)
    }
    mql.addListener(onChange)
    return () => mql.removeListener(onChange)
  }, [])

  const setCollapsed = useCallback((value: boolean) => {
    setStored(value)
    writeStored(value)
  }, [])

  const toggle = useCallback(() => {
    setStored((prev) => {
      const next = !prev
      writeStored(next)
      return next
    })
  }, [])

  // Below md, always collapsed regardless of stored preference — but the
  // stored value is untouched so it reappears when the viewport is wide again.
  const collapsed = wide ? stored : true

  return [collapsed, setCollapsed, toggle]
}
