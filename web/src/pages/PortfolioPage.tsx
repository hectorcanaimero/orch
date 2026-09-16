import { AlertTriangle, Clock, ExternalLink } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { isPortfolioDisabled, usePortfolio } from "@/hooks/usePortfolio"
import { useWhoami } from "@/hooks/useWhoami"
import type { PortfolioProject } from "@/lib/types"
import { fmt, t } from "@/i18n"
import { cn } from "@/lib/utils"

/**
 * G8.5 — several projects, one screen. Each row is deliberately compact
 * (name, progress, ETA/confidence, blockers, spend, last event, all on
 * one line at rest) so that five projects — the stated acceptance
 * criterion — fit without scrolling on a normal laptop screen; the
 * per-project detail this row summarizes already has its own full pages
 * (Sprint, Budget, Kanban) one click away at `/p/<project_id>/`.
 *
 * Operator-only and gated on the dashboard having been started with
 * `--portfolio` — AppLayout hides the nav entry for both reasons before
 * this page is ever reached, but a direct link (bookmark, typed URL)
 * still needs its own answer. `enabled: !isStakeholder` below is what
 * actually keeps that promise for a stakeholder who lands here directly
 * — without it, usePortfolio's own doc comment ("a stakeholder session
 * should never even issue the request") would be true only via
 * AppLayout, not here, which is exactly the gap opus's review of this
 * PR found.
 */
export function PortfolioPage() {
  const { data: whoami } = useWhoami()
  const isStakeholder = whoami?.profile === "stakeholder"
  const { data, isLoading, isError, error } = usePortfolio({ enabled: !isStakeholder })

  if (isLoading) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("portfolio.title")}</h1>
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-20 w-full" />
        ))}
      </div>
    )
  }

  if (isError) {
    if (isPortfolioDisabled(error)) {
      return (
        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("portfolio.off")}</AlertTitle>
          <AlertDescription>
            {t("portfolio.off_before")} <code className="font-mono">--portfolio</code>. {t("portfolio.off_after")}
          </AlertDescription>
        </Alert>
      )
    }
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>{t("portfolio.load_failed")}</AlertTitle>
        <AlertDescription>{error?.message ?? t("common.unknown_error")}</AlertDescription>
      </Alert>
    )
  }

  if (!data) return null

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("portfolio.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground tabular-nums">
          {t("portfolio.projects", { count: data.projects.length })}
          {data.unavailable.length > 0 ? ` · ${t("portfolio.unavailable", { count: data.unavailable.length })}` : ""}
        </p>
      </header>

      <div className="space-y-3">
        {data.projects.map((p) => (
          <ProjectRow key={p.project_id} project={p} />
        ))}
      </div>

      {data.unavailable.length > 0 ? (
        <details className="rounded-lg border bg-card">
          <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium text-muted-foreground">
            {t("portfolio.skipped", { count: data.unavailable.length })}
          </summary>
          <div className="space-y-1 border-t px-4 py-3 text-xs text-muted-foreground">
            {data.unavailable.map((u) => (
              <div key={u.root} className="flex items-center justify-between gap-4">
                <span className="truncate font-mono">{u.root}</span>
                <span className="shrink-0">{u.reason}</span>
              </div>
            ))}
          </div>
        </details>
      ) : null}
    </div>
  )
}

function ConfidenceDot({ confidence }: { confidence: PortfolioProject["confidence"] }) {
  if (confidence === "none") return null
  return (
    <span
      className={cn(
        "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
        confidence === "high" ? "bg-status-done" : "bg-status-blocked",
      )}
      title={confidence === "high" ? t("portfolio.high_confidence") : t("portfolio.low_confidence")}
    />
  )
}

function ProjectRow({ project }: { project: PortfolioProject }) {
  if (!project.available) {
    return (
      <Card className="border-dashed">
        <CardContent className="flex items-center justify-between gap-4 py-3">
          <div className="min-w-0">
            <div className="truncate font-medium text-muted-foreground">
              {project.project_name}
            </div>
            <div className="truncate font-mono text-xs text-muted-foreground">
              {project.root}
            </div>
          </div>
          <Badge variant="danger" className="shrink-0">
            {project.reason || t("portfolio.unavailable_one")}
          </Badge>
        </CardContent>
      </Card>
    )
  }

  const href = `/p/${encodeURIComponent(project.project_id)}/`
  const blockedLabel = project.blocked > 0 ? t("portfolio.blocked", { count: project.blocked }) : t("portfolio.no_blockers")

  return (
    <Card>
      <CardContent className="flex flex-wrap items-center gap-x-6 gap-y-2 py-3">
        <div className="min-w-[10rem] flex-1 basis-48">
          <a
            href={href}
            className="flex items-center gap-1.5 truncate font-medium hover:underline"
          >
            <span className="truncate">{project.project_name}</span>
            <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden />
          </a>
          <div className="mt-1 flex items-center gap-2">
            <Progress value={project.percent_done} className="h-1.5 w-32" />
            <span className="text-xs tabular-nums text-muted-foreground">
              {project.done}/{project.total} · {project.percent_done.toFixed(0)}%
            </span>
          </div>
        </div>

        <div className="flex items-center gap-1.5 text-xs">
          <Clock className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
          <ConfidenceDot confidence={project.confidence} />
          {project.eta_date ? (
            <span className="tabular-nums">{t("portfolio.eta", { date: fmt.day(project.eta_date) })}</span>
          ) : (
            <span className="text-muted-foreground">{t("portfolio.no_eta")}</span>
          )}
        </div>

        <Badge variant={project.blocked > 0 ? "warning" : "muted"} className="shrink-0">
          {blockedLabel}
        </Badge>

        <div className="text-xs tabular-nums text-muted-foreground">
          {project.spend.available && project.spend.total_cost_usd !== undefined
            ? fmt.usd(project.spend.total_cost_usd)
            : t("portfolio.spend_hidden")}
        </div>

        {project.blockers[0] ? (
          <div className="min-w-[12rem] flex-1 basis-56 truncate text-xs text-muted-foreground">
            <span className="font-medium text-foreground">
              {project.blockers[0].title}
            </span>
            {": "}
            {project.blockers[0].reason}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}
