import { describe, expect, it } from "vitest"
import { pipelineStages, prLabel, prTasks, type CiResponse } from "@/lib/ci"
import type { Task } from "@/lib/types"

function ci(over: Partial<CiResponse["config"]> = {}, workflows: CiResponse["workflows"] = []): CiResponse {
  return {
    config: {
      worktree_mode: true,
      base_branch: "dev",
      provider: "github",
      host: "github.com",
      auto_pr: true,
      ci_max_retries: 0,
      ci_poll_interval_s: 0,
      auto_merge: false,
      test_command: "pnpm test",
      ...over,
    },
    workflows,
    warnings: [],
  }
}

const orchCI = {
  file: "orch-ci.yml",
  name: "orch-ci",
  triggers: ["pull_request", "push"],
  runs_on_pull_requests: true,
  jobs: [{ id: "test", name: "", needs: [], steps: ["actions/checkout@v4", "Run tests"] }],
}

describe("pipelineStages", () => {
  it("describes the full loop with the engine's defaults filled in", () => {
    const stages = pipelineStages(ci({}, [orchCI]))
    expect(stages.map((s) => [s.key, s.state])).toEqual([
      ["branch", "on"],
      ["pr", "on"],
      ["checks", "on"],
      ["retry", "on"],
      ["merge", "off"],
    ])
    expect(stages[0].detail).toContain("branched from dev")
    expect(stages[2].detail).toBe("orch waits on 1 job from 1 workflow, asking every 30s.")
    expect(stages[3].detail).toContain("up to 2 times")
    expect(stages[4].detail).toContain("waits for a person to merge")
  })

  it("marks checks missing when PRs open but no workflow runs on them", () => {
    const stages = pipelineStages(ci({}, [{ ...orchCI, runs_on_pull_requests: false }]))
    expect(stages.find((s) => s.key === "checks")?.state).toBe("missing")
  })

  it("turns everything after the branch off without auto_pr", () => {
    const stages = pipelineStages(ci({ auto_pr: false, auto_merge: true }, [orchCI]))
    expect(stages.filter((s) => s.state === "on").map((s) => s.key)).toEqual(["branch"])
  })
})

function task(id: string, over: Partial<Task> = {}): Task {
  return {
    id, phase: 1, title: id, description: "", model: "claude/sonnet", reason: "", status: "in-progress",
    dependencies: [], dep_count: 0, estimate_hours: 1, files: [], spec_ref: "", comments: [],
    human_hours: 0, last_updated: "", downstream_impact: 0, on_critical_path: false, ...over,
  }
}

describe("prTasks", () => {
  it("splits PRs still under review from finished work, failures first", () => {
    const { active, finished, failing } = prTasks([
      task("T-3", { pr_url: "https://github.com/o/r/pull/3", ci_status: "success" }),
      task("T-1"),
      task("T-2", { pr_url: "https://github.com/o/r/pull/2", ci_status: "pending" }),
      task("T-4", { pr_url: "https://github.com/o/r/pull/4", ci_status: "failure", ci_attempts: 1, status: "blocked" }),
      // Its last CI reading failed, but the task is done: the PR was merged.
      task("T-5", { pr_url: "https://github.com/o/r/pull/5", ci_status: "failure", status: "done" }),
    ])
    expect(active.map((t) => t.id)).toEqual(["T-4", "T-2", "T-3"])
    expect(finished.map((t) => t.id)).toEqual(["T-5"])
    expect(failing).toBe(1)
  })
})

describe("prLabel", () => {
  it("shortens a GitHub PR URL to its number", () => {
    expect(prLabel("https://github.com/o/r/pull/277")).toBe("#277")
    expect(prLabel("https://gitlab.example/o/r/-/merge_requests/12")).toBe("12")
  })
})
