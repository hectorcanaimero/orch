import { useCallback, useEffect, useState } from "react"
import { useSearchParams } from "react-router-dom"
import { Search } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { t } from "@/i18n"
import { STATUS } from "@/lib/status"
import { cn } from "@/lib/utils"
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

// Values are the API's spellings; labels and shapes come from the shared scale.
const STATUS_OPTIONS = [
  { value: "backlog", meta: STATUS.backlog },
  { value: "todo", meta: STATUS.todo },
  { value: "in-progress", meta: STATUS.in_progress },
  { value: "blocked", meta: STATUS.blocked },
  { value: "done", meta: STATUS.done },
]

/**
 * Shared filters bar used by Kanban + Tasks pages. Owns only presentation —
 * the parent owns the `TaskFilters` state and the debounce mapping.
 */
export function TaskFiltersBar({ value, onChange, phaseOptions, modelOptions }: TaskFiltersBarProps) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="relative min-w-[200px] flex-1">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={value.search}
          onChange={(e) => onChange({ ...value, search: e.target.value })}
          placeholder={t("filters.search")}
          aria-label={t("filters.search")}
          className="pl-9"
        />
      </div>
      <div className="w-40">
        <Select value={value.status} onValueChange={(v) => onChange({ ...value, status: v })}>
          <SelectTrigger aria-label={t("filters.status")}>
            <SelectValue placeholder={t("filters.all_statuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">{t("filters.all_statuses")}</SelectItem>
            {STATUS_OPTIONS.map((s) => (
              <SelectItem key={s.value} value={s.value}>
                <span className="inline-flex items-center gap-2">
                  <s.meta.icon className={cn("h-3.5 w-3.5", s.meta.text)} aria-hidden />
                  {t(s.meta.label)}
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="w-36">
        <Select value={value.phase} onValueChange={(v) => onChange({ ...value, phase: v })}>
          <SelectTrigger aria-label={t("filters.phase_label")}>
            <SelectValue placeholder={t("filters.all_phases")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">{t("filters.all_phases")}</SelectItem>
            {phaseOptions.map((p) => (
              <SelectItem key={p} value={String(p)}>
                {t("filters.phase", { phase: p })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="w-52">
        <Select value={value.model} onValueChange={(v) => onChange({ ...value, model: v })}>
          <SelectTrigger aria-label={t("filters.model")}>
            <SelectValue placeholder={t("filters.all_models")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">{t("filters.all_models")}</SelectItem>
            {modelOptions.map((m) => (
              <SelectItem key={m} value={m}>
                {m}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )
}

const FILTER_PARAMS = { search: "q", status: "status", phase: "phase", model: "model" } as const

/**
 * The filters as query parameters, so a filtered view is a link and List,
 * Kanban and Graph share one set: switching tabs carries the query string.
 */
export function useTaskFilterParams(): [TaskFiltersBarValue, (next: TaskFiltersBarValue) => void] {
  const [params, setParams] = useSearchParams()
  const value: TaskFiltersBarValue = {
    search: params.get(FILTER_PARAMS.search) ?? "",
    status: params.get(FILTER_PARAMS.status) ?? "",
    phase: params.get(FILTER_PARAMS.phase) ?? "",
    model: params.get(FILTER_PARAMS.model) ?? "",
  }
  const setValue = useCallback(
    (next: TaskFiltersBarValue) => {
      setParams(
        (prev) => {
          const out = new URLSearchParams(prev)
          for (const [field, key] of Object.entries(FILTER_PARAMS)) {
            const v = next[field as keyof TaskFiltersBarValue]
            if (v) out.set(key, v)
            else out.delete(key)
          }
          return out
        },
        { replace: true },
      )
    },
    [setParams],
  )
  return [value, setValue]
}

/**
 * Debounce a value by `delayMs`.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
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
