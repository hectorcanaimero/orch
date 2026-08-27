import { useState } from "react"
import {
  AlertTriangle,
  Ban,
  Check,
  CheckCircle2,
  Clock,
  Copy,
  DollarSign,
  Download,
  ListTodo,
  Loader2,
  Timer,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { ProjectConfigWidget } from "@/components/ProjectConfigWidget"
import { useStakeholderSummary } from "@/hooks/useStakeholderSummary"
import type { SpendByDay, StakeholderMilestone, StakeholderPhase } from "@/lib/types"

const usdFormatter = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  maximumFractionDigits: 2,
})

// ---- Sub-components --------------------------------------------------------

function StatCard({
  label,
  value,
  icon: Icon,
  tone,
}: {
  label: string
  value: number
  icon: typeof CheckCircle2
  tone: "default" | "warning" | "danger" | "muted"
}) {
  const toneClass =
    tone === "danger"
      ? "text-red-600"
      : tone === "warning"
        ? "text-amber-600"
        : tone === "muted"
          ? "text-zinc-500"
          : "text-emerald-600"
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardDescription className="text-xs uppercase tracking-wide">
          {label}
        </CardDescription>
        <Icon className={`h-4 w-4 ${toneClass}`} />
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-semibold">{value}</div>
      </CardContent>
    </Card>
  )
}

function MilestoneRow({ milestone }: { milestone: StakeholderMilestone }) {
  const { phase, total_count, done_count, done } = milestone
  const pct =
    total_count > 0 ? Math.round((done_count / total_count) * 100) : 0
  return (
    <li className="flex flex-col gap-2 rounded-md border p-4">
      <div className="flex items-center justify-between">
        <div className="font-mono font-semibold">Phase {phase}</div>
        {done ? <Badge variant="success">Done</Badge> : null}
      </div>
      <div className="flex items-center gap-3">
        <Progress value={pct} className="h-1.5 flex-1" />
        <span className="text-xs text-muted-foreground">
          {done_count}/{total_count} tasks
        </span>
      </div>
    </li>
  )
}

// ---- Phase timeline (Gantt-like) ------------------------------------------

