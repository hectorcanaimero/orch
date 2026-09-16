import { TaskCard } from "@/components/TaskCard"
import { t } from "@/i18n"
import { STATUS } from "@/lib/status"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

export type KanbanStatus = "backlog" | "todo" | "in_progress" | "blocked" | "done"

export interface KanbanColumnProps {
  title: string
  status: KanbanStatus
  tasks: Task[]
  taskStatusMap?: Record<string, string>
  onCardClick?: (taskId: string) => void
  showCriticalPath?: boolean
}

/**
 * A neutral lane: the status lives in the header (shape + color + count), not
 * in a tinted fill, so five columns side by side stay quiet and the cards
 * carry the attention.
 */
export function KanbanColumn({ title, status, tasks, taskStatusMap, onCardClick, showCriticalPath }: KanbanColumnProps) {
  const meta = STATUS[status]
  const Icon = meta.icon
  return (
    <section
      className={cn("flex h-full min-h-[200px] flex-col overflow-hidden rounded-xl border bg-muted/40")}
      aria-label={title}
    >
      <header className="flex flex-shrink-0 items-center justify-between gap-2 px-3 py-2.5">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Icon className={cn("h-4 w-4", meta.text)} aria-hidden />
          {title}
        </div>
        <span className="rounded-full bg-background px-2 py-0.5 text-xs font-medium tabular-nums text-muted-foreground">
          {tasks.length}
        </span>
      </header>
      <div className="min-h-0 flex-1 space-y-2 overflow-y-auto px-2 pb-2">
        {tasks.length === 0 ? (
          <p className="px-2 py-4 text-center text-xs text-muted-foreground">{t("kanban.empty_column")}</p>
        ) : (
          tasks.map((task) => (
            <TaskCard
              key={task.id}
              task={task}
              taskStatusMap={taskStatusMap}
              onClick={onCardClick}
              showCriticalPath={showCriticalPath}
            />
          ))
        )}
      </div>
    </section>
  )
}
