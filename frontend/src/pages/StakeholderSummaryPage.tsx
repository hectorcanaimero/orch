import {
  AlertTriangle,
  CheckCircle2,
  Clock,
  DollarSign,
  Loader2,
  ListTodo,
  Timer,
  Ban,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { ProjectConfigWidget } from "@/components/ProjectConfigWidget"
import { useStakeholderSummary } from "@/hooks/useStakeholderSummary"
import type { StakeholderMilestone } from "@/lib/types"

const usdFormatter = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  maximumFractionDigits: 0,
})

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
  const pct =
    typeof milestone.percent_done === "number"
      ? Math.round(milestone.percent_done)
      : null
  return (
    <li className="flex flex-col gap-2 rounded-md border p-4">
      <div className="flex items-center justify-between">
        <div className="font-medium">{milestone.name}</div>
        {milestone.status ? (
          <span className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
            {milestone.status}
          </span>
        ) : null}
      </div>
      {pct !== null ? (
        <div className="flex items-center gap-3">
          <Progress value={pct} className="h-1.5 flex-1" />
          <span className="text-xs text-muted-foreground">{pct}%</span>
        </div>
      ) : null}
      {milestone.eta_hours != null ? (
        <div className="text-xs text-muted-foreground">
          ETA: {milestone.eta_hours}h
        </div>
      ) : null}
    </li>
  )
}

export function StakeholderSummaryPage() {
  const { data, isLoading, isError, error, isFetching } = useStakeholderSummary()

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-32 w-full" />
        <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
          <Skeleton className="h-24" />
          <Skeleton className="h-24" />
          <Skeleton className="h-24" />
          <Skeleton className="h-24" />
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
        <AlertDescription>
          {error?.message ?? "Unknown error"}
        </AlertDescription>
      </Alert>
    )
  }

  if (!data) return null

  const { project_id, summary, milestones, spend_rounded_usd, eta_hours, refresh_interval_s } =
    data

  const refreshSeconds =
    refresh_interval_s && refresh_interval_s > 0 ? refresh_interval_s : 10

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-semibold tracking-tight">{project_id}</h1>
          {isFetching ? (
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          ) : null}
        </div>
        <p className="text-sm text-muted-foreground">
          Refreshing every {refreshSeconds} seconds
        </p>
      </header>

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

      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard label="Done" value={summary.done} icon={CheckCircle2} tone="default" />
        <StatCard
          label="In progress"
          value={summary.in_progress}
          icon={Loader2}
          tone="warning"
        />
        <StatCard label="Blocked" value={summary.blocked} icon={Ban} tone="danger" />
        <StatCard label="Backlog" value={summary.backlog} icon={ListTodo} tone="muted" />
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardDescription className="text-xs uppercase tracking-wide">
              Spend
            </CardDescription>
            <DollarSign className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-semibold">
              {usdFormatter.format(spend_rounded_usd)}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardDescription className="text-xs uppercase tracking-wide">
              ETA
            </CardDescription>
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

      {milestones.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>Milestones</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-3">
              {milestones.map((m, i) => (
                <MilestoneRow key={m.id ?? `${m.name}-${i}`} milestone={m} />
              ))}
            </ul>
          </CardContent>
        </Card>
      ) : null}

      <ProjectConfigWidget />
    </div>
  )
}
