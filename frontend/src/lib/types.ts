export interface StakeholderSummaryStats {
  total: number
  done: number
  in_progress: number
  blocked: number
  backlog: number
  percent_done: number
  estimate_hours_total: number
}

export interface StakeholderMilestone {
  id?: string
  name: string
  status?: string
  eta_hours?: number | null
  percent_done?: number
  [key: string]: unknown
}

export interface StakeholderSummary {
  project_id: string
  summary: StakeholderSummaryStats
  milestones: StakeholderMilestone[]
  spend_rounded_usd: number
  eta_hours: number | null
  refresh_interval_s: number
}

/**
 * Task status values as emitted by the backend. Kept as a string union for
 * type safety in Kanban grouping, but any `Task.status` is still typed as
 * `string` in case the backend adds a status we don't know about — treat
 * unknowns as "backlog" in UI code.
 */
export type TaskStatus =
  | "backlog"
  | "todo"
  | "in_progress"
  | "blocked"
  | "done"

export interface Task {
  id: string
  phase: number
  title: string
  description: string
  model: string
  reason: string
  status: string
  dependencies: string[]
  dep_count: number
  estimate_hours: number
  files: string[]
  spec_ref: string
  comments: unknown[]
  human_hours: number
  last_updated: string
  downstream_impact: number
  on_critical_path: boolean
  parallelizable?: boolean
}

export interface TasksSummary {
  total: number
  done: number
  in_progress: number
  blocked: number
  backlog: number
  percent_done: number
  estimate_hours_total: number
}

export interface TasksResponse {
  project_id: string
  project_root: string
  summary: TasksSummary
  tasks: Task[]
  count: number
  total: number
}

export interface TaskFilters {
  phase?: number
  status?: string
  model?: string
  q?: string
  parallelizable?: 0 | 1
}

/**
 * Loose shape for an SSE event payload. Backend emits `format_event()` output,
 * which always has `ts` and `kind` and usually a `task_id`, but extra keys
 * vary by event kind — keep this open-ended.
 */
export interface EventPayload {
  ts?: string
  task_id?: string
  kind?: string
  [key: string]: unknown
}

/**
 * Formatted event as returned by `GET /api/events` and streamed on
 * `GET /api/events/stream` (both are backed by `format_event()` on the server).
 *
 * `severity` values follow the backend's own bucket names, NOT the shadcn
 * variant vocabulary. Real world values (see `orchestrator/dashboard/log_stream.py`):
 *
 *   success        → "ok"
 *   fail | timeout → "err"
 *   retry | escalate | resume_revert → "warn"
 *   block          → "block"
 *   dispatch | resume_adopt | reconciled → "info"
 *   unknown        → "muted"
 *
 * The UI maps these to concrete color classes — treat any unknown severity
 * as "muted" so a future backend addition renders without exploding.
 */
export interface FormattedEvent {
  ts: string
  task_id: string
  event_type: string
  backend: string
  human: string
  severity: string
  raw: Record<string, unknown>
}

export interface EventsHistoryResponse {
  events: FormattedEvent[]
  count: number
}

export interface ProjectConfigConcurrency {
  global_max?: number
  per_provider?: Record<string, number>
  per_file?: number
}

export interface ProjectConfigBudget {
  per_dispatch_usd?: number
}

export interface ProjectConfigState {
  backend?: string
  sqlite_path?: string | null
  tasks_json_precedence?: string
}

export interface ProjectConfigRetry {
  backoff_seconds?: number
  rate_limit_backoff_seconds?: number
}

export interface ProjectConfigBudgets {
  config_path?: string
  preset?: string
  typical_dispatch_tokens?: number
}

export interface ProjectConfigFindings {
  publish_repo?: string
  publish_rate_limit_per_hour?: number
  label?: string
  min_publish_confidence?: string
}

/**
 * Sprint E-3 — public, non-secret dashboard client knobs sourced from
 * `config.yaml -> dashboard:`. The backend whitelist deliberately excludes
 * `token` / `profile` (see `_load_project_config` in
 * `orchestrator/dashboard/server.py`) so this type MUST stay narrow —
 * anything wider would silently mask a leak the next time the whitelist
 * grows.
 */
export interface ProjectConfigDashboard {
  board_url?: string | null
}

export interface ProjectConfig {
  concurrency?: ProjectConfigConcurrency
  budget?: ProjectConfigBudget
  state?: ProjectConfigState
  retry?: ProjectConfigRetry
  spec_root?: string
  budgets?: ProjectConfigBudgets
  findings?: ProjectConfigFindings
  dashboard?: ProjectConfigDashboard
  strict_files_phases?: number[]
  default_timeout_multiplier?: number
}

export interface ArchitectureStatus {
  exists: boolean
  generated_at: string | null
  source_hash: string | null
  count: number
  last_cost_usd: number | null
  regenerate_in_progress: boolean
}

export interface ArchitectureSnapshot {
  timestamp: string
  source_hash: string
  cost_usd: number
  model: string
  source_artifacts: {
    prd_count: number
    spec_count: number
    task_count: number
  }
}

export interface ArchitectureHistory {
  snapshots: ArchitectureSnapshot[]
}

export interface ArchitectureRegenerateResponse {
  started_at: string
  run_id: string
}
