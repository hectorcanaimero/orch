import { GitBranch, Timer, Zap } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

export interface TaskCardProps {
  task: Task
  onClick?: (taskId: string) => void
}

export function TaskCard({ task, onClick }: TaskCardProps) {
  return (
    <Card
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
        "cursor-pointer transition-shadow hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        !onClick && "cursor-default",
      )}
    >
      <CardHeader className="space-y-1 p-4 pb-2">
        <div className="flex items-center justify-between gap-2">
          <span className="font-mono text-xs text-muted-foreground">
            [{task.phase}] {task.id}
          </span>
          {task.on_critical_path ? (
            <Badge variant="danger" className="gap-1">
              <Zap className="h-3 w-3" />
              critical
            </Badge>
          ) : null}
        </div>
        <h3 className="line-clamp-2 text-sm font-medium leading-snug">
          {task.title}
        </h3>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-2 p-4 pt-0 text-xs text-muted-foreground">
        {task.model ? (
          <Badge variant="muted" className="max-w-[140px] truncate">
            {task.model}
          </Badge>
        ) : null}
        <span className="inline-flex items-center gap-1">
          <GitBranch className="h-3 w-3" />
          {task.dep_count}
        </span>
        <span className="inline-flex items-center gap-1">
          <Timer className="h-3 w-3" />
          {task.estimate_hours}h
        </span>
      </CardContent>
    </Card>
  )
}
