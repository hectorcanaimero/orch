import { AlertTriangle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { useTaskDetail } from "@/hooks/useTaskDetail"
import { statusBadgeVariant } from "@/lib/status"
import type { Task } from "@/lib/types"

export interface TaskDetailModalProps {
  taskId: string | null
  onClose: () => void
}

function formatDate(iso: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

function MetaGrid({ task }: { task: Task }) {
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Phase
        </dt>
        <dd className="font-mono">{task.phase}</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Status
        </dt>
        <dd>
          <Badge variant={statusBadgeVariant(task.status)}>{task.status}</Badge>
        </dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Model
        </dt>
        <dd className="truncate font-mono text-xs">{task.model || "—"}</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Estimate
        </dt>
        <dd>{task.estimate_hours}h</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Human hours
        </dt>
        <dd>{task.human_hours}h</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Last updated
        </dt>
        <dd className="text-xs">{formatDate(task.last_updated)}</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Downstream impact
        </dt>
        <dd>{task.downstream_impact}</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-muted-foreground">
          Parallelizable
        </dt>
        <dd>{task.parallelizable ? "yes" : "no"}</dd>
      </div>
    </dl>
  )
}

function TaskBody({ task }: { task: Task }) {
  return (
    <div className="space-y-4">
      <MetaGrid task={task} />

      {task.description ? (
        <>
          <Separator />
          <section>
            <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Description
            </h3>
            <p className="whitespace-pre-wrap text-sm">{task.description}</p>
          </section>
        </>
      ) : null}

      {task.reason ? (
        <>
          <Separator />
          <section>
            <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Reason
            </h3>
            <p className="whitespace-pre-wrap text-sm">{task.reason}</p>
          </section>
        </>
      ) : null}

      {task.dependencies.length > 0 ? (
        <>
          <Separator />
          <section>
            <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Dependencies ({task.dependencies.length})
            </h3>
            <ul className="flex flex-wrap gap-1.5">
              {task.dependencies.map((d) => (
                <li key={d}>
                  <Badge variant="outline" className="font-mono text-xs">
                    {d}
                  </Badge>
                </li>
              ))}
            </ul>
          </section>
        </>
      ) : null}

      {task.files.length > 0 ? (
        <>
          <Separator />
          <section>
            <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Files ({task.files.length})
            </h3>
            <ul className="space-y-1 text-xs">
              {task.files.map((f) => (
                <li
                  key={f}
                  className="truncate rounded bg-zinc-100 px-2 py-1 font-mono"
                >
                  {f}
                </li>
              ))}
            </ul>
          </section>
        </>
      ) : null}

      {task.spec_ref ? (
        <>
          <Separator />
          <section className="text-xs text-muted-foreground">
            <span className="font-semibold uppercase tracking-wide">
              Spec ref:{" "}
            </span>
            <span className="font-mono">{task.spec_ref}</span>
          </section>
        </>
      ) : null}

      {task.comments.length > 0 ? (
        <>
          <Separator />
          <section>
            <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Comments ({task.comments.length})
            </h3>
            <pre className="max-h-40 overflow-auto rounded bg-zinc-100 p-2 text-xs">
              {JSON.stringify(task.comments, null, 2)}
            </pre>
          </section>
        </>
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
            <div className="grid grid-cols-2 gap-3">
              <Skeleton className="h-12" />
              <Skeleton className="h-12" />
              <Skeleton className="h-12" />
              <Skeleton className="h-12" />
            </div>
            <Skeleton className="h-24 w-full" />
          </div>
        ) : isError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>Failed to load task</AlertTitle>
            <AlertDescription>
              {error?.message ?? "Unknown error"}
            </AlertDescription>
          </Alert>
        ) : data ? (
          <>
            <DialogHeader>
              <DialogTitle className="pr-8">{data.title}</DialogTitle>
              <DialogDescription className="font-mono text-xs">
                [{data.phase}] {data.id}
              </DialogDescription>
            </DialogHeader>
            <TaskBody task={data} />
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}
