import type { EventPayload } from "@/lib/types"

/**
 * Manual SSE reader built on fetch streaming.
 *
 * Why not `EventSource`? The browser's native EventSource API does NOT support
 * custom headers, and our backend requires a Bearer token. So we roll our own
 * frame parser on top of a streaming fetch.
 *
 * Frame format (from the orch backend):
 *   event: event
 *   data: {json...}
 *   \n
 *
 * Note the event NAME is literally the string "event" — not "message".
 *
 * Reconnect: exponential backoff (1s → 2s → 4s → 8s cap). Backoff resets on
 * a successful connect. The caller can abort at any time via AbortSignal.
 */

export interface StartEventStreamOpts {
  url: string
  token: string | null
  onEvent: (eventName: string, payload: EventPayload) => void
  onError?: (err: unknown) => void
  onOpen?: () => void
  signal: AbortSignal
}

const INITIAL_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 8000

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason ?? new DOMException("Aborted", "AbortError"))
      return
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      signal.removeEventListener("abort", onAbort)
      reject(signal.reason ?? new DOMException("Aborted", "AbortError"))
    }
    signal.addEventListener("abort", onAbort)
  })
}

function parseFrame(
  frame: string,
): { eventName: string; data: string } | null {
  // A frame is one or more "field: value" lines separated by \n.
  const lines = frame.split("\n")
  let eventName: string | null = null
  const dataLines: string[] = []
  for (const raw of lines) {
    const line = raw.replace(/\r$/, "")
    if (!line || line.startsWith(":")) continue // comment / keep-alive
    if (line.startsWith("event:")) {
      eventName = line.slice(6).trimStart()
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  if (!eventName || dataLines.length === 0) return null
  return { eventName, data: dataLines.join("\n") }
}

export async function startEventStream(
  opts: StartEventStreamOpts,
): Promise<void> {
  const { url, token, onEvent, onError, onOpen, signal } = opts
  let backoff = INITIAL_BACKOFF_MS

  while (!signal.aborted) {
    try {
      const headers: Record<string, string> = {
        Accept: "text/event-stream",
      }
      if (token) headers.Authorization = `Bearer ${token}`

      const res = await fetch(url, { headers, signal })
      if (!res.ok || !res.body) {
        throw new Error(`SSE HTTP ${res.status}`)
      }

      // Reset backoff on successful connect.
      backoff = INITIAL_BACKOFF_MS
      onOpen?.()

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ""

      try {
        while (true) {
          const { value, done } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          // Frames are separated by a blank line, i.e. "\n\n". Split, keep
          // the last (possibly incomplete) frame in the buffer.
          const frames = buffer.split("\n\n")
          buffer = frames.pop() ?? ""
          for (const raw of frames) {
            if (!raw.trim()) continue
            const parsed = parseFrame(raw)
            if (!parsed) continue
            try {
              const payload = JSON.parse(parsed.data) as EventPayload
              onEvent(parsed.eventName, payload)
            } catch (err) {
              // Log & skip malformed payloads rather than tearing down the
              // whole stream.
              // eslint-disable-next-line no-console
              console.warn("[sse] failed to parse event data", err, parsed.data)
            }
          }
        }
      } finally {
        try {
          reader.releaseLock()
        } catch {
          // ignore
        }
      }
      // Server closed the connection cleanly — treat as a retryable case.
      if (signal.aborted) return
    } catch (err) {
      if (signal.aborted || (err instanceof Error && err.name === "AbortError")) {
        return
      }
      onError?.(err)
    }

    // Backoff before reconnecting.
    try {
      await sleep(backoff, signal)
    } catch {
      return
    }
    backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
  }
}
