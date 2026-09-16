import { t } from "@/i18n"
import type { CiStatus, Task } from "@/lib/types"

// /api/ci: how the project's pipeline is set up (internal/dashboard/ciroute.go).
export interface CiConfig {
  worktree_mode: boolean
  base_branch: string
  provider: string
  host: string
  auto_pr: boolean
  ci_max_retries: number
  ci_poll_interval_s: number
  auto_merge: boolean
  test_command: string
}

export interface CiJob {
  id: string
  name: string
  needs: string[]
  steps: string[]
  parse_error?: string
}

export interface CiWorkflow {
  file: string
  name: string
  triggers: string[]
  jobs: CiJob[]
  runs_on_pull_requests: boolean
  parse_error?: string
}

export interface CiResponse {
  config: CiConfig
  workflows: CiWorkflow[]
  warnings: string[]
}

// The engine's defaults when config.yaml leaves a value at zero
// (internal/engine/cipoll.go).
const DEFAULT_MAX_RETRIES = 2
const DEFAULT_POLL_S = 30

export type StageState = "on" | "off" | "missing"

export interface Stage {
  key: "branch" | "pr" | "checks" | "retry" | "merge"
  title: string
  state: StageState
  // What the stage does with this configuration, in one sentence.
  detail: string
  settings: { label: string; value: string }[]
}

// pipelineStages is the path a task's work takes after the agent finishes,
// with each step as this project configured it.
export function pipelineStages({ config, workflows }: CiResponse): Stage[] {
  const retries = config.ci_max_retries > 0 ? config.ci_max_retries : DEFAULT_MAX_RETRIES
  const poll = config.ci_poll_interval_s > 0 ? config.ci_poll_interval_s : DEFAULT_POLL_S
  const prWorkflows = workflows.filter((w) => w.runs_on_pull_requests)
  const jobs = prWorkflows.flatMap((w) => w.jobs.map((j) => j.name || j.id))
  const base = config.base_branch || "main"
  const prs = config.worktree_mode && config.auto_pr

  return [
    {
      key: "branch",
      title: t("ci.branch.title"),
      state: config.worktree_mode ? "on" : "off",
      detail: config.worktree_mode
        ? t("ci.branch.on", { base })
        : t("ci.branch.off"),
      settings: [
        { label: "dispatch.worktree_mode", value: String(config.worktree_mode) },
        { label: "dispatch.base_branch", value: base },
      ],
    },
    {
      key: "pr",
      title: t("ci.pr.title"),
      state: prs ? "on" : "off",
      detail: prs
        ? t("ci.pr.on", { base })
        : t("ci.pr.off"),
      settings: [
        { label: "vcs.auto_pr", value: String(config.auto_pr) },
        { label: "vcs.provider", value: config.provider || "github" },
      ],
    },
    {
      key: "checks",
      title: t("ci.checks.title"),
      state: !prs ? "off" : prWorkflows.length > 0 ? "on" : "missing",
      detail: !prs
        ? t("ci.checks.off")
        : prWorkflows.length > 0
          ? t("ci.checks.on", {
              jobs: t("ci.checks.jobs", { count: jobs.length }),
              workflows: t("ci.checks.workflows", { count: prWorkflows.length }),
              poll,
            })
          : t("ci.checks.missing"),
      settings: [
        { label: t("ci.checks.setting"), value: prWorkflows.map((w) => w.name || w.file).join(", ") || t("ci.checks.none") },
        { label: "vcs.ci_poll_interval_s", value: `${poll}s` },
      ],
    },
    {
      key: "retry",
      title: t("ci.retry.title"),
      state: prs ? "on" : "off",
      detail: prs
        ? t("ci.retry.on", { times: t("ci.retry.times", { count: retries }) })
        : t("ci.retry.off"),
      settings: [{ label: "vcs.ci_max_retries", value: String(retries) }],
    },
    {
      key: "merge",
      title: t("ci.merge.title"),
      state: prs && config.auto_merge ? "on" : "off",
      detail:
        prs && config.auto_merge
          ? t("ci.merge.auto")
          : prs
            ? t("ci.merge.manual")
            : t("ci.merge.off"),
      settings: [{ label: "github.auto_merge", value: String(config.auto_merge) }],
    },
  ]
}

export interface PrSplit {
  // Tasks whose PR still matters: not done yet (waiting on CI, or blocked
  // after it). Failures first, then running, by id within each.
  active: Task[]
  // Done tasks that went through a PR: history, newest ids last.
  finished: Task[]
  failing: number
}

// prTasks keeps the tasks that have a pull request, split by whether the
// work is still under review. A done task's PR was merged or closed, whatever
// its last CI reading says, so it is history, not something to act on.
export function prTasks(tasks: Task[]): PrSplit {
  const order: Record<CiStatus, number> = { failure: 0, pending: 1, success: 2, skipped: 3 }
  const withPR = tasks.filter((t) => t.pr_url)
  const active = withPR
    .filter((t) => t.status !== "done")
    .sort((a, b) => (order[a.ci_status ?? "pending"] ?? 1) - (order[b.ci_status ?? "pending"] ?? 1) || a.id.localeCompare(b.id))
  const finished = withPR.filter((t) => t.status === "done").sort((a, b) => a.id.localeCompare(b.id))
  return { active, finished, failing: active.filter((t) => t.ci_status === "failure").length }
}

// prLabel is "#277" for a GitHub PR URL, or the URL's last segment.
export function prLabel(url: string): string {
  const m = url.match(/\/pull\/(\d+)/)
  return m ? `#${m[1]}` : url.split("/").filter(Boolean).pop() ?? url
}

export function maxRetries(config: CiConfig): number {
  return config.ci_max_retries > 0 ? config.ci_max_retries : DEFAULT_MAX_RETRIES
}
