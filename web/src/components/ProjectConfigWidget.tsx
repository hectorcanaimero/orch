import { ChevronRight } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useProjectConfig } from "@/hooks/useProjectConfig"
import type { ProjectConfig } from "@/lib/types"

const usdFormatter = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono text-xs">{value}</span>
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
      <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
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
        No configuration found.
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
        <Section title="Concurrency">
          {concurrency.global_max != null ? (
            <Row label="Global max" value={concurrency.global_max} />
          ) : null}
          {concurrency.per_file != null ? (
            <Row label="Per file" value={concurrency.per_file} />
          ) : null}
          {concurrency.per_provider
            ? Object.entries(concurrency.per_provider).map(([provider, n]) => (
                <Row key={provider} label={`Per provider · ${provider}`} value={n} />
              ))
            : null}
        </Section>
      ) : null}

      {budget ? (
        <Section title="Budget">
          {budget.per_dispatch_usd != null ? (
            <Row
              label="Per dispatch"
              value={usdFormatter.format(budget.per_dispatch_usd)}
            />
          ) : null}
        </Section>
      ) : null}

      {state ? (
        <Section title="State">
          {state.backend ? <Row label="Backend" value={state.backend} /> : null}
          {state.sqlite_path ? (
            <Row label="SQLite path" value={state.sqlite_path} />
          ) : null}
          {state.tasks_json_precedence ? (
            <Row
              label="tasks.json precedence"
              value={state.tasks_json_precedence}
            />
          ) : null}
        </Section>
      ) : null}

      {retry ? (
        <Section title="Retry">
          {retry.backoff_seconds != null ? (
            <Row label="Backoff" value={`${retry.backoff_seconds}s`} />
          ) : null}
          {retry.rate_limit_backoff_seconds != null ? (
            <Row
              label="Rate limit backoff"
              value={`${retry.rate_limit_backoff_seconds}s`}
            />
          ) : null}
        </Section>
      ) : null}

      {findings ? (
        <Section title="Findings">
          {findings.publish_repo ? (
            <Row label="Publish repo" value={findings.publish_repo} />
          ) : null}
          {findings.publish_rate_limit_per_hour != null ? (
            <Row
              label="Rate limit / hour"
              value={findings.publish_rate_limit_per_hour}
            />
          ) : null}
          {findings.label ? <Row label="Label" value={findings.label} /> : null}
          {findings.min_publish_confidence ? (
            <Row
              label="Min confidence"
              value={findings.min_publish_confidence}
            />
          ) : null}
        </Section>
      ) : null}

      {spec_root ||
      default_timeout_multiplier != null ||
      (strict_files_phases && strict_files_phases.length > 0) ? (
        <Section title="Other">
          {spec_root ? <Row label="Spec root" value={spec_root} /> : null}
          {default_timeout_multiplier != null ? (
            <Row
              label="Default timeout multiplier"
              value={`×${default_timeout_multiplier}`}
            />
          ) : null}
          {strict_files_phases && strict_files_phases.length > 0 ? (
            <Row
              label="Strict files phases"
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
          <summary className="flex cursor-pointer list-none items-center gap-2 p-6 text-sm font-medium">
            <ChevronRight className="h-4 w-4 text-muted-foreground transition-transform group-open:rotate-90" />
            <span>Project configuration</span>
          </summary>
          <div className="border-t px-6 py-4">
            {isLoading ? (
              <div className="space-y-2">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-4 w-64" />
                <Skeleton className="h-4 w-52" />
              </div>
            ) : isError ? (
              <p className="text-sm text-muted-foreground">
                Failed to load configuration
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
