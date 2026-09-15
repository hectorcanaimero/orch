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
  // Absent when no pace is known (docs/SNAPSHOT-SCHEMA.md).
  eta_date?: string
  eta_confidence?: string
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
  // The phase's deliverables grouped by package (docs/SNAPSHOT-SCHEMA.md).
  packages?: StakeholderPackage[]
}

export interface StakeholderPackage {
  // "" for the group of tasks outside the atomizer's packages.
  name: string
  total: number
  done: number
  deliverables: StakeholderDeliverable[]
}

export type DeliverableStatus = "done" | "in_progress" | "blocked" | "pending"

export interface StakeholderDeliverable {
  title: string
  status: DeliverableStatus
  finished_at?: string
}

export interface StakeholderDelivery {
  title: string
  phase: string
  finished_at: string
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

/**
 * White-label block (G8.4). Absent from the document entirely unless the
 * operator configured `presentation.branding` — so every field is optional,
 * and so is the block itself. A snapshot without it must render exactly as it
 * rendered before this existed.
 */
export interface StakeholderBranding {
  /** What the client reads. Replaces `project_name` in the header. */
  name?: string
  /**
   * Always a `data:` URI, never a path: the document has to stand alone, and
   * a viewer holding this JSON cannot reach the operator's filesystem. PNG or
   * JPEG only — see docs/SNAPSHOT-SCHEMA.md for why the PDF decides that.
   */
  logo?: string
  /** `#rgb` or `#rrggbb`, already validated by `config.Load`. */
  accent_color?: string
  footer?: string
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
  branding?: StakeholderBranding
  // What finished in the last 30 days, newest first; absent when nothing did.
  deliveries?: StakeholderDelivery[]
  // Files the operator shares (portal.documents); absent when none.
  documents?: StakeholderDocument[]
  // How every delivery is checked; absent unless work goes through PRs with CI.
  quality?: StakeholderQuality
}

export type QualityGate = "tests" | "typecheck" | "lint" | "build" | "review"

export interface StakeholderQuality {
  gates: QualityGate[]
  delivered: number
  verified: number
  first_pass: number
}

export interface StakeholderDocument {
  id: string
  title: string
  updated_at: string
  markdown: string
}

