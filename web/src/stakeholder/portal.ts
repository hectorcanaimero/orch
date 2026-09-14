import type { StakeholderDelivery, StakeholderMilestone, StakeholderSnapshot } from "./types"

export type Lang = "es" | "en"

export function langOf(snapshot: StakeholderSnapshot): Lang {
  return snapshot.executive_summary.language === "en" ? "en" : "es"
}

// The words the portal uses. Kept here, beside the only code that picks
// between them, rather than in a framework: two languages, one page.
export const copy = {
  es: {
    summary: "Resumen",
    roadmap: "Hoja de ruta",
    progress: (done: number, total: number) => `${done} de ${total} entregables`,
    since: "Desde tu última visita",
    sinceFirst: "Entregado esta semana",
    nothingNew: "Nada nuevo desde tu última visita.",
    onHold: "En espera",
    now: "Ahora",
    next: "Después",
    done: "Entregado",
    inProgress: "En curso",
    blocked: "En espera",
    pending: "Pendiente",
    updated: (rel: string) => `Actualizado ${rel}`,
    other: "Otros entregables",
    budget: "Presupuesto",
    spent: "Consumido",
    loadError: "No pudimos cargar el estado del proyecto.",
    loading: "Cargando…",
    showAll: (n: number) => `Ver las ${n} entregas`,
    showLess: "Ver menos",
  },
  en: {
    summary: "Overview",
    roadmap: "Roadmap",
    progress: (done: number, total: number) => `${done} of ${total} deliverables`,
    since: "Since your last visit",
    sinceFirst: "Delivered this week",
    nothingNew: "Nothing new since your last visit.",
    onHold: "On hold",
    now: "Now",
    next: "Next",
    done: "Delivered",
    inProgress: "In progress",
    blocked: "On hold",
    pending: "Not started",
    updated: (rel: string) => `Updated ${rel}`,
    other: "Other deliverables",
    budget: "Budget",
    spent: "Spent",
    loadError: "We couldn't load the project status.",
    loading: "Loading…",
    showAll: (n: number) => `Show all ${n} deliveries`,
    showLess: "Show less",
  },
} as const

const MONTHS: Record<Lang, string[]> = {
  es: ["enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"],
  en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
}

// formatDay renders an ISO day as "16 de septiembre" / "September 16", in
// UTC so the day never shifts with the reader's timezone.
export function formatDay(iso: string, lang: Lang): string {
  const d = new Date(iso.length === 10 ? `${iso}T00:00:00Z` : iso)
  if (Number.isNaN(d.getTime())) return iso
  const month = MONTHS[lang][d.getUTCMonth()]
  return lang === "es" ? `${d.getUTCDate()} de ${month}` : `${month} ${d.getUTCDate()}`
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
  const { summary } = s
  if (summary.total > 0 && summary.done === summary.total) {
    return { headline: lang === "es" ? "Todo lo planificado está entregado." : "Everything planned has been delivered.", detail: "" }
  }
  let headline: string
  if (summary.eta_date) {
    const day = formatDay(summary.eta_date, lang)
    const early = summary.eta_confidence === "low"
    headline =
      lang === "es"
        ? `Estimamos terminar el ${day}${early ? " (estimación preliminar)" : ""}.`
        : `We expect to finish around ${day}${early ? " (early estimate)" : ""}.`
  } else {
    headline = lang === "es" ? "El trabajo está en curso." : "Work is in progress."
  }

  const parts: string[] = []
  const cur = currentPhase(s.milestones)
  if (cur) parts.push(`${lang === "es" ? "Ahora" : "Now"}: ${phaseName(cur.name)}.`)
  if (summary.blocked > 0) {
    const n = summary.blocked
    parts.push(lang === "es" ? `${n} ${n === 1 ? "tema" : "temas"} en espera.` : `${n} ${n === 1 ? "item" : "items"} on hold.`)
  }
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
