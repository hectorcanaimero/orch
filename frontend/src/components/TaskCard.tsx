import { GitBranch, Lock, Timer, Zap } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

export interface TaskCardProps {
  task: Task
  taskStatusMap?: Record<string, string>
  onClick?: (taskId: string) => void
}

const STATUS_ACCENT: Record<string, string> = {
  backlog:       "border-l-zinc-400",
  todo:          "border-l-sky-400",
  "in-progress": "border-l-violet-500",
  blocked:       "border-l-rose-500",
  done:          "border-l-emerald-500",
}

export function TaskCard({ task, taskStatusMap, onClick }: TaskCardProps) {
  const blockingDeps =
    taskStatusMap && task.status !== "done"
      ? task.dependencies.filter((id) => {
          const s = taskStatusMap[id]
          return s !== undefined && s !== "done"
        })
      : []

  const accentClass = STATUS_ACCENT[task.status] ?? "border-l-zinc-400"

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
        "group flex flex-col gap-2 rounded-md border border-l-4 border-zinc-200 bg-white p-3",
        "transition-all hover:border-zinc-300 hover:shadow-md",
        onClick
          ? "cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          : "cursor-default",
        accentClass,
      )}
    >
      {/* ID row */}
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-[10px] leading-none text-zinc-400">
          {task.id}
        </span>
        <div className="flex items-center gap-1">
          {task.parallelizable && (
            <GitBranch className="h-3 w-3 text-blue-400" aria-label="Parallelizable" />
          )}
          {task.on_critical_path && (
            <Zap className="h-3 w-3 text-red-400" aria-label="Critical path" />
          )}
        </div>
      </div>

      {/* Title */}
      <p className="line-clamp-3 text-sm font-medium leading-snug text-zinc-800">
        {task.title}
      </p>

      {/* Footer */}
      <div className="flex items-center justify-between gap-2 text-xs text-zinc-400">
        <div className="flex items-center gap-2">
          {task.estimate_hours != null && (
            <span className="inline-flex items-center gap-0.5">
              <Timer className="h-3 w-3" />
              {task.estimate_hours}h
            </span>
          )}
          {blockingDeps.length > 0 ? (
            <span className="inline-flex items-center gap-0.5 font-medium text-amber-500">
              <Lock className="h-3 w-3" />
              {blockingDeps.length} blocking
            </span>
          ) : task.dep_count > 0 ? (
            <span className="inline-flex items-center gap-0.5">
              <GitBranch className="h-3 w-3" />
              {task.dep_count}
            </span>
          ) : null}
        </div>
        {task.model && (
          <span className="max-w-[90px] truncate font-mono text-[10px] text-zinc-300">
            {task.model.includes("/") ? task.model.split("/").pop() : task.model}
          </span>
        )}
      </div>
    </div>
  )
}
