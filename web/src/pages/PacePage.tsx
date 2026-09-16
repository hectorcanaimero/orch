import type { ReactNode } from "react"
import { describeLoadError } from "@/lib/errors"
import { AlertTriangle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Explain } from "@/components/Explain"
import { useSprintHealth } from "@/hooks/useSprintHealth"
import { fmt, t } from "@/i18n"
import { STATUS } from "@/lib/status"
import type { SprintBlocker } from "@/lib/types"

// Pace is the project's measured speed and the finish date it projects. The
// page used to be called "Sprint", but orch has no sprints: this is the whole
// project over a rolling 7-day window, and saying so is the point of the page.

function Stat({ value, label }: { value: string | number; label: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col-reverse">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-lg font-semibold tabular-nums">{value}</dd>
    </div>
  )
}

function BlockerRow({ blocker }: { blocker: SprintBlocker }) {
  const Icon = STATUS.blocked.icon
  return (
    <li className="grid grid-cols-[1rem_minmax(0,1fr)] gap-3 py-3 first:pt-0 last:pb-0">
      <Icon className="mt-0.5 h-4 w-4 text-status-blocked" aria-hidden />
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium">{blocker.title}</span>
          <Badge variant="outline" className="shrink-0">
            {t("filters.phase", { phase: blocker.phase })}
          </Badge>
        </div>
        <p className="font-mono text-xs text-muted-foreground">{blocker.task_id}</p>
        {blocker.reason ? <p className="line-clamp-2 text-sm text-foreground/85">{blocker.reason}</p> : null}
        {blocker.blocked_at ? (
          <p className="text-xs text-muted-foreground">{t("pace.blocked_since", { date: fmt.day(blocker.blocked_at) })}</p>
        ) : null}
      </div>
    </li>
  )
}

export function PacePage() {
  const { data, isLoading, isError, error } = useSprintHealth()

  if (isLoading) {
    return (
      <div className="space-y-6">
        <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("pace.title")}</h1>
        <Skeleton className="h-52 w-full rounded-xl" />
        <Skeleton className="h-32 w-full rounded-xl" />
      </div>
    )
  }

  if (isError || !data) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>{t("pace.load_failed")}</AlertTitle>
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  const velocity = data.velocity_per_day > 0 ? data.velocity_per_day.toFixed(1) : "—"

  return (
    <div className="space-y-8">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("pace.title")}</h1>
        <p className="max-w-3xl text-sm text-muted-foreground">{t("pace.intro")}</p>
      </header>

      <section className="space-y-5 rounded-xl border bg-card p-5" aria-labelledby="finish-heading">
        <h2 id="finish-heading" className="text-base font-semibold">
          <Explain term="eta">{t("pace.projected_finish")}</Explain>
        </h2>
        {data.eta_date ? (
          <div className="space-y-1">
            <div className="flex flex-wrap items-baseline gap-3">
              <span className="text-3xl font-semibold tracking-[-0.02em] tabular-nums">{fmt.day(data.eta_date)}</span>
              {data.confidence !== "none" ? (
                <Badge variant={data.confidence === "high" ? "success" : "warning"}>
                  {t(data.confidence === "high" ? "pace.confidence.high" : "pace.confidence.low")}
                </Badge>
              ) : null}
            </div>
            <p className="text-sm text-muted-foreground">
              {data.eta_days != null ? `${t("pace.in_days", { count: data.eta_days })} · ` : null}
              {t("pace.excludes_blocked", { count: data.blocked_count })}
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t("pace.no_date")}</p>
        )}

        <dl className="grid grid-cols-2 gap-4 border-t pt-4 sm:grid-cols-4">
          <Stat value={data.done_count} label={t("pace.stat.done")} />
          <Stat value={data.remaining_tasks} label={t("pace.stat.remaining")} />
          <Stat value={data.blocked_count} label={t("pace.stat.blocked")} />
          <Stat
            value={t("pace.per_day", { value: velocity })}
            label={<Explain term="velocity">{t("pace.stat.velocity")}</Explain>}
          />
        </dl>

        <div className="space-y-1 rounded-lg bg-muted p-3 text-xs text-muted-foreground">
          <p className="font-medium text-foreground">{t("pace.how")}</p>
          <p>{t("pace.how_pace", { value: velocity })}</p>
          <p>{t("pace.how_finish", { count: data.remaining_tasks })}</p>
          <p>{t("pace.how_hours", { hours: data.remaining_hours })}</p>
        </div>
      </section>

      <section className="space-y-3" aria-labelledby="blocked-heading">
        <h2 id="blocked-heading" className="flex items-baseline gap-2 text-lg font-semibold">
          {t("pace.blocked")}
          {data.blocked_count > 0 ? (
            <span className="text-sm font-normal text-status-blocked tabular-nums">
              {t("pace.blocked_count", { count: data.blocked_count })}
            </span>
          ) : null}
        </h2>
        {data.blockers.length === 0 ? (
          <p className="rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground">
            {t("pace.none_blocked")}
          </p>
        ) : (
          <ul className="divide-y rounded-xl border bg-card p-5">
            {data.blockers.map((b) => (
              <BlockerRow key={b.task_id} blocker={b} />
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
