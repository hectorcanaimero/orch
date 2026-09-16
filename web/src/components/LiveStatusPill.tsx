import type { EventStreamStatus } from "@/hooks/useEventStream"
import { fmt, t } from "@/i18n"
import { cn } from "@/lib/utils"

export interface LiveStatusPillProps {
  status: EventStreamStatus
  lastEventAt: Date | null
}

const STATE: Record<EventStreamStatus, { label: "live.open" | "live.connecting" | "live.error" | "live.closed"; dot: string }> = {
  open: { label: "live.open", dot: "bg-status-done" },
  connecting: { label: "live.connecting", dot: "bg-status-running animate-pulse" },
  error: { label: "live.error", dot: "bg-status-blocked animate-pulse" },
  closed: { label: "live.closed", dot: "bg-status-failed" },
}

/**
 * SSE "Live" status pill shown in page headers. Stateless — the parent page
 * owns the `useEventStream()` call and passes its result down so we don't
 * open a second SSE connection per page.
 */
export function LiveStatusPill({ status, lastEventAt }: LiveStatusPillProps) {
  const state = STATE[status] ?? STATE.closed
  return (
    <div
      className="inline-flex h-8 items-center gap-2 rounded-full border bg-card px-3 text-xs text-muted-foreground"
      title={lastEventAt ? t("live.last_event", { time: fmt.time(lastEventAt) }) : t("live.no_events")}
    >
      <span className={cn("h-2 w-2 rounded-full", state.dot)} aria-hidden />
      <span className="font-medium text-foreground">{t(state.label)}</span>
      {lastEventAt ? <span className="tabular-nums">{fmt.time(lastEventAt)}</span> : null}
    </div>
  )
}
