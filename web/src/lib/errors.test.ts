import { describe, expect, it } from "vitest"
import { describeLoadError } from "@/lib/errors"

describe("describeLoadError", () => {
  // What a reader saw before: "Request failed with status code 403".
  it.each([
    [{ response: { status: 401 } }, /session|link/i],
    [{ response: { status: 403 } }, /not available with your access/i],
    [{ response: { status: 500 } }, /dashboard ran into an error/i],
    [{ message: "Network Error" }, /couldn't reach the dashboard/i],
  ])("turns %o into a sentence a person can act on", (error, want) => {
    const text = describeLoadError(error)
    expect(text).toMatch(want)
    expect(text).not.toMatch(/status code/i)
  })

  it("keeps a plain Error's own message", () => {
    expect(describeLoadError(new Error("tasks.json is not valid JSON"))).toBe("tasks.json is not valid JSON")
  })
})
