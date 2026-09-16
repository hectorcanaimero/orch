import { describeLoadError } from "@/lib/errors"
import { AlertTriangle, Clock } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { Explain } from "@/components/Explain"
import { useMilestones, type Milestone } from "@/hooks/useMilestones"
import { fmt, t, type MessageKey } from "@/i18n"
import { STATUS } from "@/lib/status"

// A milestone's state borrows the task status scale: done is done, active runs,
// not started waits in the queue.
const MILESTONE_STATUS: Record<
  Milestone["status"],
  { label: MessageKey; variant: "success" | "info" | "muted"; icon: (typeof STATUS)["done"]["icon"] }
> = {
  done: { label: "milestones.status.done", variant: "success", icon: STATUS.done.icon },
  active: { label: "milestones.status.active", variant: "info", icon: STATUS.in_progress.icon },
  pending: { label: "milestones.status.pending", variant: "muted", icon: STATUS.backlog.icon },
}

function Intro() {
  return (
    <header className="space-y-1">
      <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("milestones.title")}</h1>
      <p className="max-w-3xl text-sm text-muted-foreground">{t("milestones.intro")}</p>
    </header>
  )
}

function MilestoneRow({ milestone: m }: { milestone: Milestone }) {
  const status = MILESTONE_STATUS[m.status]
  const Icon = status.icon
  return (
    <li className="space-y-2.5 py-4 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="min-w-0 text-base font-semibold">{m.name}</h2>
        <Badge variant={status.variant} className="shrink-0">
          <Icon className="h-3 w-3" aria-hidden />
          {t(status.label)}
        </Badge>
      </div>
      <div className="flex items-center gap-3">
        <Progress value={m.progress.pct} className="h-2 flex-1" />
        <span className="w-10 text-right text-sm font-medium tabular-nums">{m.progress.pct}%</span>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span className="tabular-nums">
          {t("milestones.tasks_done", { done: m.progress.done, total: m.progress.total })}
          {m.blocked > 0 ? (
            <span className="text-status-blocked"> · {t("milestones.blocked", { count: m.blocked })}</span>
          ) : null}
        </span>
        {m.status !== "done" ? (
          <span className="inline-flex items-center gap-1.5">
            <Clock className="h-3.5 w-3.5" aria-hidden />
            {m.eta ? (
              <Explain term="eta">
                {t("milestones.eta", {
                  date: fmt.day(m.eta.eta_date),
                  confidence: t(m.eta.confidence === "high" ? "milestones.confidence.high" : "milestones.confidence.low"),
                })}
              </Explain>
            ) : (
              t("milestones.no_eta")
            )}
          </span>
        ) : null}
      </div>
    </li>
  )
}

export function MilestonesPage() {
  const { data: milestones, isLoading, isError, error } = useMilestones()

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Intro />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>{t("milestones.load_failed")}</AlertTitle>
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  if (!milestones || milestones.length === 0) {
    return (
      <div className="space-y-6">
        <Intro />
        <div className="rounded-xl border border-dashed p-6 text-sm text-muted-foreground">
          <p className="font-medium text-foreground">{t("milestones.none")}</p>
          <p className="mt-1">
            {t("milestones.none_before")} <code className="font-mono">orch atomize --apply</code>.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <Intro />
      <section className="rounded-xl border bg-card p-5" aria-label={t("milestones.title")}>
        <ul className="divide-y">
          {milestones.map((m) => (
            <MilestoneRow key={m.phase} milestone={m} />
          ))}
        </ul>
      </section>
    </div>
  )
}
