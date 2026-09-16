import { useMemo, useState, type ReactNode } from "react"
import { AlertTriangle, ChevronDown, ChevronUp, GitBranch, LayoutGrid, LayoutList, Lock, Route } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { EmptyTasks } from "@/components/EmptyTasks"
import { Explain } from "@/components/Explain"
import { LiveStatusPill } from "@/components/LiveStatusPill"
import { StatusBadge } from "@/components/StatusBadge"
import { TaskCard } from "@/components/TaskCard"
import { TaskDetailModal } from "@/components/TaskDetailModal"
import {
  TaskFiltersBar,
  toTaskFilters,
  useDebouncedValue,
  type TaskFiltersBarValue,
} from "@/components/TaskFiltersBar"
import { useEventStream } from "@/hooks/useEventStream"
import { useTableSort, type SortGetters } from "@/hooks/useTableSort"
import { useTasks } from "@/hooks/useTasks"
import { fmt, t } from "@/i18n"
import { describeLoadError } from "@/lib/errors"
import { criticalPathMatters } from "@/lib/status"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

interface Column {
  key: string
  label: ReactNode
  sortKey?: string
  headClass?: string
}

type ViewMode = "list" | "card"

const SORT_GETTERS: SortGetters<Task> = {
  phase: (t) => t.phase,
  id: (t) => t.id,
  title: (t) => t.title,
  status: (t) => t.status,
  model: (t) => t.model,
  dep_count: (t) => t.dep_count,
  estimate_hours: (t) => t.estimate_hours,
  parallelizable: (t) => (t.parallelizable ? 1 : 0),
  last_updated: (t) => t.last_updated,
}

function shortModel(model: string): string {
  return model.includes("/") ? (model.split("/").pop() ?? model) : model
}

