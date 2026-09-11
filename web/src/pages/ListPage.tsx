import { useMemo, useState } from "react"
import {
  AlertTriangle,
  ChevronDown,
  ChevronUp,
  GitBranch,
  LayoutGrid,
  LayoutList,
  Lock,
  Timer,
  Zap,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
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
import { useTableSort, type SortGetters } from "@/hooks/useTableSort"
import { statusBadgeVariant } from "@/lib/status"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

// ---- Types ------------------------------------------------------------------

interface Column {
  key: string
  label: string
  sortKey?: string
  headClass?: string
  cellClass?: string
}

type ViewMode = "list" | "card"

// ---- Constants --------------------------------------------------------------

const TABLE_COLUMNS: Column[] = [
  { key: "phase", label: "Phase", sortKey: "phase", headClass: "w-[60px]" },
  { key: "id", label: "ID", sortKey: "id", headClass: "w-[180px]" },
  { key: "title", label: "Title", sortKey: "title", headClass: "min-w-[240px]" },
  { key: "status", label: "Status", sortKey: "status", headClass: "w-[120px]" },
  { key: "model", label: "Model", sortKey: "model", headClass: "w-[180px]" },
  {
    key: "dep_count",
    label: "Deps",
    sortKey: "dep_count",
    headClass: "w-[60px] text-center",
    cellClass: "text-center text-muted-foreground",
  },
  {
    key: "estimate_hours",
    label: "Est",
    sortKey: "estimate_hours",
    headClass: "w-[80px] text-right",
    cellClass: "text-right",
  },
  {
    key: "on_critical_path",
    label: "Critical",
    sortKey: "on_critical_path",
    headClass: "w-[80px]",
  },
  {
    key: "parallelizable",
    label: "Parallel",
    sortKey: "parallelizable",
    headClass: "w-[80px]",
  },
  {
    key: "last_updated",
    label: "Last updated",
    sortKey: "last_updated",
    headClass: "w-[140px]",
    cellClass: "text-muted-foreground",
  },
]

const SORT_GETTERS: SortGetters<Task> = {
  phase: (t) => t.phase,
  id: (t) => t.id,
  title: (t) => t.title,
  status: (t) => t.status,
  model: (t) => t.model,
  dep_count: (t) => t.dep_count,
  estimate_hours: (t) => t.estimate_hours,
  on_critical_path: (t) => (t.on_critical_path ? 1 : 0),
  parallelizable: (t) => (t.parallelizable ? 1 : 0),
  last_updated: (t) => t.last_updated,
}

// ---- Helpers ----------------------------------------------------------------

function formatShortDate(iso: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const pad = (n: number) => n.toString().padStart(2, "0")
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}`
  )
}

// ---- Constants for card-view -------------------------------------------------

const CARD_STATUS_ACCENT: Record<string, string> = {
  backlog:       "border-l-zinc-400",
  todo:          "border-l-sky-400",
  "in-progress": "border-l-violet-500",
  blocked:       "border-l-rose-500",
  done:          "border-l-emerald-500",
}

// ---- TaskCardItem (card-view only) -------------------------------------------

function TaskCardItem({
  task,
  taskStatusMap,
  onClick,
}: {
  task: Task
  taskStatusMap: Record<string, string>
  onClick: () => void
}) {
  const blockingDeps = task.dependencies.filter((id) => {
    const s = taskStatusMap[id]
    return s !== undefined && s !== "done"
  })

  const accentClass = CARD_STATUS_ACCENT[task.status] ?? "border-l-zinc-400"

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onClick()}
      className={cn(
        "group flex flex-col gap-2.5 rounded-md border border-l-4 border-zinc-200 bg-white p-3",
        "cursor-pointer transition-all hover:border-zinc-300 hover:shadow-md",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        accentClass,
      )}
    >
      {/* ID + icons */}
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-[10px] leading-none text-zinc-400">
          Ph{task.phase} · {task.id}
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
      <h3 className="line-clamp-3 text-sm font-medium leading-snug text-zinc-800">
        {task.title}
      </h3>

      {/* Status + meta footer */}
      <div className="flex items-center justify-between gap-2">
        <Badge variant={statusBadgeVariant(task.status)} className="text-[10px]">
          {task.status}
        </Badge>
        <div className="flex items-center gap-2 text-xs text-zinc-400">
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
      </div>
    </div>
  )
}

// ---- Main page --------------------------------------------------------------

export function ListPage() {
  const [filters, setFilters] = useState<TaskFiltersBarValue>({
    phase: "",
    model: "",
    search: "",
    status: "",
  })
  const [viewMode, setViewMode] = useState<ViewMode>("list")
  const debouncedSearch = useDebouncedValue(filters.search, 250)

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
  const { status: streamStatus, lastEventAt } = useEventStream()
  const [openTaskId, setOpenTaskId] = useState<string | null>(null)

  const { sort, toggleSort, sortedRows } = useTableSort<Task>({
    column: "phase",
    direction: "asc",
  })

  const rows = useMemo(() => {
    if (!data) return [] as Task[]
    const byId = [...data.tasks].sort((a, b) =>
      a.id < b.id ? -1 : a.id > b.id ? 1 : 0,
    )
    return sortedRows(byId, SORT_GETTERS)
  }, [data, sortedRows])

  // Build a status map from the current result set — used to detect blocking deps
  const taskStatusMap = useMemo(() => {
    if (!data) return {} as Record<string, string>
    return Object.fromEntries(data.tasks.map((t) => [t.id, t.status]))
  }, [data])

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

  const renderSortIcon = (columnSortKey: string | undefined) => {
    if (!columnSortKey || sort.column !== columnSortKey) return null
    return sort.direction === "asc" ? (
      <ChevronUp className="h-3 w-3" />
    ) : (
      <ChevronDown className="h-3 w-3" />
    )
  }

  const isEmpty = !isLoading && (!data || data.tasks.length === 0)

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Tasks</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? `${data.count}/${data.total} tasks`
              : "Loading tasks…"}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
          <div className="flex items-center rounded-md border border-zinc-200 bg-zinc-50 p-0.5">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setViewMode("list")}
              aria-label="List view"
              className={cn(
                "h-8 w-8 p-0",
                viewMode === "list" && "bg-white shadow-sm",
              )}
            >
              <LayoutList className="h-4 w-4" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setViewMode("card")}
              aria-label="Card view"
              className={cn(
                "h-8 w-8 p-0",
                viewMode === "card" && "bg-white shadow-sm",
              )}
            >
              <LayoutGrid className="h-4 w-4" />
            </Button>
          </div>
        </div>
      </header>

      <TaskFiltersBar
        value={filters}
        onChange={setFilters}
        phaseOptions={phaseOptions}
        modelOptions={modelOptions}
      />

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load tasks</AlertTitle>
          <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : isEmpty ? (
        <div className="rounded-lg border border-dashed bg-zinc-50 p-10 text-center">
          <h2 className="text-base font-medium">No tasks found</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {Object.values(backendFilters).some(Boolean)
              ? "Try clearing some filters."
              : "Run "}
            {!Object.values(backendFilters).some(Boolean) && (
              <>
                <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
                  orch atomize
                </code>{" "}
                or{" "}
                <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
                  orch init
                </code>{" "}
                to seed your project.
              </>
            )}
          </p>
        </div>
      ) : viewMode === "card" ? (
        // ---- Card view -------------------------------------------------------
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {isLoading
            ? Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-36 w-full rounded-lg" />
              ))
            : rows.map((task) => (
                <TaskCardItem
                  key={task.id}
                  task={task}
                  taskStatusMap={taskStatusMap}
                  onClick={() => setOpenTaskId(task.id)}
                />
              ))}
        </div>
      ) : (
        // ---- List view -------------------------------------------------------
        <div className="max-h-[calc(100vh-16rem)] overflow-auto rounded-lg border bg-white">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-background">
              <TableRow>
                {TABLE_COLUMNS.map((col) => {
                  const sortable = Boolean(col.sortKey)
                  return (
                    <TableHead
                      key={col.key}
                      className={cn(col.headClass, sortable && "cursor-pointer select-none")}
                      onClick={
                        sortable ? () => toggleSort(col.sortKey!) : undefined
                      }
                      aria-sort={
                        sortable && sort.column === col.sortKey
                          ? sort.direction === "asc"
                            ? "ascending"
                            : "descending"
                          : undefined
                      }
                    >
                      <span className="inline-flex items-center gap-1">
                        {col.label}
                        {renderSortIcon(col.sortKey)}
                      </span>
                    </TableHead>
                  )
                })}
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading
                ? Array.from({ length: 8 }).map((_, i) => (
                    <TableRow key={`skeleton-${i}`}>
                      <TableCell colSpan={TABLE_COLUMNS.length}>
                        <Skeleton className="h-10 w-full" />
                      </TableCell>
                    </TableRow>
                  ))
                : rows.map((task) => {
                    const blockingDeps = task.dependencies.filter(
                      (id) => taskStatusMap[id] !== undefined && taskStatusMap[id] !== "done",
                    )
                    return (
                      <TableRow
                        key={task.id}
                        className="h-10 cursor-pointer"
                        onClick={() => setOpenTaskId(task.id)}
                      >
                        <TableCell className="font-mono text-xs">
                          {task.phase}
                        </TableCell>
                        <TableCell className="font-mono text-xs">
                          {task.id}
                        </TableCell>
                        <TableCell
                          className="max-w-0 truncate"
                          title={task.title}
                        >
                          {task.title}
                        </TableCell>
                        <TableCell>
                          <Badge variant={statusBadgeVariant(task.status)}>
                            {task.status}
                          </Badge>
                        </TableCell>
                        <TableCell
                          className="max-w-0 truncate font-mono text-xs"
                          title={task.model || undefined}
                        >
                          {task.model || "—"}
                        </TableCell>
                        <TableCell className="text-center text-muted-foreground">
                          {task.dep_count > 0 ? (
                            blockingDeps.length > 0 ? (
                              <span className="inline-flex items-center gap-1 text-amber-600">
                                <Lock className="h-3 w-3" />
                                {task.dep_count}
                              </span>
                            ) : (
                              task.dep_count
                            )
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        <TableCell className="text-right">
                          {task.estimate_hours}h
                        </TableCell>
                        <TableCell>
                          {task.on_critical_path ? (
                            <Badge variant="danger">critical</Badge>
                          ) : null}
                        </TableCell>
                        <TableCell>
                          {task.parallelizable ? (
                            <Badge variant="info" className="gap-1">
                              <GitBranch className="h-3 w-3" />
                            </Badge>
                          ) : null}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatShortDate(task.last_updated)}
                        </TableCell>
                      </TableRow>
                    )
                  })}
            </TableBody>
          </Table>
        </div>
      )}

      <TaskDetailModal
        taskId={openTaskId}
        onClose={() => setOpenTaskId(null)}
      />
    </div>
  )
}
