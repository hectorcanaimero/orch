import { useMemo, useState } from "react"
import { AlertTriangle, ChevronDown, ChevronUp } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
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
import type { Task } from "@/lib/types"
import { cn } from "@/lib/utils"

interface Column {
  key: string
  label: string
  /** If set, the column header is clickable and sorts by this key. */
  sortKey?: string
  /** Utility classes controlling width + text alignment on <th> and <td>. */
  headClass?: string
  cellClass?: string
}

const COLUMNS: Column[] = [
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
    key: "last_updated",
    label: "Last updated",
    sortKey: "last_updated",
    headClass: "w-[140px]",
    cellClass: "text-muted-foreground",
  },
]

/** Client-side sort keys → getters. */
const SORT_GETTERS: SortGetters<Task> = {
  phase: (t) => t.phase,
  id: (t) => t.id,
  title: (t) => t.title,
  status: (t) => t.status,
  model: (t) => t.model,
  dep_count: (t) => t.dep_count,
  estimate_hours: (t) => t.estimate_hours,
  on_critical_path: (t) => t.on_critical_path,
  last_updated: (t) => t.last_updated,
}

/** ISO string → `YYYY-MM-DD HH:mm` (local time). `—` if empty/invalid. */
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

export function ListPage() {
  const [filters, setFilters] = useState<TaskFiltersBarValue>({
    phase: "",
    model: "",
    search: "",
  })
  const debouncedSearch = useDebouncedValue(filters.search, 250)

  // Only debounce the `search` field before hitting the backend — the phase
  // + model selects fire immediately.
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
  const { status: streamStatus, lastEventAt } = useEventStream()
  const [openTaskId, setOpenTaskId] = useState<string | null>(null)

  // Default sort: phase asc, secondary tiebreaker id asc. We implement the
  // stable-sort tiebreaker by sorting the tiebreaker key FIRST outside the
  // hook (Array.prototype.sort is stable in ES2019+), then handing the
  // pre-ordered array to the active sort.
  const { sort, toggleSort, sortedRows } = useTableSort<Task>({
    column: "phase",
    direction: "asc",
  })

  const rows = useMemo(() => {
    if (!data) return [] as Task[]
    // Tiebreaker pass: id asc — cheap, keeps table deterministic when the
    // primary sort has ties.
    const byId = [...data.tasks].sort((a, b) =>
      a.id < b.id ? -1 : a.id > b.id ? 1 : 0,
    )
    return sortedRows(byId, SORT_GETTERS)
  }, [data, sortedRows])

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

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">List</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? `${data.count}/${data.total} tasks`
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

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load tasks</AlertTitle>
          <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : !isLoading && (!data || data.tasks.length === 0) ? (
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
        <div className="max-h-[calc(100vh-16rem)] overflow-auto rounded-lg border bg-white">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-background">
              <TableRow>
                {COLUMNS.map((col) => {
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
                      <TableCell colSpan={COLUMNS.length}>
                        <Skeleton className="h-10 w-full" />
                      </TableCell>
                    </TableRow>
                  ))
                : rows.map((task) => (
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
                        {task.dep_count}
                      </TableCell>
                      <TableCell className="text-right">
                        {task.estimate_hours}h
                      </TableCell>
                      <TableCell>
                        {task.on_critical_path ? (
                          <Badge variant="danger">critical</Badge>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatShortDate(task.last_updated)}
                      </TableCell>
                    </TableRow>
                  ))}
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
