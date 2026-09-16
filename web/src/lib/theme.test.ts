import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { initTheme, nextTheme, useTheme } from "@/lib/theme"

function mockSystemDark(dark: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: query === "(prefers-color-scheme: dark)" && dark,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  )
}

const isDark = () => document.documentElement.classList.contains("dark")

describe("theme", () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove("dark")
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it("starts dark when nothing was chosen, even on a light OS", () => {
    mockSystemDark(false)
    initTheme()
    expect(isDark()).toBe(true)
    expect(renderHook(() => useTheme()).result.current[0]).toBe("dark")
  })

  it("cycles dark → light → system → dark", () => {
    expect(nextTheme("dark")).toBe("light")
    expect(nextTheme("light")).toBe("system")
    expect(nextTheme("system")).toBe("dark")
  })

  it("resolves system from prefers-color-scheme", () => {
    localStorage.setItem("orch_theme", "system")
    mockSystemDark(false)
    initTheme()
    expect(isDark()).toBe(false)

    mockSystemDark(true)
    initTheme()
    expect(isDark()).toBe(true)
  })

  it("applies and remembers a choice", () => {
    mockSystemDark(true)
    const { result } = renderHook(() => useTheme())
    act(() => result.current[1]("light"))
    expect(isDark()).toBe(false)
    expect(localStorage.getItem("orch_theme")).toBe("light")
    expect(result.current[0]).toBe("light")
  })

  // Private windows and blocked site data make storage throw; the page must
  // still paint a theme and let the toggle work for this view.
  it("keeps working when localStorage throws", () => {
    mockSystemDark(false)
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked")
    })
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked")
    })
    expect(() => initTheme()).not.toThrow()
    expect(isDark()).toBe(true)

    const { result } = renderHook(() => useTheme())
    expect(() => act(() => result.current[1]("light"))).not.toThrow()
    expect(isDark()).toBe(false)
  })
})
