import { getLocale } from "@/i18n"

/** 0:37, 6:41, 1:02:09 — a running clock, not a sentence. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const pad = (n: number) => String(n).padStart(2, "0")
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

/** 2d 3h, 2h 5m, 50m, 40s — the same spelling `orch report receipt` prints. */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  if (s >= 86_400) return `${Math.floor(s / 86_400)}d ${Math.floor((s % 86_400) / 3600)}h`
  if (s >= 3600) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  if (s >= 60) return `${Math.floor(s / 60)}m`
  return `${s}s`
}

const relatives = new Map<string, Intl.RelativeTimeFormat>()
function relativeFormat(): Intl.RelativeTimeFormat {
  const loc = getLocale()
  let f = relatives.get(loc)
  if (!f) relatives.set(loc, (f = new Intl.RelativeTimeFormat(loc, { numeric: "auto" })))
  return f
}

/** "3 hours ago", "in 2 hours" — the unit that keeps the number small. */
export function formatRelative(iso: string, now: number): string {
  const at = new Date(iso).getTime()
  if (Number.isNaN(at)) return "—"
  const diff = (at - now) / 1000
  const abs = Math.abs(diff)
  const relative = relativeFormat()
  if (abs < 60) return relative.format(Math.round(diff), "second")
  if (abs < 3600) return relative.format(Math.round(diff / 60), "minute")
  if (abs < 86_400) return relative.format(Math.round(diff / 3600), "hour")
  return relative.format(Math.round(diff / 86_400), "day")
}
