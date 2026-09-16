import { useState } from "react"
import { formatEta } from "@/lib/eta"
import { useWhoami } from "@/hooks/useWhoami"
import { AlertTriangle, Check, Clock, Copy, DollarSign, Download, Loader2, Timer } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { KpiCard } from "@/components/charts/KpiCard"
import { Explain } from "@/components/Explain"
import { ProjectConfigWidget } from "@/components/ProjectConfigWidget"
import { isStakeholderSummaryUnavailable, useStakeholderSummary } from "@/hooks/useStakeholderSummary"
import { fmt, t } from "@/i18n"
import { STATUS, type StatusKey } from "@/lib/status"
import { cn } from "@/lib/utils"
import type { SpendByDay, StakeholderMilestone, StakeholderPhase } from "@/lib/types"

// ---- Phases ----------------------------------------------------------------

/**
 * One segment per task, colored by status, so a phase reads as "how many of
 * its tasks are where" — length never doubles as effort.
 */
export function PhaseSegments({ phase }: { phase: StakeholderPhase }) {
  const todo = Math.max(0, phase.total - phase.done - phase.in_progress - phase.blocked)
  const segments: StatusKey[] = [
    ...Array<StatusKey>(phase.done).fill("done"),
    ...Array<StatusKey>(phase.in_progress).fill("in_progress"),
    ...Array<StatusKey>(phase.blocked).fill("blocked"),
    ...Array<StatusKey>(todo).fill("todo"),
  ]
  return (
    <div
      className="flex h-2.5 min-w-0 flex-1 gap-[3px]"
      role="img"
      aria-label={t("summary.phase_segments", {
        done: phase.done,
        active: phase.in_progress,
        blocked: phase.blocked,
        total: phase.total,
      })}
    >
      {segments.map((key, i) => (
        <span
          key={i}
          className={cn("h-full min-w-[3px] flex-1 rounded-[2px]", key === "todo" ? "bg-muted" : STATUS[key].fill)}
        />
      ))}
    </div>
  )
}

function PhaseTimeline({ phases }: { phases: StakeholderPhase[] }) {
  if (!phases.length) return null
  return (
    <section className="rounded-xl border bg-card p-5" aria-labelledby="phases-heading">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="phases-heading" className="text-base font-semibold">
          {t("summary.phases")}
        </h2>
        <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {(["done", "in_progress", "blocked", "todo"] as StatusKey[]).map((key) => (
            <span key={key} className="inline-flex items-center gap-1.5">
              <span className={cn("h-2 w-2 rounded-[2px]", key === "todo" ? "bg-muted-foreground/40" : STATUS[key].fill)} />
              {t(STATUS[key].label)}
            </span>
          ))}
        </p>
      </div>
      <ul className="mt-4 space-y-3">
        {phases.map((phase) => (
          <li key={phase.phase} className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)_auto] items-center gap-3 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_auto]">
            <span className="truncate text-sm" title={phase.name}>
              {phase.name}
            </span>
            <PhaseSegments phase={phase} />
            <span className="text-right text-xs tabular-nums text-muted-foreground">
              {t("summary.tasks_of", { done: phase.done, total: phase.total })}
            </span>
          </li>
        ))}
      </ul>
    </section>
  )
}