function PhaseTimeline({ phases }: { phases: StakeholderPhase[] }) {
  if (!phases.length) return null
  const maxEstimate = Math.max(...phases.map((p) => p.estimate_hours), 1)

  return (
    <Card>
      <CardHeader>
        <CardTitle>Phase timeline</CardTitle>
        <CardDescription>Progress by phase — width reflects estimated effort</CardDescription>
      </CardHeader>
      <CardContent>
        <div className="space-y-3">
          {phases.map((phase) => {
            const widthPct = Math.max(
              8,
              Math.round((phase.estimate_hours / maxEstimate) * 100),
            )
            return (
              <div key={phase.phase} className="flex items-center gap-3">
                {/* Phase label */}
                <div className="w-32 shrink-0 truncate text-xs font-medium text-zinc-700">
                  {phase.name}
                </div>

                {/* Bar track */}
                <div className="relative flex-1 overflow-hidden rounded-full bg-zinc-100" style={{ height: 20 }}>
                  {/* Total bar proportional to estimate */}
                  <div
                    className="absolute inset-y-0 left-0 rounded-full bg-zinc-200"
                    style={{ width: `${widthPct}%` }}
                  />
                  {/* Done fill */}
                  <div
                    className={`absolute inset-y-0 left-0 rounded-full transition-all ${
                      phase.pct_done === 100
                        ? "bg-emerald-500"
                        : phase.blocked > 0
                          ? "bg-amber-400"
                          : "bg-blue-500"
                    }`}
                    style={{ width: `${(phase.pct_done / 100) * widthPct}%` }}
                  />
                </div>

                {/* Status badges */}
                <div className="flex w-36 shrink-0 items-center gap-1.5 text-xs">
                  <span className="font-medium text-zinc-900">{phase.pct_done}%</span>
                  {phase.in_progress > 0 && (
                    <span className="rounded bg-blue-100 px-1 py-0.5 text-blue-700">
                      {phase.in_progress} active
                    </span>
                  )}
                  {phase.blocked > 0 && (
                    <span className="rounded bg-amber-100 px-1 py-0.5 text-amber-700">
                      {phase.blocked} blocked
                    </span>
                  )}
                  {phase.pct_done === 100 && (
                    <span className="rounded bg-emerald-100 px-1 py-0.5 text-emerald-700">
                      Done
                    </span>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </CardContent>
    </Card>
  )
}

// ---- Spend sparkline -------------------------------------------------------

function SpendChart({ days }: { days: SpendByDay[] }) {
  if (!days.length) return null

  const max = Math.max(...days.map((d) => d.cost), 0.01)
  const total = days.reduce((s, d) => s + d.cost, 0)
  const W = 280
  const H = 48
  const barW = Math.max(2, Math.floor(W / days.length) - 1)

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardDescription className="text-xs uppercase tracking-wide">
          AI spend — last 14 days
        </CardDescription>
        <DollarSign className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-semibold">{usdFormatter.format(total)}</div>
        <svg
          viewBox={`0 0 ${W} ${H}`}
          className="mt-3 w-full"
          style={{ height: H }}
          aria-label="Daily spend chart"
        >
          {days.map((d, i) => {
            const barH = Math.max(2, Math.round((d.cost / max) * H))
            return (
              <rect
                key={d.date}
                x={i * (barW + 1)}
                y={H - barH}
                width={barW}
                height={barH}
                rx={1}
                className="fill-blue-400"
                aria-label={`${d.date}: ${usdFormatter.format(d.cost)}`}
              />
            )
          })}
        </svg>
        <div className="mt-1 flex justify-between text-xs text-muted-foreground">
          <span>{days[0]?.date}</span>
          <span>{days[days.length - 1]?.date}</span>
        </div>
      </CardContent>
    </Card>
  )
}

// ---- Executive summary -----------------------------------------------------

function ExecSummary({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  if (!text) return null
  const handleCopy = () => {
    void navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    })
  }
  return (
    <Card className="border-blue-200 bg-blue-50/50">
      <CardHeader className="flex flex-row items-center justify-between gap-2 pb-2">
        <CardTitle className="text-base">Executive summary</CardTitle>
        <Button
          variant="outline"
          size="sm"
          className="print:hidden gap-1.5"
          onClick={handleCopy}
        >
          {copied ? (
            <Check className="h-3.5 w-3.5 text-emerald-500" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
          {copied ? "Copied" : "Copy"}
        </Button>
      </CardHeader>
      <CardContent>
        <p className="whitespace-pre-line text-sm leading-relaxed text-zinc-700">{text}</p>
      </CardContent>
    </Card>
  )
}

// ---- PDF export ------------------------------------------------------------

function PrintButton() {
  return (
    <Button
      variant="outline"
      size="sm"
      className="print:hidden gap-1.5"
      onClick={() => window.print()}
    >
      <Download className="h-3.5 w-3.5" />
      Export PDF
    </Button>
  )
}

// ---- Main page -------------------------------------------------------------

export function StakeholderSummaryPage() {
  const { data, isLoading, isError, error, isFetching } = useStakeholderSummary()

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-32 w-full" />
        <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-24" />)}
        </div>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <Skeleton className="h-24" />
          <Skeleton className="h-24" />
        </div>
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load summary</AlertTitle>
        <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
      </Alert>
    )
  }

  if (!data) return null

  const {
    project_id,
    summary,
    milestones,
    spend_rounded_usd,
    eta_hours,
    refresh_interval_s,
    phases_timeline,
    spend_by_day,
    exec_summary,
  } = data

  const refreshSeconds = refresh_interval_s && refresh_interval_s > 0 ? refresh_interval_s : 10

  return (
    <div className="space-y-6">
      {/* Header */}
      <header className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">{project_id}</h1>
            {isFetching ? (
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            ) : null}
          </div>
          <p className="text-sm text-muted-foreground">
            Refreshing every {refreshSeconds}s
          </p>
        </div>
        <PrintButton />
      </header>

      {/* Executive summary */}
      {exec_summary ? <ExecSummary text={exec_summary} /> : null}

      {/* Overall progress */}
      <Card>
        <CardHeader>
          <CardTitle>Overall progress</CardTitle>
          <CardDescription>
            {summary.done} of {summary.total} tasks done
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-center gap-4">
            <Progress value={summary.percent_done} className="h-2 flex-1" />
            <span className="w-16 text-right text-sm font-medium">
              {Math.round(summary.percent_done)}%
            </span>
          </div>
          <p className="mt-3 text-xs text-muted-foreground">
            Estimated total effort: {summary.estimate_hours_total}h
          </p>
        </CardContent>
      </Card>

      {/* Phase timeline */}
      {phases_timeline?.length > 0 ? (
        <PhaseTimeline phases={phases_timeline} />
      ) : null}

      {/* Task counters */}
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard label="Done" value={summary.done} icon={CheckCircle2} tone="default" />
        <StatCard label="In progress" value={summary.in_progress} icon={Loader2} tone="warning" />
        <StatCard label="Blocked" value={summary.blocked} icon={Ban} tone="danger" />
        <StatCard label="Backlog" value={summary.backlog} icon={ListTodo} tone="muted" />
      </div>

      {/* Spend + ETA */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        {spend_by_day?.length > 1 ? (
          <SpendChart days={spend_by_day} />
        ) : (
          <Card>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardDescription className="text-xs uppercase tracking-wide">Spend</CardDescription>
              <DollarSign className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-2xl font-semibold">
                {usdFormatter.format(spend_rounded_usd)}
              </div>
            </CardContent>
          </Card>
        )}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardDescription className="text-xs uppercase tracking-wide">ETA</CardDescription>
            {eta_hours == null ? (
              <Clock className="h-4 w-4 text-muted-foreground" />
            ) : (
              <Timer className="h-4 w-4 text-muted-foreground" />
            )}
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-semibold">
              {eta_hours == null ? "—" : `${eta_hours}h`}
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Milestones */}
      {milestones.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>Milestones</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-3">
              {milestones.map((m) => (
                <MilestoneRow key={m.phase} milestone={m} />
              ))}
            </ul>
          </CardContent>
        </Card>
      ) : null}

      <ProjectConfigWidget />
    </div>
  )
}
