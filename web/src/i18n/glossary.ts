// What the dashboard's terms mean, how each figure is computed, and where its
// data comes from — one place, so the popover, the manuals and a future
// translation all say the same thing. Each entry is checked against the Go
// code it describes (internal/graph/analytics.go, graph/eta.go,
// project/events.go).
export type GlossaryTerm =
  | "critical_path"
  | "estimate"
  | "ready"
  | "deps"
  | "downstream_impact"
  | "agent_time"
  | "eta"
  | "spend"
  | "velocity"
  | "budget_window"
  | "weighted_tokens"
  | "cost_source"
  | "quick_tunnel"

export interface GlossaryEntry {
  title: string
  definition: string
  computed: string
  source: string
}

export const glossary: Record<GlossaryTerm, GlossaryEntry> = {
  critical_path: {
    title: "Critical path",
    definition: "The longest chain of dependent tasks. A delay on any of them delays the whole project.",
    computed: "Longest path through the dependency graph, weighting each task by its estimate (1h when it has none).",
    source: "dependencies and estimateHours in tasks.json",
  },
  estimate: {
    title: "Estimate",
    definition: "Planned effort for a task, in hours.",
    computed: "Taken as written in the plan; never measured.",
    source: "estimateHours in tasks.json",
  },
  ready: {
    title: "Ready",
    definition: "Waiting in backlog or todo with every dependency done, so it can start now.",
    computed:
      "Backlog and todo tasks whose dependencies are all done. A dependency that names no task counts as not done.",
    source: "dependencies in tasks.json, task status in orch.db",
  },
  deps: {
    title: "Dependencies",
    definition: "Tasks that must be done before this one can start.",
    computed: "How many dependencies the task lists. A lock means at least one is not done yet.",
    source: "dependencies in tasks.json, task status in orch.db",
  },
  downstream_impact: {
    title: "Downstream impact",
    definition: "How many unfinished tasks wait on this one, directly or through others.",
    computed: "Follows every task that depends on it, passing through finished ones, and counts those not done.",
    source: "dependencies in tasks.json, task status in orch.db",
  },
  agent_time: {
    title: "Agent time",
    definition: "Wall-clock time agents spent working on the task.",
    computed: "Sum of dispatch-to-finish intervals across attempts, using the run's own duration when it reports one.",
    source: "run events in orch.db",
  },
  eta: {
    title: "Estimated finish",
    definition: "When the remaining tasks finish at the recent pace.",
    computed:
      "Remaining tasks ÷ tasks finished per day over the last 7 days. High confidence up to 30 days out, low beyond. With no measured pace yet, the hours of work left are shown instead.",
    source: "task status history in orch.db",
  },
  spend: {
    title: "AI spend",
    definition: "What the providers reported costing for the runs.",
    computed: "Sum of the cost recorded per day. A provider that reports no cost adds $0.",
    source: "spend table in orch.db",
  },
  velocity: {
    title: "Pace",
    definition: "How many tasks the project finishes per day, measured over the whole project.",
    computed: "Tasks that reached done in the last 7 days ÷ 7. orch has no sprints; the window rolls forward every day.",
    source: "task status history in orch.db (internal/dashboard/sprint.go)",
  },
  budget_window: {
    title: "Budget window",
    definition:
      "The rolling hours over which a provider's token use is added up. The preset in budgets.yaml sets its length, the token budget and the threshold.",
    computed:
      "Tokens recorded for the provider within the last window_hours. At threshold_pct of token_budget orch stops sending it new tasks; it resumes when older usage leaves the window, which is the reset time shown.",
    source: "budgets.yaml preset, spend table in orch.db",
  },
  weighted_tokens: {
    title: "Weighted tokens",
    definition: "Tokens counted the way the provider bills them, which is what the guardrail compares with the cap.",
    computed:
      "Input and output at full weight, prompt-cache reads at 10%, cache writes at 125%. The raw figure is the unweighted sum the CLI reported.",
    source: "spend table in orch.db (token and cache columns)",
  },
  cost_source: {
    title: "Where a cost comes from",
    definition: "Whether a spend figure was reported by the provider's CLI or worked out by orch.",
    computed:
      "Reported: the CLI gave a price. Estimated: the CLI gave tokens only, priced from pricing.yaml. No data: it gave neither, so each dispatch counts as typical_dispatch_tokens in the window.",
    source: "spend table in orch.db, pricing.yaml",
  },
  quick_tunnel: {
    title: "Quick tunnel",
    definition:
      "A temporary public https address from Cloudflare that forwards to this dashboard. No Cloudflare account, login or domain.",
    computed:
      "orch runs cloudflared tunnel --url against the dashboard's port. The address is random, changes on every start, carries no live streams and is limited to 200 requests at a time.",
    source: "cloudflared, supervised by orch dashboard (internal/tunnel)",
  },
}
