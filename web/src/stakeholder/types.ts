// Mirrors G6.1's real schema 1 draft (internal/publish), as shared by
// orch-sonnet — not yet the committed JSON Schema, so field additions are
// still possible, but names/shapes below match their draft exactly. See
// docs/brainstorm/go-migration-notes/sonnet-2.md for the coordination
// note and the one flagged issue (Milestone's duplicate "done" key in
// their example — resolved here by treating `done` as the count only and
// deriving completeness client-side, pending their rename).

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
  spend_usd: number
  spend_by_day: StakeholderBudgetDay[]
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

/** A milestone (phase) is done when every task in it is — never trust a
 * `done` boolean sitting next to a `done` count field in the same object,
 * since a JSON object can't hold two same-named keys without one
 * silently winning during parse. */
export function isMilestoneComplete(m: StakeholderMilestone): boolean {
  return m.total > 0 && m.done >= m.total
}
