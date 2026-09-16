import { describe, expect, it } from "vitest"
import { formatEta } from "@/lib/eta"

describe("formatEta", () => {
  // The summary showed "1.7h" while the Pace page promised a date: one
  // figure, the finish date, wherever a date is known.
  it("shows the finish date and its confidence when the server projected one", () => {
    expect(formatEta({ eta_date: "2026-09-16", eta_confidence: "high", eta_hours: 1.7 })).toEqual({
      value: "Sep 16",
      detail: "high confidence",
    })
  })

  it("falls back to hours of work when no pace is known yet", () => {
    expect(formatEta({ eta_hours: 5 })).toEqual({ value: "5.0h", detail: "of work left" })
  })

  it("says nothing it does not know", () => {
    expect(formatEta({ eta_date: null, eta_hours: null })).toEqual({ value: "—", detail: "" })
  })
})
