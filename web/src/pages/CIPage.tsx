import {
  AlertTriangle,
  ChevronRight,
  CircleCheck,
  CircleDashed,
  CircleX,
  ExternalLink,
  GitBranch,
  GitMerge,
  GitPullRequest,
  RotateCcw,
  ShieldCheck,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useCI } from "@/hooks/useCI"
import { useTasks } from "@/hooks/useTasks"
import { maxRetries, pipelineStages, prLabel, prTasks, type CiWorkflow, type Stage, type StageState } from "@/lib/ci"
import { describeLoadError } from "@/lib/errors"
import { t } from "@/i18n"
import { cn } from "@/lib/utils"
import type { CiStatus, Task } from "@/lib/types"

const STAGE_ICON: Record<Stage["key"], typeof GitBranch> = {
  branch: GitBranch,
  pr: GitPullRequest,
  checks: ShieldCheck,
  retry: RotateCcw,
  merge: GitMerge,
}

const STATE_PILL: Record<StageState, { label: "ci.stage.on" | "ci.stage.off" | "ci.stage.missing"; variant: "success" | "muted" | "warning" }> = {
  on: { label: "ci.stage.on", variant: "success" },
  off: { label: "ci.stage.off", variant: "muted" },
  missing: { label: "ci.stage.missing", variant: "warning" },
}

const CI_META: Record<CiStatus, { label: "ci.pending" | "ci.success" | "ci.failure" | "ci.skipped"; variant: "info" | "success" | "danger" | "muted"; icon: typeof CircleCheck }> = {
  pending: { label: "ci.pending", variant: "info", icon: CircleDashed },
  success: { label: "ci.success", variant: "success", icon: CircleCheck },
  failure: { label: "ci.failure", variant: "danger", icon: CircleX },
  skipped: { label: "ci.skipped", variant: "muted", icon: CircleDashed },
}

/**
 * CI (operator only): how a task's work gets from its agent to the base
 * branch in this project, as config.yaml and the repo's workflows set it up,
 * and where every open pull request is on that path right now.
 */
export function CIPage() {
  const ci = useCI()
  const tasks = useTasks()

  return (
    <div className="space-y-8">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("ci.title")}</h1>
        <p className="max-w-3xl text-sm text-muted-foreground">{t("ci.intro")}</p>
      </header>

      {ci.isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("ci.setup_failed")}</AlertTitle>
          <AlertDescription>{describeLoadError(ci.error)}</AlertDescription>
        </Alert>
      ) : null}

      {ci.data?.warnings.length ? (
        <Alert className="border-status-blocked/40 bg-status-blocked/10 [&>svg]:text-status-blocked">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("ci.will_not_work")}</AlertTitle>
          <AlertDescription>
            <ul className="mt-1 list-disc space-y-1 pl-4">
              {ci.data.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}

      <section aria-labelledby="pipeline-heading" className="space-y-3">
        <h2 id="pipeline-heading" className="text-lg font-semibold">
          {t("ci.pipeline")}
        </h2>
        {ci.isLoading ? (
          <Skeleton className="h-44 w-full" />
        ) : ci.data ? (
          <ol className="grid gap-3 md:grid-cols-5">
            {pipelineStages(ci.data).map((stage, i, all) => (
              <StageCard key={stage.key} stage={stage} last={i === all.length - 1} />
            ))}
          </ol>
        ) : null}
      </section>

      <section aria-labelledby="prs-heading" className="space-y-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="prs-heading" className="text-lg font-semibold">
            {t("ci.pull_requests")}
          </h2>
          {tasks.data ? <PrCountsLine tasks={tasks.data.tasks} /> : null}
        </div>
        {tasks.isError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t("tasks.load_failed")}</AlertTitle>
            <AlertDescription>{describeLoadError(tasks.error)}</AlertDescription>
          </Alert>
        ) : tasks.isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : tasks.data ? (
          <PrTable tasks={tasks.data.tasks} retries={ci.data ? maxRetries(ci.data.config) : undefined} />
        ) : null}
      </section>

      <section aria-labelledby="workflows-heading" className="space-y-3">
        <h2 id="workflows-heading" className="text-lg font-semibold">
          {t("ci.workflows")}
        </h2>
        {ci.data && ci.data.workflows.length === 0 ? (
          <p className="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">
            {t("ci.no_workflows_before")} <code className="font-mono">.github/workflows</code>.{" "}
            <code className="font-mono">orch init</code> {t("ci.no_workflows_writes")} <code className="font-mono">orch-ci.yml</code>{" "}
            {t("ci.no_workflows_after")}
          </p>
        ) : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {ci.data?.workflows.map((w) => (
            <WorkflowCard key={w.file} workflow={w} />
          ))}
        </div>
      </section>
    </div>
  )
}

