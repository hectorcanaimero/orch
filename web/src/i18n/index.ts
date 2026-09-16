import { useSyncExternalStore } from "react"
import { en } from "./en"
import { es } from "./es"
import { pt } from "./pt"

// en is the source; es and pt are typed against its keys, so a missing or
// extra translation fails `tsc` instead of rendering a key.
export type Plural = { one: string; other: string }
export type MessageKey = keyof typeof en
export type Dictionary = Record<MessageKey, string | Plural>
type Vars = Record<string, string | number>

export type Lang = "en" | "es" | "pt"
export const LANGS: readonly Lang[] = ["en", "es", "pt"]
const DICTIONARIES: Record<Lang, Dictionary> = { en, es, pt }
const LOCALES: Record<Lang, string> = { en: "en-US", es: "es", pt: "pt-BR" }
/** Each language named in itself, for the selector. */
export const LANGUAGE_NAMES: Record<Lang, string> = { en: "English", es: "Español", pt: "Português" }

const STORAGE_KEY = "orch_lang"
const listeners = new Set<() => void>()

/**
 * The operator's language: a choice saved in this browser, else the browser's
 * own languages, else English. dashboard.language is deliberately not read:
 * it is what the CLIENT reads (portal, summary, PDF) and defaults to es.
 */
export function resolveLanguage(stored: string | null, browser: readonly string[]): Lang {
  if (stored === "en" || stored === "es" || stored === "pt") return stored
  for (const tag of browser) {
    const base = tag.toLowerCase().split(/[-_]/)[0]
    if (base === "en" || base === "es" || base === "pt") return base
  }
  return "en"
}

function readStored(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

let current: Lang = resolveLanguage(
  typeof window === "undefined" ? null : readStored(),
  typeof navigator === "undefined" ? [] : (navigator.languages ?? [navigator.language]),
)
if (typeof document !== "undefined") document.documentElement.lang = LOCALES[current]

export function getLanguage(): Lang {
  return current
}

/** The BCP 47 locale every Intl formatter uses for the current language. */
export function getLocale(): string {
  return LOCALES[current]
}

/** Switch language now, remember it in this browser, and re-render subscribers. */
export function setLanguage(lang: Lang) {
  try {
    window.localStorage.setItem(STORAGE_KEY, lang)
  } catch {
    // storage blocked: the choice lasts until reload
  }
  if (lang === current) return
  current = lang
  document.documentElement.lang = LOCALES[lang]
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function useLanguage(): Lang {
  return useSyncExternalStore(subscribe, getLanguage, getLanguage)
}

// Formatters are cheap to keep and costly to rebuild on every render.
const cache = new Map<string, Intl.PluralRules | Intl.NumberFormat>()
function cached<T extends Intl.PluralRules | Intl.NumberFormat>(key: string, make: (loc: string) => T): T {
  const k = `${getLocale()}|${key}`
  let f = cache.get(k) as T | undefined
  if (!f) {
    f = make(getLocale())
    cache.set(k, f)
  }
  return f
}

/** Look a message up; `{name}` placeholders take `vars`, plural messages pick by `vars.count`. */
export function t(key: MessageKey, vars?: Vars): string {
  const entry: string | Plural | undefined = DICTIONARIES[current][key] ?? en[key]
  // A key missing from the dictionary shows itself rather than crashing the page.
  if (entry === undefined) return key
  let text: string =
    typeof entry === "string"
      ? entry
      : cached("plural", (loc) => new Intl.PluralRules(loc)).select(Number(vars?.count ?? 0)) === "one"
        ? entry.one
        : entry.other
  if (vars) {
    for (const [name, value] of Object.entries(vars)) text = text.replaceAll(`{${name}}`, String(value))
  }
  return text
}

/** Hook form: re-renders its component when the language changes. */
export function useT() {
  useLanguage()
  return t
}

export const fmt = {
  number: (n: number) => cached("number", (loc) => new Intl.NumberFormat(loc)).format(n),
  usd: (n: number) =>
    cached(
      "usd",
      (loc) =>
        new Intl.NumberFormat(loc, { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 }),
    ).format(n),
  /** `YYYY-MM-DD` (a UTC calendar day) or an ISO timestamp → "Sep 18". */
  day: (iso: string) => {
    const dayOnly = iso.length === 10
    const d = new Date(dayOnly ? `${iso}T00:00:00Z` : iso)
    if (Number.isNaN(d.getTime())) return iso
    return d.toLocaleDateString(getLocale(), { month: "short", day: "numeric", timeZone: dayOnly ? "UTC" : undefined })
  },
  dateTime: (iso: string) => {
    const d = new Date(iso)
    if (!iso || Number.isNaN(d.getTime())) return "—"
    return d.toLocaleString(getLocale(), { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
  },
  time: (d: Date) => d.toLocaleTimeString(getLocale(), { hour: "2-digit", minute: "2-digit", second: "2-digit" }),
}
