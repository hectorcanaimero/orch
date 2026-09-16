import { useState } from "react"
import { Link } from "react-router-dom"
import { AlertTriangle, Check, Copy, Hourglass, OctagonPause } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Skeleton } from "@/components/ui/skeleton"
import { Explain } from "@/components/Explain"
import { LiveStatusPill } from "@/components/LiveStatusPill"
import { useBudgetSummary, type BudgetRow } from "@/hooks/useBudgetSummary"
import { useEventStream } from "@/hooks/useEventStream"
import { useMilestones } from "@/hooks/useMilestones"
import { useNow, useTicker, type NowDispatch } from "@/hooks/useNow"
import { useTasks } from "@/hooks/useTasks"
import { fmt, locale, t } from "@/i18n"
import { describeLoadError } from "@/lib/errors"
import { STATUS } from "@/lib/status"
import { cn } from "@/lib/utils"
import { PhaseSegments } from "@/pages/StakeholderSummaryPage"
import type { SprintBlocker } from "@/lib/types"

// The operator's first screen. Three questions, in the order they matter:
// what is running this minute, what needs a person, and how the project and
// its budget are pacing. Everything else is one destination away.

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

function SectionHeading({ id, children, aside }: { id: string; children: React.ReactNode; aside?: React.ReactNode }) {
  return (
    <div className="mb-3 flex items-baseline justify-between gap-3">
      <h2 id={id} className="text-sm font-semibold tracking-[-0.01em]">
        {children}
      </h2>
      {aside ? <span className="text-xs text-muted-foreground tabular-nums">{aside}</span> : null}
    </div>
  )
}

function WorkingRow({ d, now }: { d: NowDispatch; now: number }) {
  const started = new Date(d.started_at).getTime()
  const attempt =
    d.max_attempts > 0 ? t("now.attempt", { attempt: d.attempt, max: d.max_attempts }) : t("now.attempt_n", { attempt: d.attempt })
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-4 py-3 sm:grid-cols-[4.5rem_minmax(0,1fr)_auto]">
      <span className="hidden font-mono text-xs text-muted-foreground sm:block">{d.task_id}</span>
      <span className="min-w-0">
        <span className="block truncate text-sm font-medium">{d.title}</span>
        <span className="block truncate font-mono text-[11px] text-muted-foreground">
          <span className="sm:hidden">{d.task_id} · </span>
          {[d.provider, d.model, d.attempt > 1 || d.max_attempts > 0 ? attempt : null].filter(Boolean).join(" · ")}
        </span>
      </span>
      <span
        className="inline-flex items-center gap-2 font-mono text-sm tabular-nums text-status-running"
        title={Number.isNaN(started) ? undefined : fmt.dateTime(d.started_at)}
        aria-label={Number.isNaN(started) ? undefined : t("now.elapsed", { elapsed: formatElapsed(now - started) })}
      >
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-status-running motion-reduce:animate-none" aria-hidden />
        {Number.isNaN(started) ? "—" : formatElapsed(now - started)}
      </span>
    </li>
  )
}

function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard refused (insecure origin): the command is still selectable.
    }
  }
  return (
    <div className="mt-3 flex items-stretch gap-2">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap rounded-md border bg-muted px-2.5 py-2 font-mono text-xs">{command}</code>
      <Button type="button" size="sm" variant="outline" onClick={copy} aria-label={copied ? t("common.copied") : t("common.copy")}>
        {copied ? <Check className="h-4 w-4" aria-hidden /> : <Copy className="h-4 w-4" aria-hidden />}
      </Button>
    </div>
  )
}

function BlockedRow({ b, now }: { b: SprintBlocker; now: number }) {
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-4 py-3 sm:grid-cols-[4.5rem_minmax(0,1fr)_auto]">
      <span className="hidden font-mono text-xs text-muted-foreground sm:block">{b.task_id}</span>
      <span className="min-w-0">
        <span className="flex items-center gap-2">
          <OctagonPause className={cn("h-3.5 w-3.5 shrink-0", STATUS.blocked.text)} aria-label={t("status.blocked")} />
          <span className="truncate text-sm font-medium">{b.title}</span>
        </span>
        <span className="mt-0.5 block truncate text-xs text-muted-foreground" title={b.reason}>
          <span className="font-mono sm:hidden">{b.task_id} · </span>
          {b.reason}
          {b.blocked_at ? ` · ${t("now.blocked_since", { time: formatRelative(b.blocked_at, now) })}` : null}
        </span>
      </span>
      <Popover>
        <PopoverTrigger asChild>
          <Button type="button" size="sm" variant="outline">
            {t("now.unblock")}
          </Button>
        </PopoverTrigger>
        <PopoverContent align="end" className="w-[22rem]">
          <p className="font-medium">{t("now.unblock_title", { id: b.task_id })}</p>
          <p className="mt-1 text-muted-foreground">{t("now.unblock_detail")}</p>
          <CopyCommand command={`orch task set --id ${b.task_id} --status todo`} />
        </PopoverContent>
      </Popover>
    </li>
  )
}

