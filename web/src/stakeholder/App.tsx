import { useEffect, useMemo, useState } from "react"
import { ArrowLeft, ChevronRight, Circle, CircleCheck, CircleDot, CirclePause, FileText, TriangleAlert } from "lucide-react"
import {
  copy,
  currentPhase,
  deliveriesSince,
  formatDay,
  langOf,
  parseRoute,
  phaseName,
  snapshotURL,
  phaseState,
  readAndRecordVisit,
  rememberLinkToken,
  renderMarkdown,
  statusLine,
  type Lang,
  type PhaseState,
  type Tab,
} from "./portal"
import "./portal.css"
import type { DeliverableStatus, StakeholderDocument, StakeholderMilestone, StakeholderSnapshot } from "./types"

declare global {
  interface Window {
    // Set by the `data.js` sibling `orch publish` writes next to this HTML,
    // wrapping the same document as `data.json`. Read first because a
    // <script> tag works under file://, where fetch('./data.json') does not.
    __ORCH_SNAPSHOT__?: StakeholderSnapshot
  }
}

/**
 * Client portal — Phase B of the web review.
 *
 * What a client opens: where the project stands in a sentence, what arrived
 * since their last visit, and the roadmap by name. No API, no login, no
 * polling: it reads the snapshot `orch publish` writes beside it
 * (docs/SNAPSHOT-SCHEMA.md). Never task ids, models, tokens or file paths.
 */

type LoadState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: StakeholderSnapshot }

function currentRoute() {
  return parseRoute(typeof window !== "undefined" ? window.location.hash : "")
}

