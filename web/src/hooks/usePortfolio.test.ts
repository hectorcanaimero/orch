import { describe, expect, it } from "vitest"
import { isPortfolioDisabled, isPortfolioNavVisible } from "@/hooks/usePortfolio"
import { PortfolioShapeError } from "@/lib/api"

// axios doesn't need to be a real request/response — isPortfolioDisabled
// only ever reads `error.response.status`, so a plain object shaped like
// the slice of AxiosError it touches is enough and keeps this a pure,
// no-network unit test.
function errorWithStatus(status: number) {
  return { response: { status } }
}

describe("isPortfolioDisabled", () => {
  it("is true for a 404 (dashboard not started with --portfolio)", () => {
    expect(isPortfolioDisabled(errorWithStatus(404))).toBe(true)
  })

  it("is false for a different status", () => {
    expect(isPortfolioDisabled(errorWithStatus(500))).toBe(false)
    expect(isPortfolioDisabled(errorWithStatus(401))).toBe(false)
  })

  it("is false for no error at all", () => {
    expect(isPortfolioDisabled(undefined)).toBe(false)
    expect(isPortfolioDisabled(null)).toBe(false)
  })

  it("is false for an error with no response (network failure, not an HTTP status)", () => {
    expect(isPortfolioDisabled({})).toBe(false)
  })

  // The other real 404: a single-project dashboard has no /api/portfolio
  // route registered at all, so the request falls through to the SPA's
  // catch-all and resolves 200 with the HTML shell — getPortfolio turns
  // that into a PortfolioShapeError rather than an HTTP-status error, and
  // this must read exactly the same as a real 404 downstream.
  it("is true for a PortfolioShapeError (200 with a non-portfolio body)", () => {
    expect(isPortfolioDisabled(new PortfolioShapeError())).toBe(true)
  })
})

describe("isPortfolioNavVisible", () => {
  it("is always hidden for a stakeholder, regardless of data or error", () => {
    expect(
      isPortfolioNavVisible({ isStakeholder: true, data: { projects: [] }, error: undefined }),
    ).toBe(false)
    expect(
      isPortfolioNavVisible({ isStakeholder: true, data: undefined, error: errorWithStatus(500) }),
    ).toBe(false)
  })

  it("is hidden while the initial request is still in flight (no data, no error yet)", () => {
    expect(
      isPortfolioNavVisible({ isStakeholder: false, data: undefined, error: undefined }),
    ).toBe(false)
  })

  it("is visible once real data has arrived", () => {
    expect(
      isPortfolioNavVisible({ isStakeholder: false, data: { projects: [] }, error: undefined }),
    ).toBe(true)
  })

  it("is hidden on the 404 that means --portfolio was never passed", () => {
    expect(
      isPortfolioNavVisible({ isStakeholder: false, data: undefined, error: errorWithStatus(404) }),
    ).toBe(false)
  })

  it("fails open (visible) on any other error — a transient hiccup shouldn't hide a real feature", () => {
    expect(
      isPortfolioNavVisible({ isStakeholder: false, data: undefined, error: errorWithStatus(500) }),
    ).toBe(true)
  })
})