function Milestones({ milestones, phases }: { milestones: StakeholderMilestone[]; phases: StakeholderPhase[] }) {
  const nameOf = new Map(phases.map((p) => [p.phase, p.name]))
  return (
    <section className="rounded-xl border bg-card p-5" aria-labelledby="milestones-heading">
      <h2 id="milestones-heading" className="text-base font-semibold">
        {t("summary.milestones")}
      </h2>
      <ul className="mt-3 divide-y">
        {milestones.map((m) => {
          const pct = m.total_count > 0 ? Math.round((m.done_count / m.total_count) * 100) : 0
          return (
            <li key={m.phase} className="flex items-center gap-4 py-3">
              <span className="min-w-0 flex-1 truncate text-sm font-medium">
                {nameOf.get(m.phase) ?? t("filters.phase", { phase: m.phase })}
              </span>
              <Progress value={pct} className="hidden h-1.5 w-40 sm:block" />
              <span className="w-20 text-right text-xs tabular-nums text-muted-foreground">
                {t("summary.tasks_of", { done: m.done_count, total: m.total_count })}
              </span>
              <span className="w-14 text-right">
                {m.done ? (
                  <span className={cn("inline-flex items-center gap-1 text-xs font-medium", STATUS.done.text)}>
                    <Check className="h-3.5 w-3.5" aria-hidden />
                    {t("status.done")}
                  </span>
                ) : null}
              </span>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

// ---- Spend sparkline -------------------------------------------------------

// total is spend_rounded_usd when the server sent it: the same figure the
// executive summary quotes, so the card and the sentence cannot disagree
// ($185.76 next to "$186.00").
function SpendChart({ days, total: sharedTotal }: { days: SpendByDay[]; total: number | null }) {
  const max = Math.max(...days.map((d) => d.cost), 0.01)
  const total = sharedTotal ?? days.reduce((s, d) => s + d.cost, 0)
  const W = 280
  const H = 44
  const barW = Math.max(2, Math.floor(W / days.length) - 2)

  return (
    <KpiCard label={<Explain term="spend">{t("summary.spend_14d")}</Explain>} value={fmt.usd(total)} icon={DollarSign}>
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="mt-2 w-full" style={{ height: H }} role="img" aria-label={t("summary.spend_chart")}>
        {days.map((d, i) => {
          const barH = Math.max(2, Math.round((d.cost / max) * H))
          const last = i === days.length - 1
          return (
            <rect
              key={d.date}
              x={i * (barW + 2)}
              y={H - barH}
              width={barW}
              height={barH}
              rx={1.5}
              className={last ? "fill-foreground" : "fill-muted-foreground/35"}
            >
              <title>{`${fmt.day(d.date)}: ${fmt.usd(d.cost)}`}</title>
            </rect>
          )
        })}
      </svg>
      <div className="flex justify-between text-xs tabular-nums text-muted-foreground">
        <span>{fmt.day(days[0].date)}</span>
        <span>{fmt.day(days[days.length - 1].date)}</span>
      </div>
    </KpiCard>
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
    <section className="rounded-xl border bg-card p-5" aria-labelledby="exec-heading">
      <div className="flex items-center justify-between gap-2">
        <h2 id="exec-heading" className="text-base font-semibold">
          {t("summary.exec")}
        </h2>
        <Button variant="outline" size="sm" className="h-8 gap-1.5 print:hidden" onClick={handleCopy}>
          {copied ? <Check className={cn("h-3.5 w-3.5", STATUS.done.text)} /> : <Copy className="h-3.5 w-3.5" />}
          {copied ? t("common.copied") : t("common.copy")}
        </Button>
      </div>
      <p className="mt-2 max-w-[75ch] whitespace-pre-line text-sm leading-relaxed text-muted-foreground">{text}</p>
    </section>
  )
}

// ---- Main page -------------------------------------------------------------

export function StakeholderSummaryPage() {
  const { data, isLoading, isError, error, isFetching } = useStakeholderSummary()
  const { data: whoami } = useWhoami()
  const isStakeholder = whoami?.profile === "stakeholder"

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-24 w-full" />
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {[...Array(4)].map((_, i) => (
            <Skeleton key={i} className="h-24" />
          ))}
        </div>
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }

  if (isError) {
    // Bug 26's own review (PR #205) found this: the Go dashboard doesn't
    // implement /stakeholder/summary yet, so this named state is the
    // COMMON case today, not a rare one — a plain "failed to load"
    // alert would read as a transient problem worth retrying, when the
    // real answer is "this binary doesn't have this page yet."
    if (isStakeholderSummaryUnavailable(error)) {
      return (
        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("summary.unavailable")}</AlertTitle>
          <AlertDescription>{t("summary.unavailable_detail")}</AlertDescription>
        </Alert>
      )
    }
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>{t("summary.load_failed")}</AlertTitle>
        <AlertDescription>{error?.message ?? t("common.unknown_error")}</AlertDescription>
      </Alert>
    )
  }

  if (!data) return null

  const { project_id, summary, milestones, spend_rounded_usd, refresh_interval_s, phases_timeline, spend_by_day, exec_summary } =
    data
  const eta = formatEta(data)
  const refreshSeconds = refresh_interval_s && refresh_interval_s > 0 ? refresh_interval_s : 10
  const phases = phases_timeline ?? []

  const counters: { key: StatusKey; label: string; value: number }[] = [
    { key: "done", label: t("status.done"), value: summary.done },
    { key: "in_progress", label: t("status.in_progress"), value: summary.in_progress },
    { key: "blocked", label: t("status.blocked"), value: summary.blocked },
    { key: "backlog", label: t("status.backlog"), value: summary.backlog },
  ]

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <h1 className="flex items-center gap-3 truncate text-2xl font-semibold tracking-[-0.02em]">
            {project_id}
            {isFetching ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-label={t("common.refreshing")} /> : null}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("summary.refresh", { seconds: refreshSeconds })}</p>
        </div>
        <Button variant="outline" size="sm" className="gap-1.5 print:hidden" onClick={() => window.print()}>
          <Download className="h-3.5 w-3.5" />
          {t("summary.export_pdf")}
        </Button>
      </header>

      <section className="rounded-xl border bg-card p-5" aria-labelledby="progress-heading">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="progress-heading" className="text-base font-semibold">
            {t("summary.progress")}
          </h2>
          <span className="text-sm tabular-nums text-muted-foreground">
            <Explain term="estimate">{t("summary.effort", { hours: summary.estimate_hours_total })}</Explain>
          </span>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-x-6 gap-y-3">
          <span className="text-3xl font-semibold tabular-nums tracking-[-0.02em]">{Math.round(summary.percent_done)}%</span>
          <div className="min-w-[10rem] flex-1">
            <Progress value={summary.percent_done} className="h-2" />
            <p className="mt-1.5 text-xs tabular-nums text-muted-foreground">
              {t("summary.tasks_done", { done: summary.done, total: summary.total })}
            </p>
          </div>
          <div className="flex items-center gap-3 border-l pl-6">
            {eta.value === "—" ? (
              <Clock className="h-4 w-4 text-muted-foreground" aria-hidden />
            ) : (
              <Timer className="h-4 w-4 text-muted-foreground" aria-hidden />
            )}
            <div>
              <div className="text-xs text-muted-foreground">
                <Explain term="eta" />
              </div>
              <div className="text-lg font-semibold tabular-nums leading-tight">
                {eta.value}
                {eta.detail ? <span className="ml-2 text-xs font-normal text-muted-foreground">{eta.detail}</span> : null}
              </div>
            </div>
          </div>
        </div>
        <ul className="mt-4 grid grid-cols-2 gap-x-6 gap-y-2 border-t pt-4 sm:grid-cols-4">
          {counters.map(({ key, label, value }) => {
            const meta = STATUS[key]
            return (
              <li key={key} className="flex items-center gap-2 text-sm">
                <meta.icon className={cn("h-4 w-4", meta.text)} aria-hidden />
                <span className="text-muted-foreground">{label}</span>
                <span className="ml-auto font-semibold tabular-nums sm:ml-1">{value}</span>
              </li>
            )
          })}
        </ul>
      </section>

      {exec_summary ? <ExecSummary text={exec_summary} /> : null}

      {spend_by_day?.length > 1 ? (
        <SpendChart days={spend_by_day} total={spend_rounded_usd} />
      ) : spend_rounded_usd == null ? null : (
        <KpiCard label={<Explain term="spend">{t("summary.spend")}</Explain>} value={fmt.usd(spend_rounded_usd)} icon={DollarSign} />
      )}

      {phases.length > 0 ? <PhaseTimeline phases={phases} /> : null}

      {/* The summary's milestones are its phases counted by task; with the
          phase timeline on screen they would say the same thing twice. */}
      {phases.length === 0 && milestones.length > 0 ? <Milestones milestones={milestones} phases={phases} /> : null}

      {/* /api/config is operator material, and a stakeholder token gets 403 on it. */}
      {isStakeholder ? null : <ProjectConfigWidget />}
    </div>
  )
}
