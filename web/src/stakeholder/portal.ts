import DOMPurify from "dompurify"
import { marked } from "marked"
import type { StakeholderDelivery, StakeholderMilestone, StakeholderQuality, StakeholderSnapshot } from "./types"

import { copy, LOCALES, type Lang } from "./i18n"

export { copy, readerLang, type Lang } from "./i18n"

// langOf is the snapshot's language. One the portal cannot write is shown in
// English and said out loud in the console, instead of silently becoming
// Spanish.
export function langOf(snapshot: StakeholderSnapshot): Lang {
  const lang = snapshot.executive_summary.language
  if (lang === "en" || lang === "es" || lang === "pt") return lang
  console.warn(`orch portal: no translation for language ${JSON.stringify(lang)}; showing English`)
  return "en"
}

// formatDay renders an ISO day as "September 16" / "16 de septiembre" /
// "16 de setembro", in UTC so the day never shifts with the reader's timezone.
export function formatDay(iso: string, lang: Lang): string {
  const d = new Date(iso.length === 10 ? `${iso}T00:00:00Z` : iso)
  if (Number.isNaN(d.getTime())) return iso
  return new Intl.DateTimeFormat(LOCALES[lang], { day: "numeric", month: "long", timeZone: "UTC" }).format(d)
}

// relativeTime says how long ago an instant was ("5 minutes ago", "hace 3
// horas", "ontem"). An instant that does not parse is returned as given.
export function relativeTime(iso: string, lang: Lang, now: number = Date.now()): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return iso
  const rtf = new Intl.RelativeTimeFormat(LOCALES[lang], { numeric: "auto" })
  const minutes = Math.round((then - now) / 60_000)
  if (Math.abs(minutes) < 60) return rtf.format(minutes, "minute")
  const hours = Math.round(minutes / 60)
  if (Math.abs(hours) < 24) return rtf.format(hours, "hour")
  return rtf.format(Math.round(hours / 24), "day")
}

// phaseName drops a leading "F6 — " a tasks.json name may carry: the phase
// number is the plan's bookkeeping, not what a client reads.
export function phaseName(name: string): string {
  return name.replace(/^F\d+\s*[—–-]\s*/, "") || name
}

export function currentPhase(milestones: StakeholderMilestone[]): StakeholderMilestone | undefined {
  return [...milestones].sort((a, b) => a.phase - b.phase).find((m) => !m.complete)
}

export type PhaseState = "done" | "now" | "next"

export function phaseState(m: StakeholderMilestone, current: StakeholderMilestone | undefined): PhaseState {
  if (m.complete) return "done"
  return current && m.phase === current.phase ? "now" : "next"
}

// statusLine is the sentence a client reads first: when it finishes, then
// where the work is and what waits.
export function statusLine(s: StakeholderSnapshot, lang: Lang): { headline: string; detail: string } {
  const t = copy[lang]
  const { summary } = s
  if (summary.total > 0 && summary.done === summary.total) {
    return { headline: t.allDelivered, detail: "" }
  }
  const headline = summary.eta_date ? t.finish(formatDay(summary.eta_date, lang), summary.eta_confidence === "low") : t.working

  const parts: string[] = []
  const cur = currentPhase(s.milestones)
  if (cur) parts.push(t.nowPhase(phaseName(cur.name)))
  if (summary.blocked > 0) parts.push(t.itemsOnHold(summary.blocked))
  return { headline, detail: parts.join(" ") }
}

const WEEK_MS = 7 * 24 * 60 * 60 * 1000

// deliveriesSince is what arrived after the reader's last visit, or in the
// last seven days on a first visit.
export function deliveriesSince(
  deliveries: StakeholderDelivery[] | undefined,
  lastVisit: string | null,
  now: Date,
): StakeholderDelivery[] {
  const since = lastVisit ? Date.parse(lastVisit) : now.getTime() - WEEK_MS
  return (deliveries ?? []).filter((d) => Date.parse(d.finished_at) > since)
}

// The reader's previous visit, remembered per project in this browser only.
// Storage can be unavailable (private windows, file://, blocked site data):
// then every visit is a first visit, which is still a correct page.
export function readAndRecordVisit(project: string, now: Date): string | null {
  const key = `orch-portal:last-visit:${project}`
  try {
    const previous = window.localStorage.getItem(key)
    window.localStorage.setItem(key, now.toISOString())
    return previous
  } catch {
    return null
  }
}

export type Tab = "overview" | "roadmap" | "documents"

// parseRoute reads the page's hash: a tab, and on Documents the open one.
// Hash routes keep a published folder working from any static host or file.
export function parseRoute(hash: string): { tab: Tab; doc?: string } {
  const [head, doc] = hash.replace(/^#/, "").split("/", 2)
  if (head === "roadmap") return { tab: "roadmap" }
  if (head === "documents") return doc ? { tab: "documents", doc } : { tab: "documents" }
  return { tab: "overview" }
}

// renderMarkdown turns a shared document into HTML that is safe to insert:
// the markdown is the operator's, but the page is a client's, so nothing
// executable survives (scripts, event handlers, javascript: links).
export function renderMarkdown(markdown: string): string {
  const html = marked.parse(markdown, { async: false }) as string
  return DOMPurify.sanitize(html)
}

// snapshotURL is where the page reads its data: the file beside it, with a
// token forwarded when there is one (a live dashboard gates
// /stakeholder/data.json with it; a published folder ignores it). The link's
// own token wins over the one this browser saved.
export function snapshotURL(search: string, saved: string | null): string {
  const token = new URLSearchParams(search).get("token") || saved
  return token ? `./data.json?token=${encodeURIComponent(token)}` : "./data.json"
}

// The key the operator dashboard keeps its token under (hooks/useAuth.ts).
// Sharing it lets a session opened by the v0.12 dashboard, which moved the
// token out of the address bar, keep working after the upgrade (#274).
const TOKEN_KEY = "orch_token"

// rememberLinkToken saves the link's token for visits without it, and
// returns the one saved. Storage can be unavailable; then only the link works.
export function rememberLinkToken(search: string): string | null {
  const fromLink = new URLSearchParams(search).get("token")
  try {
    if (fromLink) window.localStorage.setItem(TOKEN_KEY, fromLink)
    return window.localStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

// qualityLine says how delivered work fared against the checks, or "" while
// nothing has been delivered through them yet (the list of checks still
// shows what every delivery will go through).
export function qualityLine(q: StakeholderQuality | undefined, lang: Lang): string {
  if (!q || q.delivered === 0) return ""
  return copy[lang].verified(q.verified, q.delivered, q.first_pass)
}