export default function App() {
  const [state, setState] = useState<LoadState>({ status: "loading" })

  useEffect(() => {
    if (window.__ORCH_SNAPSHOT__) {
      setState({ status: "ready", data: window.__ORCH_SNAPSHOT__ })
      return
    }
    let cancelled = false
    let timer: number | undefined
    const url = snapshotURL(window.location.search, rememberLinkToken(window.location.search))
    const load = () =>
      fetch(url, { cache: "no-store" })
        .then((res) => {
          if (res.status === 401 || res.status === 403) throw new LinkError()
          if (!res.ok) throw new Error(`HTTP ${res.status}`)
          return res.json()
        })
        .then((data: StakeholderSnapshot) => {
          if (cancelled) return
          setState({ status: "ready", data })
          // Served live (orch dashboard), the snapshot says how often to
          // look again; a published folder says 0 and is read once.
          if (data.refresh_interval_s > 0 && window.location.protocol.startsWith("http")) {
            timer = window.setTimeout(load, data.refresh_interval_s * 1000)
          }
        })
        .catch((err: unknown) => {
          if (cancelled) return
          // A refresh that fails keeps the last good page on screen.
          setState((prev) => (prev.status === "ready" && !(err instanceof LinkError) ? prev : { status: "error", message: loadMessage(err) }))
        })
    load()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [])

  if (state.status !== "ready") {
    return (
      <div className="portal">
        <main className="mx-auto max-w-[720px] px-5 py-16">
          {state.status === "loading" ? (
            <p className="text-[var(--portal-muted)]">Loading…</p>
          ) : (
            <div role="alert" className="flex gap-3 rounded-xl bg-[var(--portal-hold-soft)] p-5 text-[var(--portal-ink)]">
              <TriangleAlert className="mt-1 h-5 w-5 shrink-0 text-[var(--portal-hold)]" aria-hidden />
              <p>{state.message}</p>
            </div>
          )}
        </main>
      </div>
    )
  }
  return <Portal data={state.data} />
}

class LinkError extends Error {}

// loadMessage says what went wrong in words a client can act on. The page has
// no project language yet, so it answers in the reader's own.
function loadMessage(err: unknown): string {
  const es = typeof navigator !== "undefined" && navigator.language.toLowerCase().startsWith("es")
  if (err instanceof LinkError) {
    return es
      ? "Este enlace ya no es válido. Pide al equipo un enlace nuevo."
      : "This link is no longer valid. Ask the team for a new one."
  }
  if (err instanceof TypeError && window.location.protocol === "file:") {
    return "Your browser blocks a local file from reading another local file. Serve this folder over HTTP (for example `python3 -m http.server`) and open it from there."
  }
  return es
    ? "No pudimos cargar el estado del proyecto. Vuelve a intentarlo en un momento."
    : "We couldn't load the project status. Try again in a moment."
}

function Portal({ data }: { data: StakeholderSnapshot }) {
  const lang = langOf(data)
  const t = copy[lang]
  const [route, setRoute] = useState(currentRoute)
  // Read once per page load: the previous visit is what "since" means.
  const [lastVisit] = useState(() => readAndRecordVisit(data.project_name, new Date()))

  useEffect(() => {
    const onHash = () => {
      setRoute(currentRoute())
      window.scrollTo(0, 0)
    }
    window.addEventListener("hashchange", onHash)
    return () => window.removeEventListener("hashchange", onHash)
  }, [])

  const accent = data.branding?.accent_color
  const name = data.branding?.name?.trim() || data.project_name
  const documents = data.documents ?? []
  const tabs: { id: Tab; label: string }[] = [
    { id: "overview", label: t.summary },
    { id: "roadmap", label: t.roadmap },
    ...(documents.length > 0 ? [{ id: "documents" as Tab, label: t.documents }] : []),
  ]
  const tab = route.tab === "documents" && documents.length === 0 ? "overview" : route.tab

  return (
    <div className="portal" lang={lang} style={accent ? ({ "--portal-accent": accent } as React.CSSProperties) : undefined}>
      <header className="border-b border-[var(--portal-line)] bg-[var(--portal-surface)]">
        <div className="mx-auto flex max-w-[720px] items-center justify-between gap-4 px-5 pt-5">
          <div className="flex min-w-0 items-center gap-3">
            {data.branding?.logo ? <img src={data.branding.logo} alt="" className="h-9 w-auto max-w-[160px] object-contain" /> : null}
            <h1 className="portal-display truncate text-xl font-semibold">{name}</h1>
          </div>
          <span className="shrink-0 text-sm text-[var(--portal-muted)]" title={data.generated_at}>
            {t.updated(relative(data.generated_at, lang))}
          </span>
        </div>
        <nav aria-label={name} className="mx-auto hidden max-w-[720px] gap-6 px-5 sm:flex">
          {tabs.map((x) => (
            <TabLink key={x.id} tab={x.id} active={tab === x.id} label={x.label} />
          ))}
        </nav>
      </header>

      <main className="mx-auto max-w-[720px] px-5 pb-28 pt-8 sm:pb-16">
        {tab === "overview" ? <Overview data={data} lang={lang} lastVisit={lastVisit} /> : null}
        {tab === "roadmap" ? <Roadmap data={data} lang={lang} /> : null}
        {tab === "documents" ? <Documents documents={documents} open={route.doc} lang={lang} /> : null}
        {data.branding?.footer ? (
          <footer className="mt-16 border-t border-[var(--portal-line)] pt-5 text-sm text-[var(--portal-muted)]">{data.branding.footer}</footer>
        ) : null}
      </main>

      {/* Phones: the tabs sit under the thumb. */}
      <nav
        aria-label={`${name} (small screens)`}
        className="fixed inset-x-0 bottom-0 z-10 flex border-t border-[var(--portal-line)] bg-[var(--portal-surface)] pb-[env(safe-area-inset-bottom)] sm:hidden"
      >
        {tabs.map((x) => (
          <a
            key={x.id}
            href={`#${x.id}`}
            aria-current={tab === x.id ? "page" : undefined}
            className={`flex min-h-14 flex-1 items-center justify-center text-base font-medium no-underline ${
              tab === x.id ? "text-[var(--portal-accent)]" : "text-[var(--portal-muted)]"
            }`}
          >
            {x.label}
          </a>
        ))}
      </nav>
    </div>
  )
}

function TabLink({ tab, active, label }: { tab: Tab; active: boolean; label: string }) {
  return (
    <a
      href={`#${tab}`}
      aria-current={active ? "page" : undefined}
      className={`-mb-px border-b-2 py-3 text-base font-medium no-underline ${
        active ? "border-[var(--portal-accent)] text-[var(--portal-ink)]" : "border-transparent text-[var(--portal-muted)] hover:text-[var(--portal-ink)]"
      }`}
    >
      {label}
    </a>
  )
}

function Overview({ data, lang, lastVisit }: { data: StakeholderSnapshot; lang: Lang; lastVisit: string | null }) {
  const t = copy[lang]
  const line = statusLine(data, lang)
  const { summary } = data
  const pct = summary.total > 0 ? Math.round((summary.done / summary.total) * 100) : 0
  const recent = useMemo(() => deliveriesSince(data.deliveries, lastVisit, new Date(data.generated_at)), [data, lastVisit])
  const cur = currentPhase(data.milestones)
  const [showAll, setShowAll] = useState(false)

  return (
    <div className="flex flex-col gap-12">
      <section aria-labelledby="status" className="flex flex-col gap-5">
        <h2 id="status" className="portal-display text-[2rem] font-semibold leading-tight sm:text-[2.35rem]">
          {line.headline}
        </h2>
        {line.detail ? <p className="text-lg text-[var(--portal-muted)]">{line.detail}</p> : null}
        <div className="flex flex-col gap-2">
          <div
            className="h-2.5 overflow-hidden rounded-full bg-[var(--portal-track)]"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={pct}
            aria-label={t.progress(summary.done, summary.total)}
          >
            <div className="h-full rounded-full bg-[var(--portal-accent)]" style={{ width: `${pct}%` }} />
          </div>
          <p className="flex justify-between text-sm text-[var(--portal-muted)]">
            <span>{t.progress(summary.done, summary.total)}</span>
            <span>{pct}%</span>
          </p>
        </div>
      </section>

      {data.blockers.length > 0 ? (
        <section aria-labelledby="hold" className="flex flex-col gap-4">
          <h3 id="hold" className="text-sm font-semibold uppercase tracking-[0.08em] text-[var(--portal-muted)]">
            {t.onHold}
          </h3>
          <ul className="flex flex-col gap-3">
            {data.blockers.map((b, i) => (
              <li key={`${b.phase}-${i}`} className="flex gap-3 rounded-xl bg-[var(--portal-hold-soft)] px-4 py-3">
                <CirclePause className="mt-1 h-5 w-5 shrink-0 text-[var(--portal-hold)]" aria-hidden />
                <div>
                  <p className="font-medium">{b.title}</p>
                  <p className="text-sm text-[var(--portal-muted)]">{b.reason}</p>
                </div>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {cur ? (
        <section aria-labelledby="now" className="flex flex-col gap-4">
          <h3 id="now" className="text-sm font-semibold uppercase tracking-[0.08em] text-[var(--portal-muted)]">
            {t.now}
          </h3>
          <a href="#roadmap" className="flex items-center gap-4 rounded-xl bg-[var(--portal-surface)] px-5 py-4 text-[var(--portal-ink)] no-underline">
            <div className="min-w-0 flex-1">
              <p className="portal-display text-xl font-semibold">{phaseName(cur.name)}</p>
              <p className="text-sm text-[var(--portal-muted)]">{t.progress(cur.done, cur.total)}</p>
            </div>
            <ChevronRight className="h-5 w-5 text-[var(--portal-muted)]" aria-hidden />
          </a>
        </section>
      ) : null}

      <section aria-labelledby="since" className="flex flex-col gap-4">
        <h3 id="since" className="text-sm font-semibold uppercase tracking-[0.08em] text-[var(--portal-muted)]">
          {lastVisit ? t.since : t.sinceFirst}
        </h3>
        {recent.length === 0 ? (
          <p className="text-[var(--portal-muted)]">{t.nothingNew}</p>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--portal-line)] rounded-xl bg-[var(--portal-surface)]">
            {(showAll ? recent : recent.slice(0, RECENT_SHOWN)).map((d, i) => (
              <li key={`${d.finished_at}-${i}`} className="flex items-start gap-3 px-4 py-3">
                <CircleCheck className="mt-1 h-5 w-5 shrink-0 text-[var(--portal-done)]" aria-hidden />
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{d.title}</p>
                  <p className="text-sm text-[var(--portal-muted)]">{phaseName(d.phase)}</p>
                </div>
                <time dateTime={d.finished_at} className="shrink-0 text-sm text-[var(--portal-muted)]">
                  {formatDay(d.finished_at, lang)}
                </time>
              </li>
            ))}
          </ul>
        )}
        {recent.length > RECENT_SHOWN ? (
          <button
            type="button"
            onClick={() => setShowAll((v) => !v)}
            aria-expanded={showAll}
            className="self-start rounded-md py-1 text-base font-medium text-[var(--portal-accent)] hover:underline"
          >
            {showAll ? t.showLess : t.showAll(recent.length)}
          </button>
        ) : null}
      </section>

      {data.budget.enabled && data.budget.spend_usd != null ? (
        <section aria-labelledby="budget" className="flex flex-col gap-2">
          <h3 id="budget" className="text-sm font-semibold uppercase tracking-[0.08em] text-[var(--portal-muted)]">
            {t.budget}
          </h3>
          <p className="text-lg">
            {t.spent}: ${data.budget.spend_usd.toFixed(2)}
          </p>
        </section>
      ) : null}

    </div>
  )
}

function Documents({ documents, open, lang }: { documents: StakeholderDocument[]; open?: string; lang: Lang }) {
  const t = copy[lang]
  const doc = open ? documents.find((d) => d.id === open) : undefined
  const html = useMemo(() => (doc ? renderMarkdown(doc.markdown) : ""), [doc])

  if (open) {
    return (
      <article className="flex flex-col gap-6">
        <a href="#documents" className="inline-flex items-center gap-2 self-start text-base font-medium no-underline">
          <ArrowLeft className="h-4 w-4" aria-hidden />
          {t.allDocuments}
        </a>
        {doc ? (
          <>
            <p className="text-sm text-[var(--portal-muted)]">{t.updatedOn(formatDay(doc.updated_at, lang))}</p>
            <div
              className="portal-prose prose max-w-none prose-headings:font-semibold"
              // Sanitised by renderMarkdown (DOMPurify) before it gets here.
              dangerouslySetInnerHTML={{ __html: html }}
            />
          </>
        ) : (
          <p className="text-[var(--portal-muted)]">{t.noDocument}</p>
        )}
      </article>
    )
  }

  return (
    <section aria-label={t.documents}>
      <ul className="flex flex-col divide-y divide-[var(--portal-line)] rounded-xl bg-[var(--portal-surface)]">
        {documents.map((d) => (
          <li key={d.id}>
            <a href={`#documents/${d.id}`} className="flex items-center gap-4 px-5 py-4 text-[var(--portal-ink)] no-underline">
              <FileText className="h-5 w-5 shrink-0 text-[var(--portal-muted)]" aria-hidden />
              <div className="min-w-0 flex-1">
                <p className="portal-display text-lg font-semibold">{d.title}</p>
                <p className="text-sm text-[var(--portal-muted)]">{t.updatedOn(formatDay(d.updated_at, lang))}</p>
              </div>
              <ChevronRight className="h-5 w-5 shrink-0 text-[var(--portal-muted)]" aria-hidden />
            </a>
          </li>
        ))}
      </ul>
    </section>
  )
}

// How many recent deliveries the overview lists before "show all": the
// newest few are the news; the rest is history the roadmap already holds.
const RECENT_SHOWN = 5

function Roadmap({ data, lang }: { data: StakeholderSnapshot; lang: Lang }) {
  const t = copy[lang]
  const phases = [...data.milestones].sort((a, b) => a.phase - b.phase)
  const cur = currentPhase(phases)
  return (
    <section aria-label={t.roadmap}>
      <ol className="flex flex-col">
        {phases.map((m, i) => (
          <PhaseRow key={m.phase} m={m} state={phaseState(m, cur)} lang={lang} last={i === phases.length - 1} />
        ))}
      </ol>
    </section>
  )
}

const stateLabel = (s: PhaseState, lang: Lang) => ({ done: copy[lang].done, now: copy[lang].now, next: copy[lang].next })[s]

function PhaseRow({ m, state, lang, last }: { m: StakeholderMilestone; state: PhaseState; lang: Lang; last: boolean }) {
  const t = copy[lang]
  const marker =
    state === "done" ? (
      <CircleCheck className="h-6 w-6 text-[var(--portal-done)]" aria-hidden />
    ) : state === "now" ? (
      <CircleDot className="h-6 w-6 text-[var(--portal-accent)]" aria-hidden />
    ) : (
      <Circle className="h-6 w-6 text-[var(--portal-muted)]" aria-hidden />
    )
  return (
    <li className="relative flex gap-4">
      <div className="flex flex-col items-center">
        <div className="bg-[var(--portal-ground)] py-1">{marker}</div>
        {last ? null : <div className="w-px flex-1 bg-[var(--portal-line)]" />}
      </div>
      <details open={state === "now"} className="mb-6 min-w-0 flex-1">
        <summary className="flex items-start gap-3 py-1">
          <div className="min-w-0 flex-1">
            <p className="portal-display text-xl font-semibold leading-snug">{phaseName(m.name)}</p>
            <p className="text-sm text-[var(--portal-muted)]">
              {stateLabel(state, lang)} · {t.progress(m.done, m.total)}
              {m.blocked > 0 ? ` · ${m.blocked} ${t.onHold.toLowerCase()}` : ""}
            </p>
          </div>
          <ChevronRight className="portal-chevron mt-1.5 h-5 w-5 shrink-0 text-[var(--portal-muted)]" aria-hidden />
        </summary>
        <div className="mt-3 flex flex-col gap-5">
          {(m.packages ?? []).map((p, i) => (
            <div key={`${p.name}-${i}`} className="flex flex-col gap-2">
              <p className="text-sm font-semibold text-[var(--portal-muted)]">
                {p.name ? capitalize(p.name) : t.other} · {p.done}/{p.total}
              </p>
              <ul className="flex flex-col gap-1.5">
                {p.deliverables.map((d, j) => (
                  <li key={`${d.title}-${j}`} className="flex items-start gap-2.5">
                    <StatusIcon status={d.status} lang={lang} />
                    <span className={d.status === "done" ? "text-[var(--portal-muted)]" : ""}>{d.title}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </details>
    </li>
  )
}

function StatusIcon({ status, lang }: { status: DeliverableStatus; lang: Lang }) {
  const t = copy[lang]
  const common = "mt-1 h-4 w-4 shrink-0"
  switch (status) {
    case "done":
      return <CircleCheck className={`${common} text-[var(--portal-done)]`} aria-label={t.done} />
    case "in_progress":
      return <CircleDot className={`${common} text-[var(--portal-accent)]`} aria-label={t.inProgress} />
    case "blocked":
      return <CirclePause className={`${common} text-[var(--portal-hold)]`} aria-label={t.blocked} />
    default:
      return <Circle className={`${common} text-[var(--portal-muted)]`} aria-label={t.pending} />
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

function relative(iso: string, lang: Lang): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return iso
  const minutes = Math.round((Date.now() - then) / 60_000)
  const es = lang === "es"
  if (minutes < 1) return es ? "hace un momento" : "just now"
  if (minutes < 60) return es ? `hace ${minutes} min` : `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return es ? `hace ${hours} h` : `${hours} h ago`
  const days = Math.round(hours / 24)
  return es ? `hace ${days} ${days === 1 ? "día" : "días"}` : `${days} ${days === 1 ? "day" : "days"} ago`
}
