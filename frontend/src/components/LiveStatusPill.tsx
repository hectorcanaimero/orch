import type { EventStreamStatus } from "@/hooks/useEventStream"
import { cn } from "@/lib/utils"

export interface LiveStatusPillProps {
  status: EventStreamStatus
  lastEventAt: Date | null
}

/**
 * SSE "Live" status pill shown in page headers. Stateless — the parent page
 * owns the `useEventStream()` call and passes its result down so we don't
 * open a second SSE connection per page. Extracted from KanbanPage so
 * ListPage (and future pages) can reuse the same pill.
 */
export function LiveStatusPill({ status, lastEventAt }: LiveStatusPillProps) {
  const label =
    status === "open"
      ? "Live"
      : status === "connecting"
        ? "Connecting…"
        : status === "error"
          ? "Reconnecting…"
          : "Offline"
  const dotClass =
    status === "open"
      ? "bg-emerald-500"
      : status === "connecting"
        ? "bg-sky-500 animate-pulse"
        : status === "error"
          ? "bg-amber-500 animate-pulse"
          : "bg-red-500"
  return (
    <div
      className="inline-flex items-center gap-2 rounded-full border bg-white px-3 py-1 text-xs text-muted-foreground"
      title={
        lastEventAt
          ? `Last event: ${lastEventAt.toLocaleTimeString()}`
          : "No events yet"
      }
    >
      <span className={cn("h-2 w-2 rounded-full", dotClass)} />
      {label}
      {lastEventAt ? (
        <span className="text-[10px] text-zinc-400">
          {lastEventAt.toLocaleTimeString()}
        </span>
      ) : null}
    </div>
  )
}