function BudgetGauge({ row, now }: { row: BudgetRow; now: number }) {
  const pct = row.token_budget > 0 ? Math.min(100, (row.tokens_used / row.token_budget) * 100) : 0
  const tone = row.over_threshold ? "bg-status-blocked" : "bg-brand"
  const source = row.cost_source === "none" ? null : t(`now.cost.${row.cost_source}`)
  return (
    <li className="px-4 py-3">
      <div className="flex items-baseline justify-between gap-3 text-sm">
        <span className="font-medium">{row.provider}</span>
        <span className="font-mono text-xs tabular-nums text-muted-foreground">
          {t("now.tokens", { used: fmt.number(row.tokens_used), budget: fmt.number(row.token_budget) })}
        </span>
      </div>
      <div
        className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted"
        role="meter"
        aria-valuemin={0}
        aria-valuemax={row.token_budget}
        aria-valuenow={row.tokens_used}
        aria-label={row.provider}
      >
        <div className={cn("h-full rounded-full transition-[width]", tone)} style={{ width: `${pct}%` }} />
      </div>
      <p className="mt-1.5 flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
        <span>{t("now.window", { hours: row.window_hours })}</span>
        {row.raw_tokens_used !== row.tokens_used ? <span>· {t("now.tokens_raw", { raw: fmt.number(row.raw_tokens_used) })}</span> : null}
        {row.reset_at ? <span>· {t("now.resets", { time: formatRelative(row.reset_at, now) })}</span> : null}
        {source ? (
          <span>
            · {fmt.usd(row.cost_source === "estimated" ? row.estimated_cost_usd : row.cost_usd)} {source}
          </span>
        ) : null}
      </p>
    </li>
  )
}

const panel = "overflow-hidden rounded-xl border bg-card"

