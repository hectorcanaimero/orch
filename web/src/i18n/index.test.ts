import { afterEach, describe, expect, it } from "vitest"
import { en } from "@/i18n/en"
import { es } from "@/i18n/es"
import { pt } from "@/i18n/pt"
import { fmt, getLanguage, resolveLanguage, setLanguage, t, type MessageKey, type Plural } from "@/i18n"
import { englishLeaks } from "@/i18n/leaks"
import { formatRelative } from "@/lib/time"

afterEach(() => setLanguage("en"))

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

const placeholders = (v: string | Plural) =>
  [...new Set((typeof v === "string" ? v : `${v.one} ${v.other}`).match(/\{[a-z_]+\}/g) ?? [])].sort()

describe.each([
  ["es", es],
  ["pt", pt],
] as const)("the %s dictionary", (_, dict) => {
  // tsc already refuses a missing or extra key; this also catches a
  // translation that dropped or renamed a {placeholder}, which tsc cannot see.
  it("has exactly English's keys, each with the same placeholders", () => {
    expect(Object.keys(dict).sort()).toEqual(Object.keys(en).sort())
    for (const key of Object.keys(en) as MessageKey[]) {
      expect([key, placeholders(dict[key])]).toEqual([key, placeholders(en[key])])
    }
  })
})

describe("resolveLanguage", () => {
  it("prefers the choice saved in this browser", () => {
    expect(resolveLanguage("pt", ["es-AR", "en"])).toBe("pt")
  })

  it("then the first browser language orch speaks", () => {
    expect(resolveLanguage(null, ["fr-FR", "es-AR", "en"])).toBe("es")
    expect(resolveLanguage("klingon", ["pt-BR"])).toBe("pt")
  })

  it("falls back to English", () => {
    expect(resolveLanguage(null, ["fr", "de"])).toBe("en")
    expect(resolveLanguage(null, [])).toBe("en")
  })
})

describe("setLanguage", () => {
  it("switches every string, date, number and plural, and the page's lang", () => {
    setLanguage("pt")
    expect(getLanguage()).toBe("pt")
    expect(document.documentElement.lang).toBe("pt-BR")
    expect(t("tasks.title")).toBe("Tarefas")
    expect(t("logs.shown", { count: 1 })).toBe("1 evento exibido")
    expect(t("logs.shown", { count: 2 })).toBe("2 eventos exibidos")
    expect(fmt.number(1234.5)).toBe("1.234,5")
    expect(fmt.day("2026-09-18")).toMatch(/18.*set/i)
    expect(formatRelative("2026-09-16T09:00:00Z", Date.parse("2026-09-16T12:00:00Z"))).toBe("há 3 horas")

    setLanguage("es")
    expect(t("tasks.title")).toBe("Tareas")
    expect(fmt.day("2026-09-18")).toMatch(/18.*sept/i)
    expect(window.localStorage.getItem("orch_lang")).toBe("es")
  })
})

describe("englishLeaks", () => {
  it("finds an English sentence left on a translated page, and nothing in a translated one", () => {
    expect(englishLeaks(`<p>${en["now.all_clear"]}</p>`, "pt")).toContain(
      "Nothing needs you. No task is blocked or waiting on the budget.",
    )
    expect(englishLeaks(`<p>${pt["now.all_clear"]}</p>`, "pt")).toEqual([])
  })
})
