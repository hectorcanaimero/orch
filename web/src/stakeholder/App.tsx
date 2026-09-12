import { useEffect, useState } from "react"
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
import { isMilestoneComplete, type StakeholderSnapshot } from "./types"

/**
 * Stakeholder snapshot — G6.2.
 *
 * No API, no login, no polling: this whole page is `fetch('./data.json')`
 * against a file `orch publish` writes next to this HTML at export time —
 * everything it needs to render is either baked into this bundle or in
 * that one sibling file. Schema (schema: 1) is internal/publish's (G6.1),
 * confirmed with orch-sonnet — see ./types.ts for the exact shape and one
 * flagged issue in their draft.
 */

type LoadState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: StakeholderSnapshot }

export default function App() {
  const [state, setState] = useState<LoadState>({ status: "loading" })

  useEffect(() => {
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
        // message tells the viewer what to actually do about it.
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

  return (
    <div className="mx-auto max-w-4xl space-y-6 px-6 py-8">
      <header className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            {state.status === "ready" ? state.data.project_name : "Project Snapshot"}
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
        <Stat
          label="ETA"
          value={summary.eta_hours == null ? "—" : `${summary.eta_hours.toFixed(1)}h`}
        />
      </CardContent>
    </Card>
  )
}

function BudgetCard({ budget }: { budget: StakeholderSnapshot["budget"] }) {
  const last = budget.spend_by_day[budget.spend_by_day.length - 1]
  return (
    <Card>
      <CardHeader>
        <CardTitle>Budget</CardTitle>
        <CardDescription>Last {budget.spend_by_day.length} day(s)</CardDescription>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-3 text-sm">
        <Stat label="Total spend" value={`$${budget.spend_usd.toFixed(2)}`} />
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
            {milestones.map((m) => {
              const complete = isMilestoneComplete(m)
              return (
                <li key={m.phase} className="flex items-center justify-between gap-2">
                  <span className="flex items-center gap-1.5">
                    {complete ? (
                      <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" aria-hidden />
                    ) : null}
                    {m.name}
                  </span>
                  <Badge variant={complete ? "success" : "muted"} className="font-mono text-[10px]">
                    {m.done}/{m.total}
                  </Badge>
                </li>
              )
            })}
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