export function NowPage() {
  const now = useTicker()
  const { data, isLoading, isError, error } = useNow()
  const { status: streamStatus, lastEventAt } = useEventStream()
  const { data: phases } = useMilestones()
  const { data: budget } = useBudgetSummary()
  const { data: tasks } = useTasks()

  if (isLoading) {
    return (
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        <div className="space-y-4">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-48 w-full" />
        </div>
        <Skeleton className="h-72 w-full" />
      </div>
    )
  }

  if (isError || !data) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>{t("now.load_failed")}</AlertTitle>
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  const titles = new Map((tasks?.tasks ?? []).map((task) => [task.id, task.title]))
  const { summary, pace, run } = data
  const attentionCount = data.attention.length + data.waiting_for_budget.length

  return (
    <div className="space-y-8">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-[-0.02em]">{data.project || t("now.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {run
              ? run.status === "live"
                ? t("now.run_live", { id: run.run_id, time: formatRelative(run.started_at, now) })
                : t("now.run_done", { id: run.run_id, time: formatRelative(run.updated_at || run.started_at, now) })
              : null}
          </p>
        </div>
        <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
      </header>

      <div className="grid gap-8 xl:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        <div className="min-w-0 space-y-8">
          {/* The run line: progress, who is working, and when it lands. */}
          <section aria-label={t("now.title")} className="flex flex-wrap items-baseline gap-x-5 gap-y-2">
            <p className="flex items-baseline gap-2">
              <span className="text-4xl font-semibold tracking-[-0.03em] tabular-nums">
                {t("now.tasks_done", { done: summary.done, total: summary.total })}
              </span>
              <span className="text-sm text-muted-foreground">{t("now.tasks_label")}</span>
            </p>
            <p className="text-sm text-muted-foreground">
              {data.working.length > 0 ? t("now.agents_working", { count: data.working.length }) : t("now.no_agents")}
              {" · "}
              {pace.eta_date ? (
                <Explain term="eta">
                  <span>
                    {t("now.eta", { date: fmt.day(pace.eta_date) })}
                    {pace.blocked_count > 0 ? ` (${t("now.eta_excludes", { count: pace.blocked_count })})` : null}
                  </span>
                </Explain>
              ) : (
                t("now.no_eta")
              )}
            </p>
          </section>

          {!run ? (
            <p className="text-sm text-muted-foreground">
              {t("now.never_ran")} <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">orch run</code>
            </p>
          ) : null}

          <section aria-labelledby="now-working">
            <SectionHeading
              id="now-working"
              aside={
                data.slots.max > 0
                  ? t("now.slots", { used: data.slots.used, max: data.slots.max })
                  : t("now.slots_unknown", { used: data.slots.used, count: data.slots.used })
              }
            >
              {t("now.working")}
            </SectionHeading>
            {data.working.length > 0 ? (
              <ul className={cn(panel, "divide-y")}>
                {data.working.map((d) => (
                  <WorkingRow key={`${d.task_id}-${d.started_at}`} d={d} now={now} />
                ))}
              </ul>
            ) : (
              <p className="rounded-xl border border-dashed px-4 py-6 text-sm text-muted-foreground">{t("now.nothing_running")}</p>
            )}
          </section>

          <section aria-labelledby="now-attention">
            <SectionHeading id="now-attention" aside={attentionCount > 0 ? attentionCount : undefined}>
              {t("now.attention")}
            </SectionHeading>
            {attentionCount > 0 ? (
              <ul className={cn(panel, "divide-y")}>
                {data.attention.map((b) => (
                  <BlockedRow key={b.task_id} b={b} now={now} />
                ))}
                {data.waiting_for_budget.map((w) => (
                  <li key={w.task_id} className="grid grid-cols-1 items-center gap-3 px-4 py-3 sm:grid-cols-[4.5rem_minmax(0,1fr)]">
                    <span className="hidden font-mono text-xs text-muted-foreground sm:block">{w.task_id}</span>
                    <span className="min-w-0">
                      <span className="flex items-center gap-2">
                        <Hourglass className={cn("h-3.5 w-3.5 shrink-0", STATUS.blocked.text)} aria-hidden />
                        <span className="truncate text-sm font-medium">{titles.get(w.task_id) ?? w.task_id}</span>
                      </span>
                      <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                        {t("now.waiting_budget", { provider: w.provider, time: formatRelative(w.reset_at, now) })}
                      </span>
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="rounded-xl border border-dashed px-4 py-6 text-sm text-muted-foreground">{t("now.all_clear")}</p>
            )}
          </section>
        </div>

        <div className="min-w-0 space-y-8">
          <section aria-labelledby="now-phases">
            <SectionHeading id="now-phases">{t("now.phases")}</SectionHeading>
            <ul className={cn(panel, "space-y-3 p-4")}>
              {(phases ?? []).map((m) => (
                <li key={m.phase} className="grid grid-cols-[minmax(0,7.5rem)_minmax(0,1fr)_3rem] items-center gap-3">
                  <span className="truncate text-sm" title={m.name}>
                    {m.name}
                  </span>
                  <PhaseSegments
                    phase={{
                      phase: m.phase,
                      name: m.name,
                      total: m.progress.total,
                      done: m.progress.done,
                      in_progress: m.in_progress,
                      blocked: m.blocked,
                      backlog: 0,
                      estimate_hours: 0,
                      pct_done: m.progress.pct,
                    }}
                  />
                  <span className="text-right font-mono text-xs tabular-nums text-muted-foreground">
                    {m.progress.done}/{m.progress.total}
                  </span>
                </li>
              ))}
            </ul>
          </section>

          <section aria-labelledby="now-budget">
            <SectionHeading
              id="now-budget"
              aside={
                <Link to="/cost/budget" className="underline-offset-4 hover:text-foreground hover:underline">
                  {t("now.budget_details")}
                </Link>
              }
            >
              {budget?.preset ? `${t("now.budget")} · ${budget.preset}` : t("now.budget")}
            </SectionHeading>
            {budget?.available && budget.rows.length > 0 ? (
              <ul className={cn(panel, "divide-y")}>
                {budget.rows.map((row) => (
                  <BudgetGauge key={row.provider} row={row} now={now} />
                ))}
              </ul>
            ) : (
              <p className="rounded-xl border border-dashed px-4 py-6 text-sm text-muted-foreground">{t("now.budget_off")}</p>
            )}
          </section>
        </div>
      </div>
    </div>
  )
}
