import type { ReactNode } from "react"
import { AlertTriangle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { Explain } from "@/components/Explain"
import { StatusBadge } from "@/components/StatusBadge"
import { useTaskDetail } from "@/hooks/useTaskDetail"
import { fmt, t } from "@/i18n"
import type { Task } from "@/lib/types"

export interface TaskDetailModalProps {
  taskId: string | null
  onClose: () => void
}

function Meta({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-sm tabular-nums">{children}</dd>
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="border-t pt-4">
      <h3 className="mb-2 text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
}

function TaskBody({ task }: { task: Task }) {
  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-4">
        <Meta label={t("tasks.col.status")}>
          <StatusBadge status={task.status} />
        </Meta>
        <Meta label={t("tasks.col.phase")}>{task.phase}</Meta>
        <Meta label={<Explain term="estimate" />}>{t("task.hours", { hours: task.estimate_hours })}</Meta>
        <Meta label={<Explain term="agent_time" />}>{t("task.hours", { hours: task.human_hours })}</Meta>
        <Meta label={t("tasks.col.model")}>
          <span className="font-mono text-xs" title={task.model}>
            {task.model || "—"}
          </span>
        </Meta>
        <Meta label={<Explain term="downstream_impact" />}>{task.downstream_impact}</Meta>
        <Meta label={<Explain term="ready" />}>{task.parallelizable ? t("common.yes") : t("common.no")}</Meta>
        <Meta label={t("tasks.col.updated")}>{fmt.dateTime(task.last_updated)}</Meta>
      </dl>

      {task.description ? (
        <Section title={t("task.description")}>
          <p className="whitespace-pre-wrap text-sm leading-relaxed">{task.description}</p>
        </Section>
      ) : null}

      {task.reason ? (
        <Section title={t("task.reason")}>
          <p className="whitespace-pre-wrap text-sm leading-relaxed">{task.reason}</p>
        </Section>
      ) : null}

      {task.dependencies.length > 0 ? (
        <Section title={t("task.dependencies", { count: task.dependencies.length })}>
          <ul className="flex flex-wrap gap-1.5">
            {task.dependencies.map((d) => (
              <li key={d}>
                <Badge variant="outline" className="font-mono text-xs font-normal">
                  {d}
                </Badge>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}

      {task.files.length > 0 ? (
        <Section title={t("task.files", { count: task.files.length })}>
          <ul className="space-y-1 text-xs">
            {task.files.map((f) => (
              <li key={f} className="truncate rounded-md bg-muted px-2 py-1 font-mono">
                {f}
              </li>
            ))}
          </ul>
        </Section>
      ) : null}

      {task.spec_ref ? (
        <Section title={t("task.spec_ref")}>
          <span className="font-mono text-xs">{task.spec_ref}</span>
        </Section>
      ) : null}

      {task.comments.length > 0 ? (
        <Section title={t("task.comments", { count: task.comments.length })}>
          <pre className="max-h-40 overflow-auto rounded-md bg-muted p-2 text-xs">
            {JSON.stringify(task.comments, null, 2)}
          </pre>
        </Section>
      ) : null}
    </div>
  )
}

export function TaskDetailModal({ taskId, onClose }: TaskDetailModalProps) {
  const open = taskId !== null
  const { data, isLoading, isError, error } = useTaskDetail(taskId)

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
    >
      <DialogContent className="max-w-2xl">
        {isLoading ? (
          <div className="space-y-4">
            <Skeleton className="h-6 w-1/2" />
            <Skeleton className="h-4 w-2/3" />
            <div className="grid grid-cols-4 gap-3">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-10" />
              ))}
            </div>
            <Skeleton className="h-24 w-full" />
          </div>
        ) : isError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t("task.load_failed")}</AlertTitle>
            <AlertDescription>{error?.message ?? t("common.unknown_error")}</AlertDescription>
          </Alert>
        ) : data ? (
          <>
            <DialogHeader>
              <DialogDescription className="font-mono text-xs">{data.id}</DialogDescription>
              <DialogTitle className="pr-8 text-xl leading-snug">{data.title}</DialogTitle>
            </DialogHeader>
            <TaskBody task={data} />
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}
