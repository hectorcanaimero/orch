import { describe, expect, it } from "vitest"
import { t, type MessageKey } from "@/i18n"

describe("t", () => {
  it("looks a key up in the English dictionary", () => {
    expect(t("tasks.title")).toBe("Tasks")
  })

  it("fills {placeholders}, every occurrence", () => {
    expect(t("tasks.shown", { count: 3, total: 30 })).toBe("3 of 30 tasks")
    expect(t("ci.counts", { active: 1, failing: 0, finished: 2 })).toBe("1 under review · 0 failing · 2 finished")
  })

  it("picks the plural form with Intl.PluralRules from count", () => {
    expect(t("logs.shown", { count: 1 })).toBe("1 event shown")
    expect(t("logs.shown", { count: 0 })).toBe("0 events shown")
    expect(t("logs.shown", { count: 2 })).toBe("2 events shown")
  })

  // A key that is not in the dictionary (a typo that slipped past the type,
  // or a server-sent value) shows itself instead of throwing during render.
  it("returns a missing key as-is", () => {
    expect(t("no.such.key" as MessageKey)).toBe("no.such.key")
  })
})
