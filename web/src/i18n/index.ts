import { en } from "./en"

// One dictionary today; es and pt arrive as siblings of en.ts typed against
// its keys, so a missing translation fails `tsc` instead of rendering a key.
export type Plural = { one: string; other: string }
export type MessageKey = keyof typeof en
type Vars = Record<string, string | number>

export const locale = "en"
const plurals = new Intl.PluralRules(locale)

/** Look a message up; `{name}` placeholders take `vars`, plural messages pick by `vars.count`. */
export function t(key: MessageKey, vars?: Vars): string {
  const entry: string | Plural = en[key]
  let text: string =
    typeof entry === "string"
      ? entry
      : plurals.select(Number(vars?.count ?? 0)) === "one"
        ? entry.one
        : entry.other
  if (vars) {
    for (const [name, value] of Object.entries(vars)) text = text.replaceAll(`{${name}}`, String(value))
  }
  return text
}

/** Hook form, so components already read strings the way a locale switch will need. */
export function useT() {
  return t
}

const numberFmt = new Intl.NumberFormat(locale)
const usdFmt = new Intl.NumberFormat(locale, {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

export const fmt = {
  number: (n: number) => numberFmt.format(n),
  usd: (n: number) => usdFmt.format(n),
  /** `YYYY-MM-DD` (a UTC calendar day) or an ISO timestamp → "Sep 18". */
  day: (iso: string) => {
    const dayOnly = iso.length === 10
    const d = new Date(dayOnly ? `${iso}T00:00:00Z` : iso)
    if (Number.isNaN(d.getTime())) return iso
    return d.toLocaleDateString(locale, { month: "short", day: "numeric", timeZone: dayOnly ? "UTC" : undefined })
  },
  dateTime: (iso: string) => {
    const d = new Date(iso)
    if (!iso || Number.isNaN(d.getTime())) return "—"
    return d.toLocaleString(locale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
  },
  time: (d: Date) => d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" }),
}
