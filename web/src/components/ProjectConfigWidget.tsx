import { ChevronRight } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useProjectConfig } from "@/hooks/useProjectConfig"
import { fmt, t } from "@/i18n"
import type { ProjectConfig } from "@/lib/types"

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-mono text-xs tabular-nums">{value}</span>
    </div>
  )
}

function Section({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="space-y-1">
      <h4 className="text-xs font-medium text-muted-foreground">
        {title}
      </h4>
      <div className="divide-y divide-border/50">{children}</div>
    </section>
  )
}

function isEmpty(config: ProjectConfig): boolean {
  return Object.keys(config).length === 0
}

function ConfigBody({ config }: { config: ProjectConfig }) {
  if (isEmpty(config)) {
    return (
      <p className="py-2 text-sm text-muted-foreground">
        {t("config.none")}
      </p>
    )
  }

  const {
    concurrency,
    budget,
    state,
    retry,
    findings,
    spec_root,
    strict_files_phases,
    default_timeout_multiplier,
  } = config

  return (
    <div className="grid gap-6 md:grid-cols-2">
      {concurrency ? (
        <Section title={t("config.concurrency")}>
          {concurrency.global_max != null ? (
            <Row label={t("config.global_max")} value={concurrency.global_max} />
          ) : null}
          {concurrency.per_file != null ? (
            <Row label={t("config.per_file")} value={concurrency.per_file} />
          ) : null}
          {concurrency.per_provider
            ? Object.entries(concurrency.per_provider).map(([provider, n]) => (
                <Row key={provider} label={t("config.per_provider", { provider })} value={n} />
              ))
            : null}
        </Section>
      ) : null}

      {budget ? (
        <Section title={t("config.budget")}>
          {budget.per_dispatch_usd != null ? (
            <Row
              label={t("config.per_dispatch")}
              value={fmt.usd(budget.per_dispatch_usd)}
            />
          ) : null}
        </Section>
      ) : null}

      {state ? (
        <Section title={t("config.state")}>
          {state.backend ? <Row label={t("config.backend")} value={state.backend} /> : null}
          {state.sqlite_path ? (
            <Row label={t("config.sqlite_path")} value={state.sqlite_path} />
          ) : null}
          {state.tasks_json_precedence ? (
            <Row
              label={t("config.precedence")}
              value={state.tasks_json_precedence}
            />
          ) : null}
        </Section>
      ) : null}

      {retry ? (
        <Section title={t("config.retry")}>
          {retry.backoff_seconds != null ? (
            <Row label={t("config.backoff")} value={`${retry.backoff_seconds}s`} />
          ) : null}
          {retry.rate_limit_backoff_seconds != null ? (
            <Row
              label={t("config.rate_limit_backoff")}
              value={`${retry.rate_limit_backoff_seconds}s`}
            />
          ) : null}
        </Section>
      ) : null}

      {findings ? (
        <Section title={t("config.findings")}>
          {findings.publish_repo ? (
            <Row label={t("config.publish_repo")} value={findings.publish_repo} />
          ) : null}
          {findings.publish_rate_limit_per_hour != null ? (
            <Row
              label={t("config.rate_limit_hour")}
              value={findings.publish_rate_limit_per_hour}
            />
          ) : null}
          {findings.label ? <Row label={t("config.label")} value={findings.label} /> : null}
          {findings.min_publish_confidence ? (
            <Row
              label={t("config.min_confidence")}
              value={findings.min_publish_confidence}
            />
          ) : null}
        </Section>
      ) : null}

      {spec_root ||
      default_timeout_multiplier != null ||
      (strict_files_phases && strict_files_phases.length > 0) ? (
        <Section title={t("config.other")}>
          {spec_root ? <Row label={t("config.spec_root")} value={spec_root} /> : null}
          {default_timeout_multiplier != null ? (
            <Row
              label={t("config.timeout_multiplier")}
              value={`×${default_timeout_multiplier}`}
            />
          ) : null}
          {strict_files_phases && strict_files_phases.length > 0 ? (
            <Row
              label={t("config.strict_files")}
              value={strict_files_phases.join(", ")}
            />
          ) : null}
        </Section>
      ) : null}
    </div>
  )
}

export function ProjectConfigWidget() {
  const { data, isLoading, isError, error } = useProjectConfig()

  return (
    <Card>
      <CardContent className="p-0">
        <details className="group">
          <summary className="flex cursor-pointer list-none items-center gap-2 rounded-xl px-5 py-4 text-sm font-medium hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <ChevronRight className="h-4 w-4 text-muted-foreground transition-transform group-open:rotate-90" />
            <span>{t("config.title")}</span>
          </summary>
          <div className="border-t px-5 py-4">
            {isLoading ? (
              <div className="space-y-2">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-4 w-64" />
                <Skeleton className="h-4 w-52" />
              </div>
            ) : isError ? (
              <p className="text-sm text-muted-foreground">
                {t("config.load_failed")}
                {error?.message ? `: ${error.message}` : ""}
              </p>
            ) : data ? (
              <ConfigBody config={data} />
            ) : null}
          </div>
        </details>
      </CardContent>
    </Card>
  )
}