export function ListPage() {
  const [filters, setFilters] = useState<TaskFiltersBarValue>({
    phase: "",
    model: "",
    search: "",
    status: "",
  })
  // A phone gets cards: nine table columns only fit by scrolling sideways.
  const [viewMode, setViewMode] = useState<ViewMode>(() =>
    typeof window !== "undefined" && window.matchMedia?.("(max-width: 767px)").matches ? "card" : "list",
  )
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
    const byId = [...data.tasks].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
    return sortedRows(byId, SORT_GETTERS)
  }, [data, sortedRows])

  // Build a status map from the current result set — used to detect blocking deps
  const taskStatusMap = useMemo(() => {
    if (!data) return {} as Record<string, string>
    return Object.fromEntries(data.tasks.map((task) => [task.id, task.status]))
  }, [data])

  const modelOptions = useMemo(() => {
    if (!data) return [] as string[]
    const s = new Set<string>()
    for (const task of data.tasks) if (task.model) s.add(task.model)
    return Array.from(s).sort()
  }, [data])

  const phaseOptions = useMemo(() => {
    if (!data) return [] as number[]
    const s = new Set<number>()
    for (const task of data.tasks) s.add(task.phase)
    return Array.from(s).sort((a, b) => a - b)
  }, [data])

  const showCriticalPath = data ? criticalPathMatters(data.tasks) : false

  const columns: Column[] = [
    { key: "phase", label: t("tasks.col.phase"), sortKey: "phase", headClass: "w-[64px]" },
    { key: "id", label: t("tasks.col.id"), sortKey: "id", headClass: "w-[120px]" },
    { key: "title", label: t("tasks.col.title"), sortKey: "title", headClass: "min-w-[240px]" },
    { key: "status", label: t("tasks.col.status"), sortKey: "status", headClass: "w-[130px]" },
    { key: "model", label: t("tasks.col.model"), sortKey: "model", headClass: "w-[150px]" },
    {
      key: "dep_count",
      label: <Explain term="deps">{t("tasks.col.deps")}</Explain>,
      sortKey: "dep_count",
      headClass: "w-[80px]",
    },
    {
      key: "estimate_hours",
      label: <Explain term="estimate">{t("tasks.col.estimate")}</Explain>,
      sortKey: "estimate_hours",
      headClass: "w-[80px]",
    },
    {
      key: "parallelizable",
      label: <Explain term="ready">{t("tasks.col.ready")}</Explain>,
      sortKey: "parallelizable",
      headClass: "w-[80px]",
    },
    { key: "last_updated", label: t("tasks.col.updated"), sortKey: "last_updated", headClass: "w-[130px]" },
  ]

  const renderSortIcon = (columnSortKey: string | undefined) => {
    if (!columnSortKey || sort.column !== columnSortKey) return null
    return sort.direction === "asc" ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />
  }

  const isEmpty = !isLoading && (!data || data.tasks.length === 0)

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("tasks.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground tabular-nums">
            {data ? t("tasks.shown", { count: data.count, total: data.total }) : t("tasks.loading")}
            {showCriticalPath ? (
              <span className="ml-3 inline-flex items-center gap-1 align-middle">
                <Route className="h-3.5 w-3.5 text-foreground" aria-hidden />
                <Explain term="critical_path" />
              </span>
            ) : null}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <LiveStatusPill status={streamStatus} lastEventAt={lastEventAt} />
          <div className="flex items-center rounded-lg border bg-card p-0.5" role="group" aria-label={t("tasks.view")}>
            {(
              [
                ["list", LayoutList, t("tasks.view_list")],
                ["card", LayoutGrid, t("tasks.view_cards")],
              ] as const
            ).map(([mode, Icon, label]) => (
              <button
                key={mode}
                type="button"
                onClick={() => setViewMode(mode)}
                aria-label={label}
                aria-pressed={viewMode === mode}
                className={cn(
                  "flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors",
                  "hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  viewMode === mode && "bg-accent text-foreground",
                )}
              >
                <Icon className="h-4 w-4" aria-hidden />
              </button>
            ))}
          </div>
        </div>
      </header>

      <TaskFiltersBar value={filters} onChange={setFilters} phaseOptions={phaseOptions} modelOptions={modelOptions} />

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("tasks.load_failed")}</AlertTitle>
          <AlertDescription>{describeLoadError(error)}</AlertDescription>
        </Alert>
      ) : isEmpty ? (
        <EmptyTasks filtered={Object.values(backendFilters).some(Boolean)} />
      ) : viewMode === "card" ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
          {isLoading
            ? Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-32 w-full rounded-lg" />)
            : rows.map((task) => (
                <TaskCard
                  key={task.id}
                  task={task}
                  taskStatusMap={taskStatusMap}
                  onClick={setOpenTaskId}
                  showStatus
                  showCriticalPath={showCriticalPath}
                />
              ))}
        </div>
      ) : (
        <div className="max-h-[calc(100vh-16rem)] overflow-auto rounded-xl border bg-card">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow className="hover:bg-transparent">
                {columns.map((col) => {
                  const sortable = Boolean(col.sortKey)
                  return (
                    <TableHead
                      key={col.key}
                      className={cn(col.headClass, sortable && "cursor-pointer select-none hover:text-foreground")}
                      onClick={sortable ? () => toggleSort(col.sortKey!) : undefined}
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
                      <TableCell colSpan={columns.length}>
                        <Skeleton className="h-6 w-full" />
                      </TableCell>
                    </TableRow>
                  ))
                : rows.map((task) => {
                    const blocking = task.dependencies.some(
                      (id) => taskStatusMap[id] !== undefined && taskStatusMap[id] !== "done",
                    )
                    return (
                      <TableRow key={task.id} className="h-11 cursor-pointer" onClick={() => setOpenTaskId(task.id)}>
                        <TableCell className="text-muted-foreground">{task.phase}</TableCell>
                        <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">{task.id}</TableCell>
                        <TableCell className="max-w-0" title={task.title}>
                          <span className="flex items-center gap-1.5">
                            {showCriticalPath && task.on_critical_path ? (
                              <Route className="h-3.5 w-3.5 shrink-0" aria-label={t("task.on_critical_path")} />
                            ) : null}
                            <span className="truncate font-medium">{task.title}</span>
                          </span>
                        </TableCell>
                        <TableCell>
                          <StatusBadge status={task.status} />
                        </TableCell>
                        <TableCell className="max-w-0 truncate font-mono text-xs text-muted-foreground" title={task.model || undefined}>
                          {task.model ? shortModel(task.model) : "—"}
                        </TableCell>
                        <TableCell className="text-muted-foreground">
                          {task.dep_count > 0 ? (
                            <span
                              className={cn("inline-flex items-center gap-1", blocking && "text-status-blocked")}
                              title={blocking ? t("task.deps_pending") : undefined}
                            >
                              {blocking ? <Lock className="h-3 w-3" aria-hidden /> : null}
                              {task.dep_count}
                            </span>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        <TableCell>{t("task.hours", { hours: task.estimate_hours })}</TableCell>
                        <TableCell>
                          {task.parallelizable ? (
                            <GitBranch className="h-4 w-4 text-status-done" aria-label={t("task.ready")} />
                          ) : null}
                        </TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {task.last_updated ? fmt.dateTime(task.last_updated) : "—"}
                        </TableCell>
                      </TableRow>
                    )
                  })}
            </TableBody>
          </Table>
        </div>
      )}

      <TaskDetailModal taskId={openTaskId} onClose={() => setOpenTaskId(null)} />
    </div>
  )
}
