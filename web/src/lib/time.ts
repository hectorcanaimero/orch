import { locale } from "@/i18n"

/** 0:37, 6:41, 1:02:09 — a running clock, not a sentence. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const pad = (n: number) => String(n).padStart(2, "0")
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

const relative = new Intl.RelativeTimeFormat(locale, { numeric: "auto" })

/** "3 hours ago", "in 2 hours" — the unit that keeps the number small. */
export function formatRelative(iso: string, now: number): string {
  const at = new Date(iso).getTime()
  if (Number.isNaN(at)) return "—"
  const diff = (at - now) / 1000
  const abs = Math.abs(diff)
  if (abs < 60) return relative.format(Math.round(diff), "second")
  if (abs < 3600) return relative.format(Math.round(diff / 60), "minute")
  if (abs < 86_400) return relative.format(Math.round(diff / 3600), "hour")
  return relative.format(Math.round(diff / 86_400), "day")
}
