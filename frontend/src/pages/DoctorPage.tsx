import { useMemo, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  MinusCircle,
  RefreshCw,
  XCircle,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useDoctor } from "@/hooks/useDoctor"
import { cn } from "@/lib/utils"
import type { DoctorCheck, DoctorStatus } from "@/lib/types"

type BadgeVariant = "success" | "warning" | "danger" | "muted"
type AlertVariant = "default" | "destructive"

/**
 * Small config table: status → (icon, badge variant, pill background, label).
 * Kept as a plain object so the compiler catches missing branches when we add
 * a new status in the backend contract.
 */
const STATUS_META: Record<
  DoctorStatus,
  {
    Icon: typeof CheckCircle2
    label: string
    badgeVariant: BadgeVariant
    pillBg: string
    pillText: string
    pillIconClass: string
  }
> = {
  ok: {
    Icon: CheckCircle2,
    label: "OK",
    badgeVariant: "success",
    pillBg: "bg-emerald-50 border-emerald-200",
    pillText: "text-emerald-900",
    pillIconClass: "text-emerald-600",
  },
  warn: {
    Icon: AlertTriangle,
    label: "WARN",
    badgeVariant: "warning",
    pillBg: "bg-amber-50 border-amber-200",
    pillText: "text-amber-900",
    pillIconClass: "text-amber-600",
  },
  error: {
    Icon: XCircle,
    label: "ERROR",
    badgeVariant: "danger",
    pillBg: "bg-red-50 border-red-200",
    pillText: "text-red-900",
    pillIconClass: "text-red-600",
  },
  skip: {
    Icon: MinusCircle,
    label: "SKIP",
    badgeVariant: "muted",
    pillBg: "bg-zinc-50 border-zinc-200",
    pillText: "text-zinc-800",
    pillIconClass: "text-zinc-500",
  },
}

const STATUS_ORDER: DoctorStatus[] = ["ok", "warn", "error", "skip"]

function remediationAlertVariant(status: DoctorStatus): AlertVariant {
  // The `<Alert>` primitive only ships `default` and `destructive`. We use
  // destructive for error checks and default for everything else, and paint
  // the border/background via the row-scoped className so warn still reads
  // as "amber" without adding a new Alert variant.
  return status === "error" ? "destructive" : "default"
}

function remediationAccentClass(status: DoctorStatus): string {
  switch (status) {
    case "warn":
      return "border-amber-200 bg-amber-50 text-amber-900"
    case "ok":
      return "border-emerald-200 bg-emerald-50 text-emerald-900"
    case "skip":
      return "border-zinc-200 bg-zinc-50 text-zinc-700"
    case "error":
    default:
      // destructive variant supplies its own colors — no override needed.
      return ""
  }
}

/**
 * Compact KPI-style pill used in the summary strip. Clicking a pill toggles a
 * status filter on the checks table below. Selected pills get a ring so the
 * active filter is obvious at a glance.
 */
function StatusPill({
  status,
  count,
  active,
  onClick,
}: {
  status: DoctorStatus
  count: number
  active: boolean
  onClick: () => void
}) {
  const meta = STATUS_META[status]
  const Icon = meta.Icon
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      aria-label={`Filter by ${meta.label.toLowerCase()} (${count})`}
      className={cn(
        "flex items-center justify-between rounded-lg border px-3 py-2.5 text-left transition-all",
        meta.pillBg,
        meta.pillText,
        "hover:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
        active && "ring-2 ring-ring ring-offset-1",
      )}
    >
      <span className="flex items-center gap-2">
        <Icon className={cn("h-4 w-4 shrink-0", meta.pillIconClass)} />
        <span className="text-xs font-semibold uppercase tracking-wide">
          {meta.label}
        </span>
      </span>
      <span className="text-lg font-semibold tabular-nums">{count}</span>
    </button>
  )
}

/**
 * One check row. Collapsible when a remediation string is present; static
 * otherwise (no chevron, no click). This preserves keyboard/mouse affordance
 * only when there is actual detail to reveal.
 */
