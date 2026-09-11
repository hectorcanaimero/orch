import type { Badge } from "@/components/ui/badge"

/**
 * Map a task's raw status string to the shadcn Badge variant used across
 * the dashboard (Kanban, list view, detail modal). Kept in `lib/` so that
 * both pages and modal share the exact same mapping — do not duplicate.
 */
export function statusBadgeVariant(
  status: string,
): Parameters<typeof Badge>[0]["variant"] {
  switch (status) {
    case "done":
      return "success"
    case "in-progress":
    case "in_progress":
      return "warning"
    case "blocked":
      return "danger"
    case "todo":
    case "backlog":
      return "muted"
    default:
      return "outline"
  }
}

/**
 * Translate a raw status value to its display label using the
 * presentation.status_labels config from /api/config.
 * Falls back to the raw status string if the label is not configured.
 */
export function labelForStatus(
  status: string,
  labels: Record<string, string> | undefined,
): string {
  if (!labels) return status
  return labels[status] ?? status
}
