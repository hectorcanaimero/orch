import { CircleCheck, CircleDashed, CircleDot, CirclePause, CircleX, type LucideIcon } from "lucide-react"
import type { Badge } from "@/components/ui/badge"
import type { MessageKey } from "@/i18n"

export type StatusKey = "backlog" | "todo" | "in_progress" | "blocked" | "failed" | "done"

export interface StatusMeta {
  key: StatusKey
  label: MessageKey
  /** A shape that carries the state on its own, for readers who can't tell the colors apart. */
  icon: LucideIcon
  /** Text/icon color. */
  text: string
  /** Solid fill (dots, graph nodes, segments). */
  fill: string
  /** Quiet tinted surface behind a label. */
  soft: string
}

/**
 * The one status vocabulary for every view: list, kanban, graph, timeline,
 * modal. Blocked is amber — a task waiting on something, not a failure. Red
 * is reserved for failed. Do not add a second mapping in a page.
 */
export const STATUS: Record<StatusKey, StatusMeta> = {
  backlog: {
    key: "backlog",
    label: "status.backlog",
    icon: CircleDashed,
    text: "text-status-queued",
    fill: "bg-status-queued",
    soft: "bg-status-queued/12",
  },
  todo: {
    key: "todo",
    label: "status.todo",
    icon: CircleDashed,
    text: "text-status-queued",
    fill: "bg-status-queued",
    soft: "bg-status-queued/12",
  },
  in_progress: {
    key: "in_progress",
    label: "status.in_progress",
    icon: CircleDot,
    text: "text-status-running",
    fill: "bg-status-running",
    soft: "bg-status-running/14",
  },
  blocked: {
    key: "blocked",
    label: "status.blocked",
    icon: CirclePause,
    text: "text-status-blocked",
    fill: "bg-status-blocked",
    soft: "bg-status-blocked/14",
  },
  failed: {
    key: "failed",
    label: "status.failed",
    icon: CircleX,
    text: "text-status-failed",
    fill: "bg-status-failed",
    soft: "bg-status-failed/14",
  },
  done: {
    key: "done",
    label: "status.done",
    icon: CircleCheck,
    text: "text-status-done",
    fill: "bg-status-done",
    soft: "bg-status-done/14",
  },
}

/** Normalize a raw status string (the API spells in-progress with a hyphen) to a known key. */
export function statusKey(status: string): StatusKey {
  switch (status) {
    case "in-progress":
    case "in_progress":
      return "in_progress"
    case "blocked":
    case "done":
    case "todo":
    case "failed":
      return status
    default:
      return "backlog"
  }
}

export function statusMeta(status: string): StatusMeta {
  return STATUS[statusKey(status)]
}

/**
 * Badge variant for a raw status, for callers that render a plain Badge.
 * Same scale as STATUS: blocked is a warning, only failed is danger.
 */
export function statusBadgeVariant(status: string): Parameters<typeof Badge>[0]["variant"] {
  switch (statusKey(status)) {
    case "done":
      return "success"
    case "in_progress":
      return "info"
    case "blocked":
      return "warning"
    case "failed":
      return "danger"
    default:
      return "muted"
  }
}

/**
 * Whether marking the critical path tells the reader anything. In a single
 * chain every task is on it, and a mark on every row is noise that trains the
 * eye to skip it — so it is shown only when some task is off the path.
 */
export function criticalPathMatters(tasks: { on_critical_path: boolean }[]): boolean {
  return tasks.some((t) => t.on_critical_path) && tasks.some((t) => !t.on_critical_path)
}

/**
 * Translate a raw status value to its display label using the
 * presentation.status_labels config from /api/config.
 * Falls back to the raw status string if the label is not configured.
 */
export function labelForStatus(status: string, labels: Record<string, string> | undefined): string {
  if (!labels) return status
  return labels[status] ?? status
}
