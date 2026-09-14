import { useEffect, useState } from "react"
import { formatEta } from "@/lib/eta"
import { AlertTriangle, CheckCircle2 } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import type { StakeholderSnapshot } from "./types"

declare global {
  interface Window {
    // Populated by a `data.js` sibling file `orch publish` writes next to
    // this HTML, wrapping the same document `data.json` carries. Reading
    // this first (before the fetch fallback below) is what lets a
    // downloaded zip opened straight from disk (file://) render without a
    // server: a `<script src="data.js">` tag is not subject to the
    // same-origin fetch restriction a bare `fetch('./data.json')` is
    // under `file://`, since it's just script execution, not a network
    // read.
    __ORCH_SNAPSHOT__?: StakeholderSnapshot
  }
}

/**
 * Stakeholder snapshot — G6.2.
 *
 * No API, no login, no polling: this whole page reads a snapshot `orch
 * publish` writes next to this HTML at export time — everything it needs
 * to render is either baked into this bundle or in those sibling files.
 * Two ways to get it, tried in order: `window.__ORCH_SNAPSHOT__` (set by a
 * `data.js` sibling, works under `file://`) and, only if that's absent,
 * `fetch('./data.json')` (works when served over http, e.g. the operator
 * dashboard's own stakeholder profile route). Schema (schema: 1) is
 * internal/publish's (G6.1) — see ./types.ts and
 * docs/SNAPSHOT-SCHEMA.md for the exact, committed shape.
 */

type LoadState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: StakeholderSnapshot }

