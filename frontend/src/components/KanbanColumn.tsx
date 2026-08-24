import { Badge } from "@/components/ui/badge"
import { TaskCard } from "@/components/TaskCard"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

export type KanbanStatus = "backlog" | "todo" | "in_progress" | "blocked" | "done"

export interface KanbanColumnProps {
  title: string
  status: KanbanStatus
  tasks: Task[]
  taskStatusMap?: Record<string, string>
  onCardClick?: (taskId: string) => void
}

const STATUS_STYLES: Record<
  KanbanStatus,
  {
    bg: string
    header: string
    badge: Parameters<typeof Badge>[0]["variant"]
    topBorder: string
  }
> = {
  backlog: {
    bg: "bg-zinc-50",
    header: "text-zinc-500",
    badge: "muted",
    topBorder: "border-t-zinc-400",
  },
  todo: {
    bg: "bg-sky-50",
    header: "text-sky-700",
    badge: "info",
    topBorder: "border-t-sky-400",
  },
  in_progress: {
    bg: "bg-violet-50",
    header: "text-violet-700",
    badge: "warning",
    topBorder: "border-t-violet-500",
  },
  blocked: {
    bg: "bg-rose-50",
    header: "text-rose-700",
    badge: "danger",
    topBorder: "border-t-rose-500",
  },
  done: {
    bg: "bg-emerald-50",
    header: "text-emerald-700",
    badge: "success",
    topBorder: "border-t-emerald-500",
  },
}

export function KanbanColumn({
  title,
  status,
  tasks,
  taskStatusMap,
  onCardClick,
}: KanbanColumnProps) {
  const styles = STATUS_STYLES[status]
  return (
    <section
      className={cn(
        "flex h-full min-h-[240px] flex-col overflow-hidden rounded-lg",
        "border border-zinc-200 border-t-4",
        styles.bg,
        styles.topBorder,
      )}
      aria-label={title}
    >
      <header className="flex flex-shrink-0 items-center justify-between border-b border-zinc-200 bg-white/70 px-3 py-2.5">
        <div className={cn("text-xs font-semibold uppercase tracking-wider", styles.header)}>
          {title}
        </div>
        <Badge variant={styles.badge}>{tasks.length}</Badge>
      </header>
      <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-2">
        {tasks.length === 0 ? (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">
            No tasks
          </p>
        ) : (
          tasks.map((t) => (
            <TaskCard key={t.id} task={t} taskStatusMap={taskStatusMap} onClick={onCardClick} />
          ))
        )}
      </div>
    </section>
  )
}
