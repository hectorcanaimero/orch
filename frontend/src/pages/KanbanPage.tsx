import { useMemo, useState } from "react"
import { AlertTriangle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Skeleton } from "@/components/ui/skeleton"
import { KanbanColumn, type KanbanStatus } from "@/components/KanbanColumn"
import { LiveStatusPill } from "@/components/LiveStatusPill"
import {
  TaskFiltersBar,
  toTaskFilters,
  useDebouncedValue,
  type TaskFiltersBarValue,
} from "@/components/TaskFiltersBar"
import { TaskDetailModal } from "@/components/TaskDetailModal"
import { useEventStream } from "@/hooks/useEventStream"
import { useTasks } from "@/hooks/useTasks"
import type { Task } from "@/lib/types"

const STATUS_TO_COLUMN: Record<string, KanbanStatus> = {
  backlog: "backlog",
  todo: "backlog",
  in_progress: "in_progress",
  blocked: "blocked",
  done: "done",
}

const COLUMNS: { title: string; status: KanbanStatus }[] = [
  { title: "Backlog", status: "backlog" },
  { title: "In progress", status: "in_progress" },
  { title: "Blocked", status: "blocked" },
  { title: "Done", status: "done" },
]

function groupTasks(tasks: Task[]): Record<KanbanStatus, Task[]> {
  const buckets: Record<KanbanStatus, Task[]> = {
    backlog: [],
    in_progress: [],
    blocked: [],
    done: [],
  }
  for (const t of tasks) {
    const col = STATUS_TO_COLUMN[t.status] ?? "backlog"
    buckets[col].push(t)
  }
  return buckets
}

export function KanbanPage() {
  const [filters, setFilters] = useState<TaskFiltersBarValue>({
    phase: "",
    model: "",
    search: "",
  })
  const debouncedSearch = useDebouncedValue(filters.search, 250)

  const [openTaskId, setOpenTaskId] = useState<string | null>(null)

  const backendFilters = useMemo(
    () =>
      toTaskFilters({
        phase: filters.phase,
        model: filters.model,
        search: debouncedSearch,
      }),
    [filters.phase, filters.model, debouncedSearch],
  )

  const { data, isLoading, isError, error } = useTasks(backendFilters)

  // Derive model options from the (unfiltered) task list — but we only have
  // the filtered list here. Good enough: model options come from whatever is
  // currently visible. If the user clears filters, more models will appear.
  const modelOptions = useMemo(() => {
    if (!data) return [] as string[]
    const s = new Set<string>()
    for (const t of data.tasks) if (t.model) s.add(t.model)
    return Array.from(s).sort()
  }, [data])

  const phaseOptions = useMemo(() => {
    if (!data) return [] as number[]
    const s = new Set<number>()
    for (const t of data.tasks) s.add(t.phase)
    return Array.from(s).sort((a, b) => a - b)
  }, [data])

  // Kick off the SSE stream (invalidates the `tasks` query cache on events).
  // The pill in the header consumes this same result via props so we only
  // open one connection per page mount.
  const { status: streamStatus, lastEventAt } = useEventStream()

  const grouped = data ? groupTasks(data.tasks) : null

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Kanban</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? `${data.summary.done}/${data.summary.total} tasks done`
              : "Loading tasks…"}
          </p>
        </div>
        <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
      </header>

      <TaskFiltersBar
        value={filters}
        onChange={setFilters}
        phaseOptions={phaseOptions}
        modelOptions={modelOptions}
      />

      {isLoading ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          <Skeleton className="h-80" />
          <Skeleton className="h-80" />
          <Skeleton className="h-80" />
          <Skeleton className="h-80" />
        </div>
      ) : isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load tasks</AlertTitle>
          <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : !data || data.tasks.length === 0 ? (
        <div className="rounded-lg border border-dashed bg-white p-10 text-center">
          <h2 className="text-base font-medium">No tasks yet</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Run{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
              orch atomize
            </code>{" "}
            or{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
              orch init
            </code>{" "}
            to seed your project.
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          {COLUMNS.map((col) => (
            <KanbanColumn
              key={col.status}
              title={col.title}
              status={col.status}
              tasks={grouped ? grouped[col.status] : []}
              onCardClick={(id) => setOpenTaskId(id)}
            />
          ))}
        </div>
      )}

      <TaskDetailModal
        taskId={openTaskId}
        onClose={() => setOpenTaskId(null)}
      />
    </div>
  )
}
