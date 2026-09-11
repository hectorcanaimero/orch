import { useMemo, useRef, useState } from "react"
import { AlertTriangle, Maximize2, Minimize2 } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
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
import { useFullscreen } from "@/hooks/useFullscreen"
import { useTasks } from "@/hooks/useTasks"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

const STATUS_TO_COLUMN: Record<string, KanbanStatus> = {
  backlog: "backlog",
  todo: "todo",
  "in-progress": "in_progress",
  blocked: "blocked",
  done: "done",
}

const COLUMNS: { title: string; status: KanbanStatus }[] = [
  { title: "Backlog", status: "backlog" },
  { title: "Todo", status: "todo" },
  { title: "In Progress", status: "in_progress" },
  { title: "Blocked", status: "blocked" },
  { title: "Done", status: "done" },
]

function groupTasks(tasks: Task[]): Record<KanbanStatus, Task[]> {
  const buckets: Record<KanbanStatus, Task[]> = {
    backlog: [],
    todo: [],
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
    status: "",
  })
  const debouncedSearch = useDebouncedValue(filters.search, 250)

  const [openTaskId, setOpenTaskId] = useState<string | null>(null)

  // Sprint E-6: fullscreen the board region (not the page shell) so operators
  // can zoom the columns without losing the sidebar/nav on exit.
  const boardRef = useRef<HTMLDivElement>(null)
  const [isFullscreen, toggleFullscreen] = useFullscreen(boardRef)

  const backendFilters = useMemo(
    () =>
      toTaskFilters({
        phase: filters.phase,
        model: filters.model,
        search: debouncedSearch,
        status: filters.status,
      }),
    [filters.phase, filters.model, debouncedSearch, filters.status],
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

  const taskStatusMap = useMemo(() => {
    if (!data) return {} as Record<string, string>
    return Object.fromEntries(data.tasks.map((t) => [t.id, t.status]))
  }, [data])

  return (
    // Fixed viewport column ONLY on xl (where all 4 columns fit in one row).
    // On smaller breakpoints the columns stack and the page scrolls naturally
    // — locking height there would push filters/header off-screen. `h-full`
    // instead of `h-[calc(100vh-4rem)]` at xl doesn't work because AppLayout
    // doesn't stretch this child; we need the explicit viewport calc.
    // 4rem accounts for AppLayout's py-8 (2rem top + 2rem bottom).
    <div className="flex flex-col gap-4 xl:h-[calc(100vh-4rem)]">
      <header className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Kanban</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? `${data.summary.done}/${data.summary.total} tasks done`
              : "Loading tasks…"}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
          <Button
            type="button"
            variant="outline"
            size="icon"
            onClick={toggleFullscreen}
            title={isFullscreen ? "Exit fullscreen" : "Enter fullscreen"}
            aria-label={isFullscreen ? "Exit fullscreen" : "Enter fullscreen"}
          >
            {isFullscreen ? (
              <Minimize2 className="h-4 w-4" />
            ) : (
              <Maximize2 className="h-4 w-4" />
            )}
          </Button>
        </div>
      </header>

      <div className="flex-shrink-0">
        <TaskFiltersBar
          value={filters}
          onChange={setFilters}
          phaseOptions={phaseOptions}
          modelOptions={modelOptions}
        />
      </div>

      {isLoading ? (
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-5">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-full" />
          ))}
        </div>
      ) : isError ? (
        <Alert variant="destructive" className="flex-shrink-0">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load tasks</AlertTitle>
          <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : !data || data.tasks.length === 0 ? (
        <div className="flex-shrink-0 rounded-lg border border-dashed bg-white p-10 text-center">
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
        // `grid-rows-1` forces the single row to `1fr`, so columns get a
        // bounded height and their inner `overflow-y-auto` finally has
        // something to scroll against. `bg-background` + inner padding keep
        // the fullscreen surface presentable when the browser hands us the
        // whole viewport with no chrome.
        <div
          ref={boardRef}
          className={cn(
            "grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-5",
            // xl-only: this row fills the parent's remaining height (1fr) so
            // each KanbanColumn is bounded and its inner overflow-y-auto has
            // something to scroll against. Below xl the row is `auto` and
            // the page scrolls, matching the natural stacked layout.
            "xl:min-h-0 xl:flex-1 xl:grid-rows-1",
            "bg-background",
            isFullscreen && "p-4",
          )}
        >
          {COLUMNS.map((col) => (
            <KanbanColumn
              key={col.status}
              title={col.title}
              status={col.status}
              tasks={grouped ? grouped[col.status] : []}
              taskStatusMap={taskStatusMap}
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
