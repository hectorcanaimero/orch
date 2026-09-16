import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { AlertTriangle, ChevronDown, Search } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { LiveStatusPill } from "@/components/LiveStatusPill"
import { useDebouncedValue } from "@/components/TaskFiltersBar"
import { useEventsHistory } from "@/hooks/useEventsHistory"
import { useLiveLogs } from "@/hooks/useLiveLogs"
import { t } from "@/i18n"
import { cn } from "@/lib/utils"
import type { FormattedEvent } from "@/lib/types"

/** Event types the operator can filter on. Empty string = "All types". */
// The engine's own event names, shown as they appear in the stream.
const EVENT_TYPE_OPTIONS = ["dispatch", "success", "fail", "timeout", "block", "retry"] as const

/**
 * Dedup / severity mapping.
 *
 * Backend severity strings (see `orchestrator/dashboard/log_stream.py`):
 *   ok | err | warn | block | info | muted
 *
 * We normalize them to a dot color class + a text color class so the
 * terminal-style row stays consistent regardless of the underlying event
 * type. Unknown severities fall through to "muted" so a new backend value
 * still renders.
 */
// Same scale as task status: red only for failures, amber for anything that
// waits (warnings, blocks), blue for information.
const SEVERITY_DOT_CLASS: Record<string, string> = {
  ok: "bg-status-done",
  success: "bg-status-done",
  err: "bg-status-failed",
  danger: "bg-status-failed",
  warn: "bg-status-blocked",
  warning: "bg-status-blocked",
  block: "bg-status-blocked",
  info: "bg-status-running",
  muted: "bg-status-queued",
}

const SEVERITY_TEXT_CLASS: Record<string, string> = {
  ok: "text-status-done",
  success: "text-status-done",
  err: "text-status-failed",
  danger: "text-status-failed",
  warn: "text-status-blocked",
  warning: "text-status-blocked",
  block: "text-status-blocked",
  info: "text-status-running",
  muted: "text-muted-foreground",
}

function dotClass(sev: string): string {
  return SEVERITY_DOT_CLASS[sev] ?? SEVERITY_DOT_CLASS.muted
}

function textClass(sev: string): string {
  return SEVERITY_TEXT_CLASS[sev] ?? SEVERITY_TEXT_CLASS.muted
}

function eventKey(ev: FormattedEvent): string {
  return `${ev.ts}|${ev.task_id}|${ev.event_type}`
}

/**
 * Split `event.human` into (timestamp, icon, task_id, event_type, tail) using
 * the format produced by `format_event()`:
 *
 *   "[hh:mm:ss] icon task_id event_type tail..."
 *
 * We only need the TAIL for rendering — timestamp, task_id, event_type are
 * pulled from the structured fields directly. When parsing fails we just
 * return the whole human string as tail and let the row render it verbatim.
 */
function extractTail(ev: FormattedEvent): string {
  // "[hh:mm:ss] ICON task_id event_type ...tail..."
  const idx = ev.human.indexOf(ev.event_type)
  if (idx === -1) return ev.human
  const after = ev.human.slice(idx + ev.event_type.length)
  // Trim leading whitespace but keep leading punctuation like "->", "(", etc.
  return after.replace(/^\s+/, "")
}

function extractHms(ts: string): string {
  // ISO "2026-08-21T02:04:19Z" → "02:04:19"
  return ts.length >= 19 ? ts.slice(11, 19) : ts
}

