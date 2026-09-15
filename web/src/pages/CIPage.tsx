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
import { cn } from "@/lib/utils"
import type { CiStatus, Task } from "@/lib/types"

const STAGE_ICON: Record<Stage["key"], typeof GitBranch> = {
  branch: GitBranch,
  pr: GitPullRequest,
  checks: ShieldCheck,
  retry: RotateCcw,
  merge: GitMerge,
}

const STATE_PILL: Record<StageState, { label: string; variant: "success" | "muted" | "warning" }> = {
  on: { label: "On", variant: "success" },
  off: { label: "Off", variant: "muted" },
  missing: { label: "Missing", variant: "warning" },
}

const CI_META: Record<CiStatus, { label: string; variant: "warning" | "success" | "danger" | "muted"; icon: typeof CircleCheck }> = {
  pending: { label: "Running", variant: "warning", icon: CircleDashed },
  success: { label: "Passed", variant: "success", icon: CircleCheck },
  failure: { label: "Failed", variant: "danger", icon: CircleX },
  skipped: { label: "No checks", variant: "muted", icon: CircleDashed },
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
        <h1 className="text-2xl font-semibold tracking-tight">CI</h1>
        <p className="max-w-3xl text-sm text-muted-foreground">
          What happens to a task's work after its agent finishes: the pipeline as this project is configured, and the
          pull requests on it now.
        </p>
      </header>

      {ci.isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Couldn't read the CI setup</AlertTitle>
          <AlertDescription>{describeLoadError(ci.error)}</AlertDescription>
        </Alert>
      ) : null}

      {ci.data?.warnings.length ? (
        <Alert className="border-amber-300 bg-amber-50 text-amber-900">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>This setup will not work as it reads</AlertTitle>
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
          Pipeline
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
            Pull requests
          </h2>
          {tasks.data ? <PrCountsLine tasks={tasks.data.tasks} /> : null}
        </div>
        {tasks.isError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>Couldn't load the tasks</AlertTitle>
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
          Workflows
        </h2>
        {ci.data && ci.data.workflows.length === 0 ? (
          <p className="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">
            No files under <code className="font-mono">.github/workflows</code>. <code className="font-mono">orch init</code>{" "}
            writes <code className="font-mono">orch-ci.yml</code> for a new project.
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
      <Card className={cn("h-full", stage.state === "off" && "bg-zinc-50", stage.state === "missing" && "border-amber-300")}>
        <CardHeader className="space-y-2 p-4 pb-2">
          <div className="flex items-center justify-between gap-2">
            <Icon className={cn("h-5 w-5", stage.state === "off" ? "text-zinc-400" : "text-zinc-800")} aria-hidden />
            <Badge variant={pill.variant}>{pill.label}</Badge>
          </div>
          <CardTitle className="text-sm">{stage.title}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 p-4 pt-0">
          <p className="text-sm text-muted-foreground">{stage.detail}</p>
          <dl className="space-y-1">
            {stage.settings.map((s) => (
              <div key={s.label} className="flex flex-wrap justify-between gap-x-2 text-xs">
                <dt className="font-mono text-muted-foreground">{s.label}</dt>
                <dd className="break-all font-mono text-zinc-800">{s.value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
      {last ? null : (
        <ChevronRight
          className="absolute -right-3 top-1/2 z-10 hidden h-4 w-4 -translate-y-1/2 text-zinc-400 md:block"
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
      {active.length} under review · {failing} failing · {finished.length} finished
    </p>
  )
}

function PrTable({ tasks, retries }: { tasks: Task[]; retries?: number }) {
  const { active, finished } = prTasks(tasks)
  return (
    <div className="space-y-3">
      {active.length === 0 ? (
        <p className="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">
          No task is waiting on a pull request right now. They appear here when a task finishes with{" "}
          <code className="font-mono">vcs.auto_pr</code> on.
        </p>
      ) : (
        <PrRows rows={active} retries={retries} showStatus />
      )}
      {finished.length > 0 ? (
        <details className="group rounded-lg border">
          <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium">
            {finished.length} finished {finished.length === 1 ? "task" : "tasks"} went through a pull request
          </summary>
          <div className="border-t">
            <PrRows rows={finished} retries={retries} bare />
          </div>
        </details>
      ) : null}
    </div>
  )
}

const TASK_STATUS: Record<string, string> = {
  "in-progress": "In review",
  blocked: "Blocked",
  todo: "Retrying",
  done: "Done",
}

function PrRows({ rows, retries, showStatus, bare }: { rows: Task[]; retries?: number; showStatus?: boolean; bare?: boolean }) {
  return (
    <div className={cn("overflow-x-auto", !bare && "rounded-lg border")}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Task</TableHead>
            {showStatus ? <TableHead>Task status</TableHead> : null}
            <TableHead>Last CI</TableHead>
            <TableHead className="text-right">Retries</TableHead>
            <TableHead className="text-right">PR</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((t) => {
            const meta = CI_META[t.ci_status ?? "pending"]
            const StatusIcon = meta.icon
            return (
              <TableRow key={t.id}>
                <TableCell>
                  <div className="font-mono text-xs text-muted-foreground">{t.id}</div>
                  <div className="text-sm">{t.title}</div>
                </TableCell>
                {showStatus ? <TableCell className="text-sm">{TASK_STATUS[t.status] ?? t.status}</TableCell> : null}
                <TableCell>
                  <Badge variant={meta.variant}>
                    <StatusIcon className="h-3 w-3" aria-hidden />
                    {meta.label}
                  </Badge>
                </TableCell>
                <TableCell className="text-right font-mono text-sm tabular-nums">
                  {t.ci_attempts ?? 0}
                  {retries !== undefined ? ` / ${retries}` : ""}
                </TableCell>
                <TableCell className="text-right">
                  <a
                    href={t.pr_url ?? undefined}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 font-mono text-sm hover:underline"
                  >
                    {prLabel(t.pr_url ?? "")}
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
          {workflow.runs_on_pull_requests ? <Badge variant="success">Runs on PRs</Badge> : <Badge variant="muted">Not on PRs</Badge>}
        </div>
        <CardDescription className="flex flex-wrap items-center gap-2">
          <code className="font-mono text-xs">.github/workflows/{workflow.file}</code>
          {workflow.triggers.map((t) => (
            <Badge key={t} variant="outline" className="font-mono">
              {t}
            </Badge>
          ))}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {workflow.parse_error ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>GitHub cannot read this file</AlertTitle>
            <AlertDescription className="font-mono text-xs">{workflow.parse_error}</AlertDescription>
          </Alert>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            {workflow.jobs.map((job) => (
              <div key={job.id} className="rounded-md border bg-zinc-50/60 p-3">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-sm font-medium">{job.name || job.id}</span>
                  {job.needs.length ? <span className="text-xs text-muted-foreground">after {job.needs.join(", ")}</span> : null}
                </div>
                {job.parse_error ? <p className="mt-2 font-mono text-xs text-red-700">{job.parse_error}</p> : null}
                <ol className="mt-2 space-y-1">
                  {job.steps.map((step, i) => (
                    <li key={`${i}-${step}`} className="flex gap-2 text-xs">
                      <span className="w-4 shrink-0 text-right font-mono text-muted-foreground tabular-nums">{i + 1}</span>
                      <span className="break-all font-mono text-zinc-800">{step}</span>
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
