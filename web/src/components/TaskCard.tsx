import { CircleCheck, CircleDashed, CircleMinus, CircleX, ExternalLink, GitBranch, Lock, Route, Timer } from "lucide-react"
import { StatusBadge } from "@/components/StatusBadge"
import { t } from "@/i18n"
import { cn } from "@/lib/utils"
import type { CiStatus, Task } from "@/lib/types"

const CI: Record<CiStatus, { label: "ci.pending" | "ci.success" | "ci.failure" | "ci.skipped"; icon: typeof CircleCheck; className: string }> = {
  pending: { label: "ci.pending", icon: CircleDashed, className: "text-status-running" },
  success: { label: "ci.success", icon: CircleCheck, className: "text-status-done" },
  failure: { label: "ci.failure", icon: CircleX, className: "text-status-failed" },
  skipped: { label: "ci.skipped", icon: CircleMinus, className: "text-muted-foreground" },
}

export interface TaskCardProps {
  task: Task
  taskStatusMap?: Record<string, string>
  onClick?: (taskId: string) => void
  /** Kanban columns already say the status; the card grid does not. */
  showStatus?: boolean
  /** Only when the project has branches — see criticalPathMatters. */
  showCriticalPath?: boolean
}

/** The one task card: kanban columns and the task list's card view. */
export function TaskCard({ task, taskStatusMap, onClick, showStatus = false, showCriticalPath = false }: TaskCardProps) {
  const blockingDeps =
    taskStatusMap && task.status !== "done"
      ? task.dependencies.filter((id) => {
          const s = taskStatusMap[id]
          return s !== undefined && s !== "done"
        })
      : []
  const ci = task.ci_status ? CI[task.ci_status] : undefined
  const model = task.model.includes("/") ? task.model.split("/").pop() : task.model

  return (
    <div
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick ? () => onClick(task.id) : undefined}
      onKeyDown={
        onClick
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault()
                onClick(task.id)
              }
            }
          : undefined
      }
      className={cn(
        "group flex flex-col gap-2 rounded-lg border bg-card p-3 text-card-foreground transition-colors",
        onClick
          ? "cursor-pointer hover:border-input hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          : "cursor-default",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs tabular-nums text-muted-foreground">{task.id}</span>
        <div className="flex items-center gap-1.5 text-muted-foreground">
          {task.parallelizable ? <GitBranch className="h-3.5 w-3.5 text-status-done" aria-label={t("task.ready")} /> : null}
          {showCriticalPath && task.on_critical_path ? (
            <Route className="h-3.5 w-3.5 text-foreground" aria-label={t("task.on_critical_path")} />
          ) : null}
        </div>
      </div>

      <p className="line-clamp-3 text-sm font-medium leading-snug">{task.title}</p>

      {task.pr_url || ci ? (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          {task.pr_url ? (
            <a
              href={task.pr_url}
              target="_blank"
              rel="noopener noreferrer"
              onClick={(e) => e.stopPropagation()}
              className="inline-flex items-center gap-1 rounded-md bg-muted px-1.5 py-0.5 font-medium text-muted-foreground hover:text-foreground"
            >
              <ExternalLink className="h-3 w-3" aria-hidden />
              {t("task.pr")}
            </a>
          ) : null}
          {ci ? (
            <span className={cn("inline-flex items-center gap-1 font-medium", ci.className)}>
              <ci.icon className="h-3 w-3" aria-hidden />
              {t(ci.label)}
            </span>
          ) : null}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1.5 text-xs text-muted-foreground">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          {showStatus ? <StatusBadge status={task.status} /> : null}
          {task.estimate_hours != null ? (
            <span className="inline-flex items-center gap-1 tabular-nums">
              <Timer className="h-3 w-3" aria-hidden />
              {t("task.hours", { hours: task.estimate_hours })}
            </span>
          ) : null}
          {blockingDeps.length > 0 ? (
            <span className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-status-blocked">
              <Lock className="h-3 w-3" aria-hidden />
              {t("task.waiting_on", { count: blockingDeps.length })}
            </span>
          ) : null}
        </div>
        {model ? (
          <span className="min-w-0 max-w-[110px] truncate font-mono text-[11px]" title={task.model}>
            {model}
          </span>
        ) : null}
      </div>
    </div>
  )
}
