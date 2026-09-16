import { useEffect, useRef } from "react"
import { useLocation, useNavigate, type NavigateFunction } from "react-router-dom"
import { t } from "@/i18n"
import type { NavDestination } from "@/lib/nav"

// The pages whose task list reads the filters in the query string.
export const FILTERED_PAGES = ["/work/list", "/work/kanban"]

export function isMac(): boolean {
  return typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
}

export const GO_KEYS: Record<string, NavDestination["id"]> = { n: "now", w: "work", c: "cost", d: "delivery" }
const G_WINDOW_MS = 1500

/** Keys pressed while someone is typing belong to the field, never to a shortcut. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)
}

function focusTaskSearch(navigate: NavigateFunction, pathname: string, search: string) {
  if (!FILTERED_PAGES.includes(pathname)) navigate(`/work/list${search}`)
  // The list renders after navigation; look for its search field for up to a second.
  let tries = 0
  const attempt = () => {
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t("filters.search")}"]`)
    if (input) {
      input.focus()
      input.select()
    } else if (++tries < 20) {
      window.setTimeout(attempt, 50)
    }
  }
  attempt()
}

export interface ShortcutHandlers {
  destinations: NavDestination[]
  onPalette: () => void
  onShortcuts: () => void
}

/**
 * ⌘K / Ctrl+K anywhere; and, when nobody is typing and no dialog is open:
 * `g` then n/w/c/d to change destination, `/` to search tasks, `?` for help.
 */
export function useKeyboardShortcuts({ destinations, onPalette, onShortcuts }: ShortcutHandlers) {
  const navigate = useNavigate()
  const { pathname, search } = useLocation()
  const latest = useRef({ destinations, onPalette, onShortcuts, navigate, pathname, search })
  useEffect(() => {
    latest.current = { destinations, onPalette, onShortcuts, navigate, pathname, search }
  })

  useEffect(() => {
    let gPressedAt = 0
    const onKey = (e: KeyboardEvent) => {
      const h = latest.current
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault()
        h.onPalette()
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey || isTypingTarget(e.target) || document.querySelector('[aria-modal="true"]')) {
        gPressedAt = 0
        return
      }
      if (gPressedAt && Date.now() - gPressedAt < G_WINDOW_MS) {
        gPressedAt = 0
        const destination = h.destinations.find((d) => d.id === GO_KEYS[e.key])
        if (destination) {
          e.preventDefault()
          h.navigate(destination.tabs[0].to)
        }
        return
      }
      gPressedAt = 0
      if (e.key === "g") {
        gPressedAt = Date.now()
      } else if (e.key === "?") {
        e.preventDefault()
        h.onShortcuts()
      } else if (e.key === "/" && h.destinations.some((d) => d.tabs.some((tab) => tab.to === "/work/list"))) {
        e.preventDefault()
        focusTaskSearch(h.navigate, h.pathname, h.search)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
}
