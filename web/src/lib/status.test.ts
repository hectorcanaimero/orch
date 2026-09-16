import { describe, expect, it } from "vitest"
import { t } from "@/i18n"
import { criticalPathMatters, STATUS, statusBadgeVariant, statusKey, statusMeta } from "@/lib/status"

// The statuses internal/model/status.go defines, spelled as the API sends them.
const BACKEND_STATUSES = ["backlog", "todo", "in-progress", "done", "blocked"]

describe("status scale", () => {
  it.each(BACKEND_STATUSES)("maps %s to a token, an icon and a label", (status) => {
    const meta = statusMeta(status)
    expect(meta.text).toMatch(/^text-status-/)
    expect(meta.fill).toMatch(/^bg-status-/)
    expect(meta.icon).toBeTruthy()
    const label = t(meta.label)
    expect(label).not.toBe(meta.label)
    expect(label.length).toBeGreaterThan(0)
  })

  it("reads in-progress with either spelling", () => {
    expect(statusKey("in-progress")).toBe("in_progress")
    expect(statusKey("in_progress")).toBe("in_progress")
  })

  it("falls back to backlog for a status it does not know", () => {
    expect(statusKey("archived")).toBe("backlog")
    expect(statusMeta("")).toBe(STATUS.backlog)
    expect(statusBadgeVariant("archived")).toBe("muted")
  })

  // Blocked waits on something; failed broke. They must never share a color.
  it("keeps blocked apart from failed", () => {
    expect(STATUS.blocked.text).not.toBe(STATUS.failed.text)
    expect(STATUS.blocked.fill).not.toBe(STATUS.failed.fill)
    expect(statusBadgeVariant("blocked")).not.toBe(statusBadgeVariant("failed"))
    expect(statusBadgeVariant("blocked")).not.toBe("danger")
  })

  it("marks the critical path only when some task is off it", () => {
    expect(criticalPathMatters([{ on_critical_path: true }, { on_critical_path: true }])).toBe(false)
    expect(criticalPathMatters([{ on_critical_path: true }, { on_critical_path: false }])).toBe(true)
    expect(criticalPathMatters([{ on_critical_path: false }])).toBe(false)
  })
})