export function LogsPage() {
  const [searchInput, setSearchInput] = useState("")
  const [eventType, setEventType] = useState<string>("")
  const [taskIdInput, setTaskIdInput] = useState("")
  const [followTail, setFollowTail] = useState(true)

  // Debounce filters so we don't refetch/rerender on every keystroke.
  const debouncedSearch = useDebouncedValue(searchInput, 200)
  const debouncedTaskId = useDebouncedValue(taskIdInput.trim(), 300)
  const taskIdFilter = debouncedTaskId || undefined

  const history = useEventsHistory({
    taskId: taskIdFilter,
    limit: 200,
  })
  const live = useLiveLogs({
    enabled: true,
    taskId: taskIdFilter,
  })

  // Merge + dedupe (history first, then any live rows we haven't seen).
  const merged = useMemo<FormattedEvent[]>(() => {
    const seen = new Set<string>()
    const out: FormattedEvent[] = []
    const push = (ev: FormattedEvent) => {
      const k = eventKey(ev)
      if (seen.has(k)) return
      seen.add(k)
      out.push(ev)
    }
    for (const ev of history.data?.events ?? []) push(ev)
    for (const ev of live.events) push(ev)
    return out
  }, [history.data, live.events])

  // Apply client-side text + event-type filters after dedupe.
  const filtered = useMemo<FormattedEvent[]>(() => {
    const q = debouncedSearch.trim().toLowerCase()
    if (!q && !eventType) return merged
    return merged.filter((ev) => {
      if (eventType && ev.event_type !== eventType) return false
      if (q && !ev.human.toLowerCase().includes(q)) return false
      return true
    })
  }, [merged, debouncedSearch, eventType])

  // --- scroll / follow-tail wiring ----------------------------------------
  const containerRef = useRef<HTMLDivElement | null>(null)

  // Auto-scroll to bottom whenever the visible-row count grows and follow is ON.
  useLayoutEffect(() => {
    if (!followTail) return
    const el = containerRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
  }, [filtered.length, followTail])

  // Detect user scroll — if they scroll up meaningfully, turn follow OFF.
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const onScroll = () => {
      const distanceFromBottom =
        el.scrollHeight - el.scrollTop - el.clientHeight
      if (distanceFromBottom > 8) {
        setFollowTail((prev) => (prev ? false : prev))
      }
    }
    el.addEventListener("scroll", onScroll, { passive: true })
    return () => el.removeEventListener("scroll", onScroll)
  }, [])

  // When user re-enables follow, jump to bottom immediately.
  const handleToggleFollow = () => {
    setFollowTail((prev) => {
      const next = !prev
      if (next) {
        // Defer so the state flip has committed before we scroll.
        requestAnimationFrame(() => {
          const el = containerRef.current
          if (el) el.scrollTop = el.scrollHeight
        })
      }
      return next
    })
  }

  const jumpToBottom = () => {
    setFollowTail(true)
    requestAnimationFrame(() => {
      const el = containerRef.current
      if (el) el.scrollTop = el.scrollHeight
    })
  }

  return (
    // h-[calc(100vh-4rem)] accounts for AppLayout's py-8 (2rem top + 2rem bottom).
    // Flex column so header + toolbar (+ optional alert) stay at natural height and
    // the terminal grows to consume all remaining vertical space.
    <div className="flex h-[calc(100vh-4rem)] flex-col gap-6">
      <header className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("logs.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground tabular-nums">
            {history.isLoading
              ? t("logs.loading")
              : t("logs.shown", { count: filtered.length })}
          </p>
        </div>
        <LiveStatusPill
          status={live.status}
          lastEventAt={live.lastEventAt}
        />
      </header>

      {/* Toolbar */}
      <div className="flex flex-shrink-0 flex-wrap items-center gap-2">
        <div className="relative min-w-[220px] flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            placeholder={t("logs.filter")}
            className="pl-9"
            aria-label={t("logs.filter")}
          />
        </div>
        <div className="w-40">
          <Select value={eventType} onValueChange={setEventType}>
            <SelectTrigger aria-label={t("logs.event_type")}>
              <SelectValue placeholder={t("logs.all_types")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("logs.all_types")}</SelectItem>
              {EVENT_TYPE_OPTIONS.map((type) => (
                <SelectItem key={type} value={type}>
                  <span className="font-mono text-xs">{type}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Input
          type="text"
          value={taskIdInput}
          onChange={(e) => setTaskIdInput(e.target.value)}
          placeholder={t("logs.task_id")}
          className="h-10 w-[200px] font-mono text-xs"
          aria-label={t("logs.task_id_label")}
        />
        <Button
          type="button"
          variant={followTail ? "default" : "outline"}
          size="sm"
          onClick={handleToggleFollow}
          className="h-10"
          aria-pressed={followTail}
        >
          {followTail ? t("logs.following") : t("logs.follow")}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={live.clear}
          className="h-10"
        >
          {t("logs.clear")}
        </Button>
      </div>

      {history.isError ? (
        <Alert variant="destructive" className="flex-shrink-0">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("logs.history_failed")}</AlertTitle>
          <AlertDescription>
            {t("logs.history_failed_detail", { message: history.error?.message ?? t("common.unknown_error") })}
          </AlertDescription>
        </Alert>
      ) : null}

      {/* Terminal body — flex-1 + min-h-0 so it can shrink below intrinsic
          content height. Without min-h-0 the flex child refuses to shrink
          and the inner scroll never engages. */}
      <div className="relative flex min-h-0 flex-1">
        <div
          ref={containerRef}
          className={cn(
            "flex-1 rounded-xl border bg-card font-mono text-xs text-card-foreground",
            "overflow-auto",
          )}
        >
          {history.isLoading ? (
            <div className="space-y-2 p-3">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton
                  key={`log-skeleton-${i}`}
                  className="h-5 w-full"
                />
              ))}
            </div>
          ) : filtered.length === 0 ? (
            <div className="p-8 text-center font-sans text-sm text-muted-foreground">
              {t("logs.waiting")}
            </div>
          ) : (
            <ul className="divide-y divide-border/60">
              {filtered.map((ev) => (
                <li
                  key={eventKey(ev)}
                  className="flex items-baseline gap-2.5 px-3 py-1.5 hover:bg-accent/50"
                >
                  <span className="tabular-nums text-muted-foreground">
                    {extractHms(ev.ts)}
                  </span>
                  <span
                    className={cn(
                      "inline-block h-2 w-2 rounded-full shrink-0",
                      dotClass(ev.severity),
                    )}
                    aria-hidden
                  />
                  <Badge
                    variant="outline"
                    className="bg-transparent px-1.5 py-0 font-mono text-[11px] font-normal"
                  >
                    {ev.task_id}
                  </Badge>
                  <span
                    className={cn(
                      "text-[11px] font-medium",
                      textClass(ev.severity),
                    )}
                  >
                    {ev.event_type}
                  </span>
                  <span className="truncate text-foreground/85">
                    {extractTail(ev)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>

        {!followTail && filtered.length > 0 ? (
          <button
            type="button"
            onClick={jumpToBottom}
            className={cn(
              "absolute bottom-3 left-1/2 -translate-x-1/2",
              "inline-flex items-center gap-1 rounded-full",
              "border bg-popover px-3 py-1 text-xs text-popover-foreground",
              "shadow-[0_8px_20px_-8px_rgb(0_0_0/0.45)] hover:bg-accent",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            )}
          >
            <ChevronDown className="h-3.5 w-3.5" />
            {t("logs.new_events")}
          </button>
        ) : null}
      </div>
    </div>
  )
}
