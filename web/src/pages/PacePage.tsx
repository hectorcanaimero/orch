import { describeLoadError } from "@/lib/errors"
import { AlertTriangle, CalendarClock } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useSprintHealth } from "@/hooks/useSprintHealth"
import type { SprintBlocker } from "@/lib/types"

// Pace is the project's measured speed and the finish date it projects. The
// page used to be called "Sprint", but orch has no sprints: this is the whole
// project over a rolling 7-day window, and saying so is the point of the page.

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`

function Stat({ value, label }: { value: string | number; label: string }) {
  return (
    <div>
      <p className="text-lg font-semibold">{value}</p>
      <p className="text-xs text-muted-foreground">{label}</p>
    </div>
  )
}

function BlockerCard({ blocker }: { blocker: SprintBlocker }) {
  const blockedDate = blocker.blocked_at
    ? new Date(blocker.blocked_at).toLocaleDateString("en-US", { month: "short", day: "numeric" })
    : null

  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-rose-100 bg-rose-50/50 p-3">
      <div className="flex items-start justify-between gap-2">
        <div className="flex flex-col gap-0.5">
          <span className="text-sm font-medium text-zinc-800">{blocker.title}</span>
          <span className="font-mono text-[10px] text-zinc-400">{blocker.task_id}</span>
        </div>
        <Badge variant="outline" className="shrink-0 text-[10px]">
          Phase {blocker.phase}
        </Badge>
      </div>
      <p className="text-xs text-rose-700 line-clamp-2">{blocker.reason}</p>
      {blockedDate && <p className="text-[10px] text-muted-foreground">Blocked since {blockedDate}</p>}
    </div>
  )
}

export function PacePage() {
  const { data, isLoading, isError, error } = useSprintHealth()

  if (isLoading) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Pace</h1>
        <Skeleton className="h-44 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }

  if (isError || !data) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load the project's pace</AlertTitle>
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  const velocity = data.velocity_per_day > 0 ? data.velocity_per_day.toFixed(1) : "—"

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Pace</h1>
        <p className="text-sm text-muted-foreground">
          The whole project, measured over the last 7 days. orch has no sprints.
        </p>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-base">
            <CalendarClock className="h-4 w-4 text-violet-500" />
            Projected finish
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {data.eta_date ? (
            <div className="space-y-1">
              <div className="flex items-baseline gap-3">
                <span className="text-3xl font-bold tracking-tight">
                  {new Date(data.eta_date + "T12:00:00Z").toLocaleDateString("en-US", {
                    month: "long",
                    day: "numeric",
                  })}
                </span>
                {data.confidence !== "none" && (
                  <Badge variant={data.confidence === "high" ? "success" : "warning"}>
                    {data.confidence} confidence
                  </Badge>
                )}
              </div>
              <p className="text-sm text-muted-foreground">
                {data.eta_days != null && `in ~${data.eta_days} days · `}
                excludes {plural(data.blocked_count, "blocked task")}
              </p>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              No date yet: no task finished in the last 7 days, so there is no pace to project from.
            </p>
          )}

          <div className="grid grid-cols-2 gap-3 border-t pt-3 sm:grid-cols-4">
            <Stat value={data.done_count} label="done" />
            <Stat value={data.remaining_tasks} label="remaining (unblocked)" />
            <Stat value={data.blocked_count} label="blocked" />
            <Stat value={`${velocity}/day`} label="tasks finished" />
          </div>

          <div className="space-y-1 rounded-md bg-muted p-3 text-xs text-muted-foreground">
            <p className="font-medium text-foreground">How this is calculated</p>
            <p>Pace = tasks finished in the last 7 days ÷ 7 = {velocity} per day.</p>
            <p>
              Finish = {data.remaining_tasks} remaining tasks ÷ pace. Blocked tasks are left out because
              they cannot start.
            </p>
            <p>
              {data.remaining_hours}h of work remain by the plan's estimates — a sum of estimateHours,
              not measured time.
            </p>
          </div>
        </CardContent>
      </Card>

      <div className="space-y-3">
        <h2 className="text-lg font-semibold tracking-tight">
          Blocked
          {data.blocked_count > 0 && (
            <span className="ml-2 text-sm font-normal text-rose-500">
              {plural(data.blocked_count, "task")}
            </span>
          )}
        </h2>

        {data.blockers.length === 0 ? (
          <Card>
            <CardContent className="py-6 text-center text-sm text-muted-foreground">
              Nothing is blocked.
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            {data.blockers.map((b) => (
              <BlockerCard key={b.task_id} blocker={b} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
