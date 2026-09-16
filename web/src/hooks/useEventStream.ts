import { useEffect, useRef, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { API_BASE_URL, getToken } from "@/lib/api"
import { startEventStream } from "@/lib/sse"
import type { EventPayload } from "@/lib/types"

export type EventStreamStatus =
  | "connecting"
  | "open"
  | "error"
  | "closed"

export interface UseEventStreamOptions {
  /** Optional task_id filter — server will only tail that task's events. */
  taskId?: string
  /** Set to false to disable the stream (e.g. when unauthenticated). */
  enabled?: boolean
}

/** How often the fallback refreshes the task list while the stream is down. */
export const POLL_FALLBACK_MS = 15_000

/**
 * A stream that has sent nothing — not even the server's keep-alive, every
 * 25s (internal/dashboard/stream.go) — for this long is not flowing. A proxy
 * that buffers responses does that: Cloudflare quick tunnels do not support
 * server-sent events, so through `orch dashboard --tunnel` the connection
 * opens and then delivers nothing.
 */
export const STREAM_STALE_MS = 60_000

/**
 * Whether the page has to refresh by polling: the stream is not open, or it
 * is open and silent past two keep-alives.
 */
export function needsPolling(
  status: EventStreamStatus,
  lastActivityAt: number | null,
  now: number,
): boolean {
  if (status !== "open") return true
  return lastActivityAt === null || now - lastActivityAt > STREAM_STALE_MS
}

export interface UseEventStreamResult {
  status: EventStreamStatus
  lastEventAt: Date | null
}

/**
 * Opens a long-lived SSE connection to /api/events/stream and invalidates
 * relevant TanStack Query caches on every event. Reconnects with backoff via
 * `startEventStream`.
 */
export function useEventStream(
  opts: UseEventStreamOptions = {},
): UseEventStreamResult {
  const { taskId, enabled = true } = opts
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<EventStreamStatus>("connecting")
  const [lastEventAt, setLastEventAt] = useState<Date | null>(null)

  // Keep a stable ref to the client so the effect deps stay minimal.
  const qcRef = useRef(queryClient)
  qcRef.current = queryClient
  const statusRef = useRef<EventStreamStatus>("connecting")
  statusRef.current = status
  const activityRef = useRef<number | null>(null)

  // The fallback: while the stream is not delivering, refresh on a timer so
  // the page still moves — slower, but never frozen.
  useEffect(() => {
    if (!enabled) return
    const id = window.setInterval(() => {
      if (needsPolling(statusRef.current, activityRef.current, Date.now())) {
        void qcRef.current.invalidateQueries({ queryKey: ["tasks"] })
      }
    }, POLL_FALLBACK_MS)
    return () => window.clearInterval(id)
  }, [enabled])

  useEffect(() => {
    if (!enabled) {
      setStatus("closed")
      return
    }

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
        activityRef.current = Date.now()
        setStatus("open")
      },
      onActivity: () => {
        activityRef.current = Date.now()
      },
      onError: (err) => {
        // eslint-disable-next-line no-console
        console.warn("[sse] stream error, will retry", err)
        setStatus("error")
      },
      onEvent: (_eventName, payload: EventPayload) => {
        setLastEventAt(new Date())
        // Always refresh the task list.
        void qcRef.current.invalidateQueries({ queryKey: ["tasks"] })
        // If the event references a specific task, refresh its detail too.
        if (typeof payload.task_id === "string" && payload.task_id.length > 0) {
          void qcRef.current.invalidateQueries({
            queryKey: ["task", payload.task_id],
          })
        }
      },
    }).finally(() => {
      // Only mark closed if we weren't already reporting an error state and
      // the effect wasn't torn down by abort.
      if (!controller.signal.aborted) {
        setStatus("closed")
      }
    })

    return () => {
      controller.abort()
      setStatus("closed")
    }
  }, [enabled, taskId])

  return { status, lastEventAt }
}
