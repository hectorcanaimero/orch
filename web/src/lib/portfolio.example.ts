import type { Portfolio } from "@/lib/types"

/**
 * EXAMPLE — matches opus-2's proposed `/api/portfolio` shape (G8.5's
 * server half), agreed on but not yet landed on `main` as of when this
 * file was written. Not imported by any production code path (test
 * files are the one exception — PortfolioPage.test.tsx renders against
 * it) — it exists so PortfolioPage's layout was written and eyeballed
 * against a real five-project screenful before a real server existed to
 * ask, and as a reference the moment the real endpoint's shape needs
 * comparing against what this page actually expects. Delete this file
 * once `docs/brainstorm/go-migration-notes/sonnet-2.md`'s coordination
 * note is resolved and the real endpoint has shipped a few times
 * without its shape moving.
 */
export const EXAMPLE_PORTFOLIO: Portfolio = {
  generated_at: "2026-09-12T04:40:00Z",
  projects: [
    {
      project_id: "billing-api",
      project_name: "billing-api",
      root: "/srv/orch-projects/billing-api",
      available: true,
      total: 12,
      done: 5,
      in_progress: 1,
      blocked: 2,
      backlog: 4,
      percent_done: 41.7,
      velocity_per_day: 1.43,
      eta_days: 3.5,
      eta_date: "2026-09-15",
      confidence: "low",
      blockers: [
        { task_id: "F1.T2", title: "Stripe webhook", reason: "falta la clave de sandbox" },
      ],
      spend: { available: true, total_cost_usd: 12.37 },
      last_event: { event_type: "sprint_done", ts: "2026-09-12T04:06:48Z", task_id: "" },
    },
    {
      project_id: "marketing-site",
      project_name: "marketing-site",
      root: "/srv/orch-projects/marketing-site",
      available: true,
      total: 8,
      done: 8,
      in_progress: 0,
      blocked: 0,
      backlog: 0,
      percent_done: 100,
      velocity_per_day: 2.1,
      eta_days: null,
      eta_date: null,
      confidence: "none",
      blockers: [],
      spend: { available: true, total_cost_usd: 3.2 },
      last_event: { event_type: "sprint_done", ts: "2026-09-10T18:02:00Z", task_id: "" },
    },
    {
      project_id: "mobile-app",
      project_name: "mobile-app",
      root: "/srv/orch-projects/mobile-app",
      available: true,
      total: 40,
      done: 6,
      in_progress: 3,
      blocked: 5,
      backlog: 26,
      percent_done: 15,
      velocity_per_day: 0.4,
      eta_days: 85,
      eta_date: "2026-12-05",
      confidence: "low",
      blockers: [
        { task_id: "F2.T7", title: "Push notification cert", reason: "esperando cuenta Apple del cliente" },
        { task_id: "F3.T1", title: "Offline sync", reason: "límite de uso del proveedor de IA" },
      ],
      spend: { available: false },
      last_event: { event_type: "budget_pause", ts: "2026-09-12T02:15:00Z", task_id: "" },
    },
    {
      project_id: "internal-tools",
      project_name: "internal-tools",
      root: "/srv/orch-projects/internal-tools",
      available: true,
      total: 5,
      done: 0,
      in_progress: 0,
      blocked: 0,
      backlog: 5,
      percent_done: 0,
      velocity_per_day: 0,
      eta_days: null,
      eta_date: null,
      confidence: "none",
      blockers: [],
      spend: { available: true, total_cost_usd: 0 },
      last_event: null,
    },
    {
      project_id: "data-pipeline",
      project_name: "data-pipeline",
      root: "/srv/orch-projects/data-pipeline",
      available: false,
      reason: "database is locked",
      total: 0,
      done: 0,
      in_progress: 0,
      blocked: 0,
      backlog: 0,
      percent_done: 0,
      velocity_per_day: 0,
      eta_days: null,
      eta_date: null,
      confidence: "none",
      blockers: [],
      spend: { available: false },
      last_event: null,
    },
  ],
  unavailable: [{ root: "/srv/orch-projects/viejo", reason: "no tasks.json" }],
}
