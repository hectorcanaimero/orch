import { describe, expect, it } from "vitest"
import { STREAM_STALE_MS, needsPolling } from "@/hooks/useEventStream"

// Through a Cloudflare quick tunnel the event stream opens and then delivers
// nothing: quick tunnels do not support server-sent events. The page has to
// notice and refresh on a timer instead of freezing on stale data.
describe("needsPolling", () => {
  const now = 1_000_000

  it("polls while the stream is not open", () => {
    expect(needsPolling("connecting", null, now)).toBe(true)
    expect(needsPolling("error", now, now)).toBe(true)
    expect(needsPolling("closed", now, now)).toBe(true)
  })

  it("does not poll a stream that is flowing", () => {
    expect(needsPolling("open", now - 1_000, now)).toBe(false)
    expect(needsPolling("open", now - STREAM_STALE_MS, now)).toBe(false)
  })

  it("polls an open stream that has gone silent past two keep-alives", () => {
    expect(needsPolling("open", now - STREAM_STALE_MS - 1, now)).toBe(true)
    expect(needsPolling("open", null, now)).toBe(true)
  })
})
