// Mirrors the committed schema 1 (docs/SNAPSHOT-SCHEMA.md, PR #177) —
// internal/publish/snapshot.Build's real, shipped shape. Earlier drafts of
// this file worked around a duplicate "done" key (int count + bool flag)
// in orch-sonnet's pre-commit example by deriving completeness
// client-side; the committed schema resolved that itself with a separate
// `complete` boolean field, so this file now just reads it. See
// docs/brainstorm/go-migration-notes/sonnet-2.md for that earlier note.

export interface StakeholderSummary {
  total: number
  done: number
  in_progress: number
  blocked: number
  backlog: number
  percent_done: number
  estimate_hours_total: number
  eta_hours: number | null
}

export interface StakeholderMilestone {
  phase: number
  name: string
  total: number
  done: number
  in_progress: number
  blocked: number
  backlog: number
  percent_done: number
  complete: boolean
}

export interface StakeholderBlocker {
  phase: number
  title: string
  reason: string
}

export interface StakeholderBudgetDay {
  date: string
  cost_usd: number
}

export interface StakeholderBudget {
  enabled: boolean
  // Absent from the document entirely when `enabled` is false — the
  // snapshot builder omits them at the source, not just at render time
  // (docs/SNAPSHOT-SCHEMA.md's `budget` section), so these are optional
  // rather than nullable.
  spend_usd?: number
  spend_by_day?: StakeholderBudgetDay[]
}

export interface StakeholderExecutiveSummary {
  text: string
  language: string
}

export interface StakeholderSnapshot {
  schema: number
  generated_at: string
  project_name: string
  refresh_interval_s: number
  summary: StakeholderSummary
  milestones: StakeholderMilestone[]
  blockers: StakeholderBlocker[]
  budget: StakeholderBudget
  executive_summary: StakeholderExecutiveSummary
}

