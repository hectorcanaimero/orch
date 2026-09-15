import { afterEach, describe, expect, it, vi } from "vitest"
import { currentPhase, deliveriesSince, parseRoute, phaseName, phaseState, qualityLine, rememberLinkToken, renderMarkdown, snapshotURL, statusLine } from "@/stakeholder/portal"
import type { StakeholderMilestone, StakeholderSnapshot } from "@/stakeholder/types"

function milestone(phase: number, name: string, done: number, total: number, extra: Partial<StakeholderMilestone> = {}): StakeholderMilestone {
  return { phase, name, done, total, in_progress: 0, blocked: 0, backlog: total - done, percent_done: total ? (done / total) * 100 : 0, complete: total > 0 && done === total, ...extra }
}

function snapshot(over: Partial<StakeholderSnapshot> = {}): StakeholderSnapshot {
  return {
    schema: 1,
    generated_at: "2026-09-14T12:00:00Z",
    project_name: "usebot",
    refresh_interval_s: 0,
    summary: { total: 51, done: 44, in_progress: 0, blocked: 1, backlog: 6, percent_done: 86.3, estimate_hours_total: 183, eta_hours: 1.7, eta_date: "2026-09-16", eta_confidence: "high" },
    milestones: [milestone(5, "Marca y producto", 15, 15), milestone(6, "Campaña de lanzamiento", 4, 11, { blocked: 1 })],
    blockers: [{ phase: 6, title: "Plan de campaña", reason: "En espera de una decisión" }],
    budget: { enabled: false },
    executive_summary: { text: "", language: "es" },
    ...over,
  }
}

describe("statusLine", () => {
  // The first thing a client reads answers "are we on track, and when?" in a
  // sentence — not "86%", "ETA 1.7h" or "Phase 6".
  it("leads with the finish date, then where the work is and what waits", () => {
    const s = statusLine(snapshot(), "es")
    expect(s.headline).toBe("Estimamos terminar el 16 de septiembre.")
    expect(s.detail).toBe("Ahora: Campaña de lanzamiento. 1 tema en espera.")
  })

  it("says a low-confidence date is preliminary, and speaks English when the project does", () => {
    const s = statusLine(snapshot({ summary: { ...snapshot().summary, eta_confidence: "low", blocked: 2 } }), "en")
    expect(s.headline).toBe("We expect to finish around September 16 (early estimate).")
    expect(s.detail).toBe("Now: Campaña de lanzamiento. 2 items on hold.")
  })

  it("does not promise a date it does not have", () => {
    const base = snapshot().summary
    const s = statusLine(snapshot({ summary: { ...base, eta_date: undefined, eta_confidence: undefined, blocked: 0 } }), "es")
    expect(s.headline).toBe("El trabajo está en curso.")
    expect(s.detail).toBe("Ahora: Campaña de lanzamiento.")
  })

  it("says so when everything is delivered", () => {
    const s = statusLine(snapshot({ summary: { ...snapshot().summary, done: 51, backlog: 0, blocked: 0 }, milestones: [milestone(5, "Marca", 15, 15)] }), "es")
    expect(s.headline).toBe("Todo lo planificado está entregado.")
    expect(s.detail).toBe("")
  })
})

describe("phaseName", () => {
  it("drops the plan's phase number, keeps a name without one", () => {
    expect(phaseName("F0 — Foundation")).toBe("Foundation")
    expect(phaseName("F12 - Pagos")).toBe("Pagos")
    expect(phaseName("Seguridad")).toBe("Seguridad")
  })
})

describe("currentPhase / phaseState", () => {
  it("is the first phase not yet complete, in phase order", () => {
    const ms = [milestone(6, "B", 1, 3), milestone(2, "A", 5, 5), milestone(7, "C", 0, 2)]
    expect(currentPhase(ms)?.name).toBe("B")
    const cur = currentPhase(ms)
    expect(ms.map((m) => phaseState(m, cur))).toEqual(["now", "done", "next"])
  })
})