function CheckRow({
  check,
  expanded,
  onToggle,
}: {
  check: DoctorCheck
  expanded: boolean
  onToggle: () => void
}) {
  const meta = STATUS_META[check.status]
  const Icon = meta.Icon
  const hasRemediation = check.remediation !== null && check.remediation !== ""

  const inner = (
    <div className="flex w-full items-start gap-3 px-4 py-3">
      <Icon className={cn("h-5 w-5 shrink-0", meta.pillIconClass)} aria-hidden="true" />
      <span className="min-w-[10rem] shrink-0 font-mono text-xs text-foreground">
        {check.name}
      </span>
      <Badge variant={meta.badgeVariant} className="shrink-0">
        {meta.label}
      </Badge>
      <span className="flex-1 text-sm text-muted-foreground">{check.detail}</span>
      {hasRemediation ? (
        <ChevronDown
          className={cn(
            "h-4 w-4 shrink-0 text-muted-foreground transition-transform",
            expanded && "rotate-180",
          )}
          aria-hidden="true"
        />
      ) : (
        // Reserve the same slot so all rows align visually.
        <span aria-hidden="true" className="h-4 w-4 shrink-0" />
      )}
    </div>
  )

  return (
    <div className="border-t first:border-t-0">
      {hasRemediation ? (
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          aria-controls={`remediation-${check.name}`}
          className="w-full text-left transition-colors hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:outline-none"
        >
          {inner}
        </button>
      ) : (
        <div>{inner}</div>
      )}
      {hasRemediation && expanded ? (
        <div className="px-4 pb-3" id={`remediation-${check.name}`}>
          <Alert
            variant={remediationAlertVariant(check.status)}
            className={cn(
              "text-sm",
              remediationAccentClass(check.status),
            )}
          >
            <AlertTitle className="text-xs font-semibold uppercase tracking-wide">
              Remediation
            </AlertTitle>
            <AlertDescription>{check.remediation}</AlertDescription>
          </Alert>
        </div>
      ) : null}
    </div>
  )
}

export function DoctorPage() {
  const queryClient = useQueryClient()
  const { data, isLoading, isFetching, isError, error } = useDoctor()

  const [statusFilter, setStatusFilter] = useState<DoctorStatus | null>(null)
  const [nameFilter, setNameFilter] = useState("")
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())

  const toggleExpanded = (name: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  const filteredChecks = useMemo(() => {
    if (!data) return []
    const q = nameFilter.trim().toLowerCase()
    return data.checks.filter((c) => {
      if (statusFilter && c.status !== statusFilter) return false
      if (q && !c.name.toLowerCase().includes(q)) return false
      return true
    })
  }, [data, statusFilter, nameFilter])

  const handleRerun = () => {
    void queryClient.invalidateQueries({ queryKey: ["doctor"] })
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Doctor</h1>
          <p className="text-sm text-muted-foreground">
            {data
              ? `Preflight checks for ${data.project.id}`
              : isLoading
                ? "Loading…"
                : "Preflight checks"}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          onClick={handleRerun}
          disabled={isFetching}
          aria-label="Re-run doctor checks"
          className="gap-2"
        >
          <RefreshCw
            className={cn("h-4 w-4", isFetching && "animate-spin")}
            aria-hidden="true"
          />
          Re-run
        </Button>
      </header>

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load doctor report</AlertTitle>
          <AlertDescription>
            <p>{error?.message ?? "Unknown error"}</p>
            <p className="mt-1 text-xs opacity-80">
              Is the backend reachable? Check <code>/api/doctor</code> from your
              terminal.
            </p>
          </AlertDescription>
        </Alert>
      ) : null}

      {isLoading ? (
        <>
          <div className="grid grid-cols-4 gap-3">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-16" />
            ))}
          </div>
          <Skeleton className="h-10 w-full max-w-sm" />
          <Card>
            <div className="divide-y">
              {[0, 1, 2, 3, 4, 5].map((i) => (
                <div key={i} className="flex items-center gap-3 px-4 py-3">
                  <Skeleton className="h-5 w-5 rounded-full" />
                  <Skeleton className="h-4 w-40" />
                  <Skeleton className="h-4 w-14" />
                  <Skeleton className="h-4 flex-1" />
                </div>
              ))}
            </div>
          </Card>
        </>
      ) : null}

      {data ? (
        <>
          <div className="grid grid-cols-4 gap-3">
            {STATUS_ORDER.map((s) => (
              <StatusPill
                key={s}
                status={s}
                count={data.summary[s]}
                active={statusFilter === s}
                onClick={() =>
                  setStatusFilter((prev) => (prev === s ? null : s))
                }
              />
            ))}
          </div>

          <div className="flex items-center gap-2">
            <Input
              type="search"
              placeholder="Filter checks by name…"
              value={nameFilter}
              onChange={(e) => setNameFilter(e.target.value)}
              className="max-w-sm"
              aria-label="Filter checks by name"
            />
            {statusFilter ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setStatusFilter(null)}
              >
                Clear status filter
              </Button>
            ) : null}
          </div>

          <Card>
            {filteredChecks.length === 0 ? (
              <div className="px-4 py-10 text-center text-sm text-muted-foreground">
                No checks match this filter.
              </div>
            ) : (
              <div>
                {filteredChecks.map((c) => (
                  <CheckRow
                    key={c.name}
                    check={c}
                    expanded={expanded.has(c.name)}
                    onToggle={() => toggleExpanded(c.name)}
                  />
                ))}
              </div>
            )}
          </Card>
        </>
      ) : null}
    </div>
  )
}
