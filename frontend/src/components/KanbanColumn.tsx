import { Badge } from "@/components/ui/badge"
import { TaskCard } from "@/components/TaskCard"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

export type KanbanStatus = "backlog" | "in_progress" | "blocked" | "done"

export interface KanbanColumnProps {
  title: string
  status: KanbanStatus
  tasks: Task[]
  onCardClick?: (taskId: string) => void
}

const STATUS_STYLES: Record<
  KanbanStatus,
  { bg: string; header: string; badge: Parameters<typeof Badge>[0]["variant"] }
> = {
  backlog: {
    bg: "bg-zinc-50",
    header: "text-zinc-700",
    badge: "muted",
  },
  in_progress: {
    bg: "bg-amber-50",
    header: "text-amber-900",
    badge: "warning",
  },
  blocked: {
    bg: "bg-red-50",
    header: "text-red-900",
    badge: "danger",
  },
  done: {
    bg: "bg-emerald-50",
    header: "text-emerald-900",
    badge: "success",
  },
}

export function KanbanColumn({
  title,
  status,
  tasks,
  onCardClick,
}: KanbanColumnProps) {
  const styles = STATUS_STYLES[status]
  return (
    <section
      className={cn(
        "flex min-h-[240px] flex-1 flex-col rounded-lg border",
        styles.bg,
      )}
      aria-label={title}
    >
      <header className="flex items-center justify-between border-b bg-white/60 px-3 py-2">
        <div className={cn("text-sm font-semibold", styles.header)}>
          {title}
        </div>
        <Badge variant={styles.badge}>{tasks.length}</Badge>
      </header>
      <div className="flex-1 space-y-2 overflow-y-auto p-2">
        {tasks.length === 0 ? (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">
            No tasks
          </p>
        ) : (
          tasks.map((t) => (
            <TaskCard key={t.id} task={t} onClick={onCardClick} />
          ))
        )}
      </div>
    </section>
  )
}
