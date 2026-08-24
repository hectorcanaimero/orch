import { useEffect, useState } from "react"
import { Search } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import type { TaskFilters } from "@/lib/types"

export interface TaskFiltersBarValue {
  phase: string
  model: string
  search: string
  status: string
}

export interface TaskFiltersBarProps {
  value: TaskFiltersBarValue
  onChange: (next: TaskFiltersBarValue) => void
  /** Options derived from the currently-visible task list. */
  phaseOptions: number[]
  modelOptions: string[]
}

const STATUS_OPTIONS = [
  { value: "backlog", label: "Backlog" },
  { value: "todo", label: "Todo" },
  { value: "in-progress", label: "In progress" },
  { value: "blocked", label: "Blocked" },
  { value: "done", label: "Done" },
]

/**
 * Shared filters bar used by Kanban + Tasks pages. Owns only presentation —
 * the parent owns the `TaskFilters` state and the debounce mapping.
 */
export function TaskFiltersBar({
  value,
  onChange,
  phaseOptions,
  modelOptions,
}: TaskFiltersBarProps) {
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-zinc-50 p-3">
      <div className="relative min-w-[220px] flex-1">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={value.search}
          onChange={(e) => onChange({ ...value, search: e.target.value })}
          placeholder="Search by title, description, ID…"
          className="pl-9"
        />
      </div>
      <div className="w-36">
        <Select
          value={value.status}
          onChange={(e) => onChange({ ...value, status: e.target.value })}
        >
          <option value="">All statuses</option>
          {STATUS_OPTIONS.map((s) => (
            <option key={s.value} value={s.value}>
              {s.label}
            </option>
          ))}
        </Select>
      </div>
      <div className="w-36">
        <Select
          value={value.phase}
          onChange={(e) => onChange({ ...value, phase: e.target.value })}
        >
          <option value="">All phases</option>
          {phaseOptions.map((p) => (
            <option key={p} value={p}>
              Phase {p}
            </option>
          ))}
        </Select>
      </div>
      <div className="w-52">
        <Select
          value={value.model}
          onChange={(e) => onChange({ ...value, model: e.target.value })}
        >
          <option value="">All models</option>
          {modelOptions.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </Select>
      </div>
    </div>
  )
}

/**
 * Debounce a value by `delayMs`.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(t)
  }, [value, delayMs])
  return debounced
}

/**
 * Map `TaskFiltersBarValue` to the backend `TaskFilters` shape.
 */
export function toTaskFilters(v: TaskFiltersBarValue): TaskFilters {
  const f: TaskFilters = {}
  if (v.phase) f.phase = Number(v.phase)
  if (v.model) f.model = v.model
  if (v.search.trim()) f.q = v.search.trim()
  if (v.status) f.status = v.status
  return f
}
