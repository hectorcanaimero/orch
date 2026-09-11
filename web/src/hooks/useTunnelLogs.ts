import { useEffect, useRef, useState } from "react"
import { API_BASE_URL, getToken } from "@/lib/api"

export interface UseTunnelLogsOptions {
  /** Set to false to disable the stream (e.g. panel collapsed or caps missing). */
  enabled: boolean
}

export type TunnelLogStreamStatus =
  | "idle"
  | "connecting"
  | "open"
  | "error"
  | "closed"

export interface UseTunnelLogsResult {
  lines: string[]
  status: TunnelLogStreamStatus
  /** Drop the buffered lines without touching the underlying stream. */
  clear: () => void
}

// Client-side ring cap — matches the server-side 200-line replay budget so
// the panel can't hold more history than the backend advertises.
const MAX_BUFFER_LINES = 200
// Backoff cap for the reconnect loop. Backend sends `retry: 5000` on the
// 30-min idle cutoff (design D3) — we treat that as a clean close and
// reconnect at the same cadence.
const RETRY_MS = 5_000

/**
 * Sprint E-5 — SSE consumer for `/api/tunnel/logs` (TUN-10).
 *
 * The backend stream is nameless (default `message` event) and each frame is
 * `data: <one line>\n\n`. The 30-min idle cutoff (design D3) closes the
 * stream cleanly with a `retry: 5000\n\n` frame; our reader treats a clean
 * close as a signal to reconnect after that delay.
 *
 * Auth: EventSource can't send headers, but the token-in-query fallback works
 * against `TokenAuthMiddleware`. We do NOT use `EventSource` directly because
 * we need to piggyback the bearer through `?token=` and we want the same
 * abortable-fetch shape as `useLiveLogs.ts` — otherwise the parent unmount
 * leaves a dangling native EventSource handle.
 */
export function useTunnelLogs(opts: UseTunnelLogsOptions): UseTunnelLogsResult {
  const { enabled } = opts
  const [lines, setLines] = useState<string[]>([])
  const [status, setStatus] = useState<TunnelLogStreamStatus>("idle")

  // Stable clear() handle across renders so consumers can memoize on it.
  const clearRef = useRef(() => setLines([]))
  clearRef.current = () => setLines([])

  useEffect(() => {
    if (!enabled) {
      setStatus("idle")
      return
    }

    const controller = new AbortController()
    let cancelled = false

    const appendLine = (line: string) => {
      setLines((prev) => {
        if (prev.length >= MAX_BUFFER_LINES) {
          return [...prev.slice(-(MAX_BUFFER_LINES - 1)), line]
        }
        return [...prev, line]
      })
    }

    const readOnce = async () => {
      setStatus("connecting")
      const token = getToken()
      const url = new URL("/api/tunnel/logs", API_BASE_URL)
      if (token) url.searchParams.set("token", token)

      const headers: Record<string, string> = { Accept: "text/event-stream" }
      // Belt-and-suspenders: also send the header when we have one so the
      // TokenAuthMiddleware primary path fires and the URL param is only
      // a fallback for clients that can't set headers at all.
      if (token) headers.Authorization = `Bearer ${token}`

      const res = await fetch(url.toString(), {
        headers,
        signal: controller.signal,
      })
      if (!res.ok || !res.body) {
        throw new Error(`SSE HTTP ${res.status}`)
      }
      setStatus("open")

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ""

      try {
        while (true) {
          const { value, done } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          // Frames are separated by a blank line ("\n\n"); keep the trailing
          // partial frame in the buffer for the next chunk.
          const frames = buffer.split("\n\n")
          buffer = frames.pop() ?? ""
          for (const raw of frames) {
            if (!raw.trim()) continue
            for (const rawLine of raw.split("\n")) {
              const line = rawLine.replace(/\r$/, "")
              if (!line || line.startsWith(":")) continue
              if (line.startsWith("data:")) {
                appendLine(line.slice(5).replace(/^ /, ""))
              }
              // `retry:` frames are informational — the backend uses them
              // to signal the 30-min idle cutoff (D3). We honor them by
              // returning from the read loop; the outer while-loop will
              // reconnect after RETRY_MS.
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
    }

    const sleep = (ms: number) =>
      new Promise<void>((resolve, reject) => {
        if (controller.signal.aborted) {
          reject(new DOMException("Aborted", "AbortError"))
          return
        }
        const timer = setTimeout(() => {
          controller.signal.removeEventListener("abort", onAbort)
          resolve()
        }, ms)
        const onAbort = () => {
          clearTimeout(timer)
          controller.signal.removeEventListener("abort", onAbort)
          reject(new DOMException("Aborted", "AbortError"))
        }
        controller.signal.addEventListener("abort", onAbort)
      })

    const run = async () => {
      while (!cancelled && !controller.signal.aborted) {
        try {
          await readOnce()
          // Server closed cleanly — treat as retry after the standard delay.
          if (cancelled || controller.signal.aborted) return
          setStatus("closed")
        } catch (err) {
          if (
            cancelled ||
            controller.signal.aborted ||
            (err instanceof Error && err.name === "AbortError")
          ) {
            return
          }
          setStatus("error")
        }
        try {
          await sleep(RETRY_MS)
        } catch {
          return
        }
      }
    }

    void run()

    return () => {
      cancelled = true
      controller.abort()
      setStatus("closed")
    }
  }, [enabled])

  return {
    lines,
    status,
    clear: () => clearRef.current(),
  }
}