export default function App() {
  const [state, setState] = useState<LoadState>({ status: "loading" })

  useEffect(() => {
    // window.__ORCH_SNAPSHOT__ is set synchronously by data.js, which is
    // loaded via a <script> tag before this bundle in the exported HTML
    // — so by the time this component mounts, it's already there if the
    // exporter wrote one.
    if (window.__ORCH_SNAPSHOT__) {
      setState({ status: "ready", data: window.__ORCH_SNAPSHOT__ })
      return
    }

    let cancelled = false
    fetch("./data.json")
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((data: StakeholderSnapshot) => {
        if (!cancelled) setState({ status: "ready", data })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        // A file:// page blocked by the browser's cross-origin policy
        // throws a bare TypeError with no HTTP status to inspect —
        // distinguish that from "the file is genuinely missing" so the
        // message tells the viewer what to actually do about it. This
        // path is now only reached when data.js is also missing or
        // failed to set window.__ORCH_SNAPSHOT__ (an older export, or a
        // build that skipped it) — a current export's file:// case is
        // handled above, before any fetch is attempted.
        const isLikelyCorsBlock =
          err instanceof TypeError && window.location.protocol === "file:"
        setState({
          status: "error",
          message: isLikelyCorsBlock
            ? "Your browser blocks a local file from reading another local file. Serve this folder over HTTP instead (e.g. `python3 -m http.server`) and open it from there."
            : `Could not load data.json: ${err instanceof Error ? err.message : String(err)}`,
        })
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Absent unless the operator configured it; every branch below treats that
  // as "render exactly what we rendered before this feature existed".
  const branding = state.status === "ready" ? state.data.branding : undefined

  return (
    <div className="mx-auto max-w-4xl space-y-6 px-6 py-8">
      <header className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          {branding?.logo ? (
            // The logo is a data: URI by the time it reaches here — the
            // builder embeds it, so nothing on this page fetches anything.
            <img
              src={branding.logo}
              alt=""
              className="h-10 w-auto max-w-[180px] object-contain"
            />
          ) : null}
          <h1
            className="text-2xl font-semibold tracking-tight"
            style={branding?.accent_color ? { color: branding.accent_color } : undefined}
          >
            {headerTitle(state)}
          </h1>
        </div>
        {state.status === "ready" ? <Freshness generatedAt={state.data.generated_at} /> : null}
      </header>

      {state.status === "loading" ? (
        <div className="space-y-4">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : null}

      {state.status === "error" ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Couldn't load this snapshot</AlertTitle>
          <AlertDescription>{state.message}</AlertDescription>
        </Alert>
      ) : null}

      {state.status === "ready" ? (
        <>
          {state.data.executive_summary.text ? (
            <p className="text-sm text-muted-foreground">
              {state.data.executive_summary.text}
            </p>
          ) : null}
          <div className="grid gap-4 sm:grid-cols-2">
            <SummaryCard summary={state.data.summary} />
            {state.data.budget.enabled ? <BudgetCard budget={state.data.budget} /> : null}
            <MilestonesCard milestones={state.data.milestones} />
            <BlockersCard blockers={state.data.blockers} />
          </div>
        </>
      ) : null}

      {branding?.footer ? (
        <footer className="border-t pt-4 text-xs text-muted-foreground">
          {branding.footer}
        </footer>
      ) : null}
    </div>
  )
}

function Freshness({ generatedAt }: { generatedAt: string }) {
  return (
    <span className="whitespace-nowrap text-xs text-muted-foreground" title={generatedAt}>
      Generated {formatRelative(generatedAt)}
    </span>
  )
}

function formatRelative(iso: string): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return iso
  const diffMs = Date.now() - then
  const minutes = Math.round(diffMs / 60_000)
  if (minutes < 1) return "just now"
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  return `${days}d ago`
}

function SummaryCard({ summary }: { summary: StakeholderSnapshot["summary"] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Summary</CardTitle>
        <CardDescription>
          {summary.done}/{summary.total} tasks done ({summary.percent_done.toFixed(0)}%)
        </CardDescription>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
        <Stat label="In progress" value={summary.in_progress} />
        <Stat label="Blocked" value={summary.blocked} />
        <Stat label="Backlog" value={summary.backlog} />
        <Stat label="Estimated finish" value={formatEta(summary).value} />
      </CardContent>
    </Card>
  )
}

function BudgetCard({ budget }: { budget: StakeholderSnapshot["budget"] }) {
  // Only rendered by the caller when budget.enabled is true, at which
  // point the snapshot always carries both fields (docs/SNAPSHOT-SCHEMA.md)
  // — they're optional in the type only because the schema omits them
  // entirely when disabled. The fallbacks below are for a malformed or
  // stale document, not the documented shape.
  const spendByDay = budget.spend_by_day ?? []
  const spendUsd = budget.spend_usd ?? 0
  const last = spendByDay[spendByDay.length - 1]
  return (
    <Card>
      <CardHeader>
        <CardTitle>Budget</CardTitle>
        <CardDescription>Last {spendByDay.length} day(s)</CardDescription>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-3 text-sm">
        <Stat label="Total spend" value={`$${spendUsd.toFixed(2)}`} />
        <Stat
          label="Yesterday"
          value={last ? `$${last.cost_usd.toFixed(2)}` : "—"}
        />
      </CardContent>
    </Card>
  )
}

function MilestonesCard({
  milestones,
}: {
  milestones: StakeholderSnapshot["milestones"]
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Milestones</CardTitle>
      </CardHeader>
      <CardContent>
        {milestones.length === 0 ? (
          <p className="text-sm text-muted-foreground">No milestones.</p>
        ) : (
          <ul className="space-y-2 text-sm">
            {milestones.map((m) => (
              <li key={m.phase} className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5">
                  {m.complete ? (
                    <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" aria-hidden />
                  ) : null}
                  {m.name}
                </span>
                <Badge variant={m.complete ? "success" : "muted"} className="font-mono text-[10px]">
                  {m.done}/{m.total}
                </Badge>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function BlockersCard({ blockers }: { blockers: StakeholderSnapshot["blockers"] }) {
  return (
    <Card className={cn(blockers.length > 0 && "border-amber-500/50")}>
      <CardHeader>
        <CardTitle>Blockers</CardTitle>
      </CardHeader>
      <CardContent>
        {blockers.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing blocked.</p>
        ) : (
          <ul className="space-y-2 text-sm">
            {blockers.map((b, i) => (
              <li key={`${b.phase}-${i}`}>
                <span className="font-medium">{b.title}</span>
                <span className="block text-xs text-muted-foreground">{b.reason}</span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * The client-facing name: the brand when there is one, the project's own name
 * otherwise, and a neutral placeholder before the document has loaded.
 */
function headerTitle(state: LoadState): string {
  if (state.status !== "ready") return "Project Snapshot"
  return state.data.branding?.name?.trim() || state.data.project_name
}

function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <div className="text-base">{value}</div>
    </div>
  )
}