describe("deliveriesSince", () => {
  const deliveries = [
    { title: "Newest", phase: "X", finished_at: "2026-09-14T09:00:00Z" },
    { title: "Mid", phase: "X", finished_at: "2026-09-10T09:00:00Z" },
    { title: "Old", phase: "X", finished_at: "2026-09-01T09:00:00Z" },
  ]
  const now = new Date("2026-09-14T12:00:00Z")

  it("shows what arrived after the reader's last visit", () => {
    expect(deliveriesSince(deliveries, "2026-09-09T00:00:00Z", now).map((d) => d.title)).toEqual(["Newest", "Mid"])
  })

  it("shows the last seven days on a first visit", () => {
    expect(deliveriesSince(deliveries, null, now).map((d) => d.title)).toEqual(["Newest", "Mid"])
  })

  it("copes with no deliveries at all", () => {
    expect(deliveriesSince(undefined, null, now)).toEqual([])
  })
})

describe("renderMarkdown", () => {
  // Documents are the operator's files, but the page is a client's: whatever
  // the markdown carries, only safe HTML reaches the DOM.
  it("renders markdown and strips anything executable", () => {
    const html = renderMarkdown("# Título\n\nTexto con **énfasis**.\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[enlace](javascript:alert(1))")
    expect(html).toContain("<h1>Título</h1>")
    expect(html).toContain("<strong>énfasis</strong>")
    expect(html).not.toMatch(/<script|onerror|javascript:/i)
  })
})

describe("parseRoute", () => {
  it("reads the tab and the open document from the hash", () => {
    expect(parseRoute("")).toEqual({ tab: "overview" })
    expect(parseRoute("#roadmap")).toEqual({ tab: "roadmap" })
    expect(parseRoute("#documents")).toEqual({ tab: "documents" })
    expect(parseRoute("#documents/portal-para-clientes")).toEqual({ tab: "documents", doc: "portal-para-clientes" })
    expect(parseRoute("#nonsense")).toEqual({ tab: "overview" })
  })
})

describe("snapshotURL", () => {
  // Served live, the data sits behind the token the shared link carries; a
  // published folder has no token and reads the file beside it.
  it("forwards the link's token, and only that", () => {
    expect(snapshotURL("?token=abc%2B1", null)).toBe("./data.json?token=abc%2B1")
    expect(snapshotURL("", null)).toBe("./data.json")
    expect(snapshotURL("?utm_source=mail", null)).toBe("./data.json")
  })

  // #274: the v0.12 dashboard kept the token in localStorage and took it out
  // of the address bar, so its bookmarks and reloads carry none.
  it("falls back to the token this browser saved, and the link's wins", () => {
    expect(snapshotURL("", "saved")).toBe("./data.json?token=saved")
    expect(snapshotURL("?token=fresh", "saved")).toBe("./data.json?token=fresh")
  })
})

describe("rememberLinkToken", () => {
  afterEach(() => {
    vi.restoreAllMocks()
    window.localStorage.clear()
  })

  it("saves the link's token for later visits without it", () => {
    expect(rememberLinkToken("?token=abc")).toBe("abc")
    expect(window.localStorage.getItem("orch_token")).toBe("abc")
    expect(rememberLinkToken("")).toBe("abc")
  })

  // The key the v0.12 dashboard wrote, read back as it left it (#274).
  it("returns a token saved by the operator dashboard", () => {
    window.localStorage.setItem("orch_token", "from-v012")
    expect(rememberLinkToken("?utm_source=mail")).toBe("from-v012")
  })

  it("answers nothing when storage is unavailable", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked")
    })
    expect(rememberLinkToken("")).toBeNull()
  })
})

describe("qualityLine", () => {
  const q = { gates: ["tests", "review"] as const, delivered: 44, verified: 31, first_pass: 25 }

  it("says how delivered work fared, in the reader's language", () => {
    expect(qualityLine({ ...q, gates: [...q.gates] }, "es")).toBe("31 de 44 entregas pasaron todas las verificaciones, 25 a la primera.")
    expect(qualityLine({ ...q, gates: [...q.gates] }, "en")).toBe("31 of 44 deliveries passed every check, 25 on the first try.")
  })

  it("says nothing before anything is delivered, or without the block", () => {
    expect(qualityLine({ ...q, gates: [...q.gates], delivered: 0, verified: 0, first_pass: 0 }, "es")).toBe("")
    expect(qualityLine(undefined, "en")).toBe("")
  })
})
