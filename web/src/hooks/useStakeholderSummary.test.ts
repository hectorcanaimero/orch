import { afterEach, describe, expect, it, vi } from "vitest"
import { apiClient } from "@/lib/api"
import {
  fetchStakeholderSummary,
  isStakeholderSummaryUnavailable,
  shouldRetryStakeholderSummary,
  StakeholderSummaryShapeError,
} from "@/hooks/useStakeholderSummary"
import type { StakeholderSummary } from "@/lib/types"

describe("isStakeholderSummaryUnavailable", () => {
  it("is true for a StakeholderSummaryShapeError", () => {
    expect(isStakeholderSummaryUnavailable(new StakeholderSummaryShapeError())).toBe(true)
  })

  it("is true for a real 404", () => {
    expect(isStakeholderSummaryUnavailable({ response: { status: 404 } })).toBe(true)
  })

  it("is false for other errors", () => {
    expect(isStakeholderSummaryUnavailable({ response: { status: 500 } })).toBe(false)
    expect(isStakeholderSummaryUnavailable(new Error("network down"))).toBe(false)
    expect(isStakeholderSummaryUnavailable(undefined)).toBe(false)
  })
})

// The bug this file exists to fix: the Go dashboard doesn't implement
// /stakeholder/summary yet, so the request falls through to the SPA's
// own catch-all and axios resolves 200 with the HTML shell as `data` —
// a real response, just not this one's shape. Without this guard,
// StakeholderSummaryPage's first read of `summary.done` threw a bare
// TypeError (found via PR #205's review, opus-2 running the real binary).
describe("fetchStakeholderSummary", () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("returns the response body as-is for a real summary payload", async () => {
    const sample: StakeholderSummary = {
      project_id: "demo",
      summary: {
        total: 10,
        done: 3,
        in_progress: 1,
        blocked: 0,
        backlog: 6,
        percent_done: 30,
        estimate_hours_total: 40,
      },
      milestones: [],
      spend_rounded_usd: 12.5,
      eta_hours: 8,
      refresh_interval_s: 10,
      phases_timeline: [],
      spend_by_day: [],
      exec_summary: "on track",
    }
    vi.spyOn(apiClient, "get").mockResolvedValueOnce({ data: sample })

    await expect(fetchStakeholderSummary()).resolves.toEqual(sample)
  })

  it("rejects with StakeholderSummaryShapeError when the body isn't a summary (e.g. the SPA's HTML shell on 200)", async () => {
    vi.spyOn(apiClient, "get").mockResolvedValueOnce({
      data: "<!doctype html><html>...</html>",
    })

    await expect(fetchStakeholderSummary()).rejects.toBeInstanceOf(StakeholderSummaryShapeError)
  })

  it("rejects with StakeholderSummaryShapeError when `summary` is missing entirely", async () => {
    vi.spyOn(apiClient, "get").mockResolvedValueOnce({ data: { project_id: "demo" } })

    await expect(fetchStakeholderSummary()).rejects.toBeInstanceOf(StakeholderSummaryShapeError)
  })
})

// Gemini's finding on this PR: the retry policy's own suppression logic
// (don't retry auth errors or the route-not-implemented case) had no
// test of its own — isStakeholderSummaryUnavailable was covered, but
// not the function react-query actually calls.
describe("shouldRetryStakeholderSummary", () => {
  it("does not retry the route-not-implemented case (shape error or 404)", () => {
    expect(shouldRetryStakeholderSummary(0, new StakeholderSummaryShapeError())).toBe(false)
    expect(shouldRetryStakeholderSummary(0, { response: { status: 404 } })).toBe(false)
  })

  it("does not retry auth errors", () => {
    expect(shouldRetryStakeholderSummary(0, { response: { status: 401 } })).toBe(false)
    expect(shouldRetryStakeholderSummary(0, { response: { status: 403 } })).toBe(false)
  })

  it("retries anything else up to react-query's default of 2 attempts", () => {
    expect(shouldRetryStakeholderSummary(0, { response: { status: 500 } })).toBe(true)
    expect(shouldRetryStakeholderSummary(1, { response: { status: 500 } })).toBe(true)
    expect(shouldRetryStakeholderSummary(2, { response: { status: 500 } })).toBe(false)
  })
})