function StageCard({ stage, last }: { stage: Stage; last: boolean }) {
  const Icon = STAGE_ICON[stage.key]
  const pill = STATE_PILL[stage.state]
  return (
    <li className="relative">
      <Card className={cn("h-full", stage.state === "off" && "bg-muted/40", stage.state === "missing" && "border-status-blocked/60")}>
        <CardHeader className="space-y-2 p-4 pb-2">
          <div className="flex items-center justify-between gap-2">
            <Icon className={cn("h-5 w-5", stage.state === "off" ? "text-muted-foreground" : "text-foreground")} aria-hidden />
            <Badge variant={pill.variant}>{t(pill.label)}</Badge>
          </div>
          <CardTitle className="text-sm">{stage.title}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 p-4 pt-0">
          <p className="text-sm text-muted-foreground">{stage.detail}</p>
          <dl className="space-y-1">
            {stage.settings.map((s) => (
              <div key={s.label} className="flex flex-wrap justify-between gap-x-2 text-xs">
                <dt className="font-mono text-muted-foreground">{s.label}</dt>
                <dd className="break-all font-mono text-foreground">{s.value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
      {last ? null : (
        <ChevronRight
          className="absolute -right-3 top-1/2 z-10 hidden h-4 w-4 -translate-y-1/2 text-muted-foreground md:block"
          aria-hidden
        />
      )}
    </li>
  )
}

function PrCountsLine({ tasks }: { tasks: Task[] }) {
  const { active, finished, failing } = prTasks(tasks)
  if (active.length + finished.length === 0) return null
  return (
    <p className="text-sm text-muted-foreground tabular-nums">
      {t("ci.counts", { active: active.length, failing, finished: finished.length })}
    </p>
  )
}

function PrTable({ tasks, retries }: { tasks: Task[]; retries?: number }) {
  const { active, finished } = prTasks(tasks)
  return (
    <div className="space-y-3">
      {active.length === 0 ? (
        <p className="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">
          {t("ci.no_prs_before")} <code className="font-mono">vcs.auto_pr</code> {t("ci.no_prs_after")}
        </p>
      ) : (
        <PrRows rows={active} retries={retries} showStatus />
      )}
      {finished.length > 0 ? (
        <details className="group rounded-lg border">
          <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium">
            {t("ci.finished_through_pr", { count: finished.length })}
          </summary>
          <div className="border-t">
            <PrRows rows={finished} retries={retries} bare />
          </div>
        </details>
      ) : null}
    </div>
  )
}

const TASK_STATUS: Record<string, "ci.task.in_review" | "ci.task.blocked" | "ci.task.retrying" | "ci.task.done"> = {
  "in-progress": "ci.task.in_review",
  blocked: "ci.task.blocked",
  todo: "ci.task.retrying",
  done: "ci.task.done",
}

function PrRows({ rows, retries, showStatus, bare }: { rows: Task[]; retries?: number; showStatus?: boolean; bare?: boolean }) {
  return (
    <div className={cn("overflow-x-auto", !bare && "rounded-lg border")}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("ci.col.task")}</TableHead>
            {showStatus ? <TableHead>{t("ci.col.task_status")}</TableHead> : null}
            <TableHead>{t("ci.col.last_ci")}</TableHead>
            <TableHead className="text-right">{t("ci.col.retries")}</TableHead>
            <TableHead className="text-right">{t("task.pr")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((task) => {
            const meta = CI_META[task.ci_status ?? "pending"]
            const StatusIcon = meta.icon
            return (
              <TableRow key={task.id}>
                <TableCell>
                  <div className="font-mono text-xs text-muted-foreground">{task.id}</div>
                  <div className="text-sm">{task.title}</div>
                </TableCell>
                {showStatus ? <TableCell className="text-sm">{TASK_STATUS[task.status] ? t(TASK_STATUS[task.status]) : task.status}</TableCell> : null}
                <TableCell>
                  <Badge variant={meta.variant}>
                    <StatusIcon className="h-3 w-3" aria-hidden />
                    {t(meta.label)}
                  </Badge>
                </TableCell>
                <TableCell className="text-right font-mono text-sm tabular-nums">
                  {task.ci_attempts ?? 0}
                  {retries !== undefined ? ` / ${retries}` : ""}
                </TableCell>
                <TableCell className="text-right">
                  <a
                    href={task.pr_url ?? undefined}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 font-mono text-sm hover:underline"
                  >
                    {prLabel(task.pr_url ?? "")}
                    <ExternalLink className="h-3 w-3" aria-hidden />
                  </a>
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}

function WorkflowCard({ workflow }: { workflow: CiWorkflow }) {
  return (
    <Card>
      <CardHeader className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle className="text-base">{workflow.name || workflow.file}</CardTitle>
          {workflow.runs_on_pull_requests ? <Badge variant="success">{t("ci.runs_on_prs")}</Badge> : <Badge variant="muted">{t("ci.not_on_prs")}</Badge>}
        </div>
        <CardDescription className="flex flex-wrap items-center gap-2">
          <code className="font-mono text-xs">.github/workflows/{workflow.file}</code>
          {workflow.triggers.map((trigger) => (
            <Badge key={trigger} variant="outline" className="font-mono">
              {trigger}
            </Badge>
          ))}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {workflow.parse_error ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t("ci.unreadable")}</AlertTitle>
            <AlertDescription className="font-mono text-xs">{workflow.parse_error}</AlertDescription>
          </Alert>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            {workflow.jobs.map((job) => (
              <div key={job.id} className="rounded-lg bg-muted/50 p-3">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-sm font-medium">{job.name || job.id}</span>
                  {job.needs.length ? <span className="text-xs text-muted-foreground">{t("ci.after", { jobs: job.needs.join(", ") })}</span> : null}
                </div>
                {job.parse_error ? <p className="mt-2 font-mono text-xs text-status-failed">{job.parse_error}</p> : null}
                <ol className="mt-2 space-y-1">
                  {job.steps.map((step, i) => (
                    <li key={`${i}-${step}`} className="flex gap-2 text-xs">
                      <span className="w-4 shrink-0 text-right font-mono text-muted-foreground tabular-nums">{i + 1}</span>
                      <span className="break-all font-mono text-foreground/85">{step}</span>
                    </li>
                  ))}
                </ol>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
