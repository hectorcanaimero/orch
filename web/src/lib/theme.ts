import { useCallback, useSyncExternalStore } from "react"

// "dark" is the default: the dashboard is a console watched for hours next to
// a terminal. "system" follows prefers-color-scheme for operators who want it.
export type ThemeChoice = "dark" | "light" | "system"

const STORAGE_KEY = "orch_theme"
const MEDIA = "(prefers-color-scheme: dark)"
const listeners = new Set<() => void>()

function readChoice(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(STORAGE_KEY)
    if (v === "light" || v === "system" || v === "dark") return v
  } catch {
    // storage blocked: fall through to the default
  }
  return "dark"
}

function resolve(choice: ThemeChoice): "dark" | "light" {
  if (choice !== "system") return choice
  return window.matchMedia?.(MEDIA).matches ? "dark" : "light"
}

function paint(choice: ThemeChoice) {
  const dark = resolve(choice) === "dark"
  document.documentElement.classList.toggle("dark", dark)
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", dark ? "#101116" : "#f6f6f9")
}

/** Runs once before the first render so the page never flashes the other theme. */
export function initTheme() {
  paint(readChoice())
  window.matchMedia?.(MEDIA).addEventListener?.("change", () => {
    if (readChoice() === "system") paint("system")
  })
  // Paper is white: print with the light tokens, then put the choice back.
  window.addEventListener("beforeprint", () => document.documentElement.classList.remove("dark"))
  window.addEventListener("afterprint", () => paint(readChoice()))
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function subscribeResolved(listener: () => void) {
  const observer = new MutationObserver(listener)
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] })
  return () => observer.disconnect()
}

/** Whether the dark tokens are the ones painted right now, whatever the choice was. */
export function useIsDark(): boolean {
  return useSyncExternalStore(
    subscribeResolved,
    () => document.documentElement.classList.contains("dark"),
    () => true,
  )
}

export function useTheme():[ThemeChoice, (next: ThemeChoice) => void] {
  const choice = useSyncExternalStore(subscribe, readChoice, () => "dark" as ThemeChoice)
  const setChoice = useCallback((next: ThemeChoice) => {
    try {
      window.localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // not persisted; still applied for this page view
    }
    paint(next)
    listeners.forEach((l) => l())
  }, [])
  return [choice, setChoice]
}
