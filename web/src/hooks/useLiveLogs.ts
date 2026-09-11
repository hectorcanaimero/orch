import { useEffect, useRef, useState } from "react"
import { API_BASE_URL, getToken } from "@/lib/api"
import { startEventStream } from "@/lib/sse"
import type { EventStreamStatus } from "@/hooks/useEventStream"
import type { FormattedEvent } from "@/lib/types"

export interface UseLiveLogsOptions {
  /** Set to false to disable the stream (e.g. when unauthenticated). */
  enabled: boolean
  /** Optional task_id filter — server-side scoped when provided. */
  taskId?: string
}

export interface UseLiveLogsResult {
  events: FormattedEvent[]
  status: EventStreamStatus
  lastEventAt: Date | null
  /** Reset the live buffer without dropping the underlying SSE connection. */
  clear: () => void
}

/** Cap the live buffer so a long-lived tail never grows unbounded. */
const MAX_LIVE_BUFFER = 500

/**
 * Live-tail hook for the Logs page.
 *
 * Opens an SSE connection to `/api/events/stream` (via the shared
 * `startEventStream` reader in `lib/sse.ts`) and APPENDS every incoming event
 * to a local array bounded to the last {@link MAX_LIVE_BUFFER} entries.
 *
 * Does NOT seed from history — the caller is expected to compose the
 * `useEventsHistory()` result with this hook's `events` (deduplicating on a
 * composite key) so the final list is `[...history, ...live]`.
 *
 * The `taskId` param is passed to the server URL as `?task_id=` so the
 * server-side filter runs before the payload ever hits the wire.
 */
export function useLiveLogs(opts: UseLiveLogsOptions): UseLiveLogsResult {
  const { enabled, taskId } = opts
  const [events, setEvents] = useState<FormattedEvent[]>([])
  const [status, setStatus] = useState<EventStreamStatus>("connecting")
  const [lastEventAt, setLastEventAt] = useState<Date | null>(null)

  // When taskId changes, `useEffect` re-runs and we reset the buffer so live
  // filter switches feel snappy (no stale rows from a previous scope). Use a
  // ref so the clear() imperative handle stays stable across renders.
  const clearRef = useRef(() => setEvents([]))
  clearRef.current = () => setEvents([])

  useEffect(() => {
    if (!enabled) {
      setStatus("closed")
      return
    }

    // Reset on scope changes — switching task_id must not leak rows.
    setEvents([])

    const controller = new AbortController()
    const params = taskId ? `?task_id=${encodeURIComponent(taskId)}` : ""
    const url = `${API_BASE_URL}/api/events/stream${params}`
    const token = getToken()

    setStatus("connecting")

    void startEventStream({
      url,
      token,
      signal: controller.signal,
      onOpen: () => {
        setStatus("open")
      },
      onError: (err) => {
        // eslint-disable-next-line no-console
        console.warn("[useLiveLogs] stream error, will retry", err)
        setStatus("error")
      },
      onEvent: (_eventName, payload) => {
        // The stream payload IS a FormattedEvent — the backend serializer
        // is the same for the history endpoint and the SSE frame. Guard
        // against a malformed one anyway so a single bad row can't break
        // the tail.
        const ev = payload as unknown as Partial<FormattedEvent>
        if (
          typeof ev?.ts !== "string" ||
          typeof ev?.task_id !== "string" ||
          typeof ev?.event_type !== "string" ||
          typeof ev?.human !== "string"
        ) {
          return
        }
        const normalized: FormattedEvent = {
          ts: ev.ts,
          task_id: ev.task_id,
          event_type: ev.event_type,
          backend: typeof ev.backend === "string" ? ev.backend : "",
          human: ev.human,
          severity: typeof ev.severity === "string" ? ev.severity : "muted",
          raw:
            ev.raw && typeof ev.raw === "object"
              ? (ev.raw as Record<string, unknown>)
              : {},
        }
        setLastEventAt(new Date())
        setEvents((prev) => {
          const next = prev.length >= MAX_LIVE_BUFFER ? prev.slice(-(MAX_LIVE_BUFFER - 1)) : prev
          return [...next, normalized]
        })
      },
    }).finally(() => {
      if (!controller.signal.aborted) {
        setStatus("closed")
      }
    })

    return () => {
      controller.abort()
      setStatus("closed")
    }
  }, [enabled, taskId])

  return {
    events,
    status,
    lastEventAt,
    clear: () => clearRef.current(),
  }
}
