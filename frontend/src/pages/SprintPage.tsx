import {
  AlertTriangle,
  CalendarClock,
  CheckCircle2,
  Clock,
  TrendingUp,
  Zap,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useSprintHealth } from "@/hooks/useSprintHealth"
import type { SprintBlocker } from "@/lib/types"
import { cn } from "@/lib/utils"

function ConfidenceBadge({ confidence }: { confidence: "high" | "low" | "none" }) {
  if (confidence === "none") return null
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium",
        confidence === "high"
          ? "bg-emerald-100 text-emerald-700"
          : "bg-amber-100 text-amber-700",
      )}
    >
      {confidence === "high" ? "alta confianza" : "baja confianza"}
    </span>
  )
}

function EtaCard({
  etaDate,
  etaDays,
  confidence,
  velocity,
  remaining,
  done,
}: {
  etaDate: string | null
  etaDays: number | null
  confidence: "high" | "low" | "none"
  velocity: number
  remaining: number
  done: number
}) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-base">
          <CalendarClock className="h-4 w-4 text-violet-500" />
          ETA del sprint
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {etaDate ? (
          <div className="space-y-1">
            <div className="flex items-baseline gap-3">
              <span className="text-3xl font-bold tracking-tight text-zinc-900">
                {new Date(etaDate + "T12:00:00Z").toLocaleDateString("es-AR", {
                  day: "numeric",
                  month: "long",
                })}
              </span>
              <ConfidenceBadge confidence={confidence} />
            </div>
            <p className="text-sm text-muted-foreground">
              {etaDays != null && `en ~${etaDays} días`}
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            Sin datos suficientes para proyectar una fecha — completá algunas tareas primero.
          </p>
        )}

        <div className="grid grid-cols-3 gap-3 border-t pt-3">
          <div className="text-center">
            <p className="text-lg font-semibold text-zinc-900">{done}</p>
            <p className="text-xs text-muted-foreground">completadas</p>
          </div>
          <div className="text-center">
            <p className="text-lg font-semibold text-zinc-900">{remaining}</p>
            <p className="text-xs text-muted-foreground">restantes</p>
          </div>
          <div className="text-center">
            <p className="text-lg font-semibold text-zinc-900">
              {velocity > 0 ? velocity.toFixed(1) : "—"}
            </p>
            <p className="text-xs text-muted-foreground">tasks/día</p>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

function BlockerCard({ blocker }: { blocker: SprintBlocker }) {
  const blockedDate = blocker.blocked_at
    ? new Date(blocker.blocked_at).toLocaleDateString("es-AR", {
        day: "numeric",
        month: "short",
      })
    : null

  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-rose-100 bg-rose-50/50 p-3">
      <div className="flex items-start justify-between gap-2">
        <div className="flex flex-col gap-0.5">
          <span className="text-sm font-medium text-zinc-800">{blocker.title}</span>
          <span className="font-mono text-[10px] text-zinc-400">{blocker.task_id}</span>
        </div>
        <Badge variant="outline" className="shrink-0 text-[10px]">
          Fase {blocker.phase}
        </Badge>
      </div>
      <p className="text-xs text-rose-700 line-clamp-2">{blocker.reason}</p>
      {blockedDate && (
        <p className="text-[10px] text-muted-foreground">Bloqueado el {blockedDate}</p>
      )}
    </div>
  )
}

export function SprintPage() {
  const { data, isLoading, isError, error } = useSprintHealth()

  if (isLoading) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Sprint</h1>
        <Skeleton className="h-44 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Error al cargar sprint health</AlertTitle>
        <AlertDescription>{(error as Error)?.message}</AlertDescription>
      </Alert>
    )
  }

  if (!data?.available) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Sprint</h1>
        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Requiere backend SQLite</AlertTitle>
          <AlertDescription>
            Activá SQLite con <code className="font-mono">orch migrate</code> para ver el sprint health.
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Sprint</h1>
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Clock className="h-3.5 w-3.5" />
          Velocidad calculada sobre los últimos 7 días
        </div>
      </div>

      {/* KPI row */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardContent className="pt-5">
            <div className="flex items-center gap-3">
              <CheckCircle2 className="h-8 w-8 text-emerald-500" />
              <div>
                <p className="text-2xl font-bold">{data.done_count}</p>
                <p className="text-xs text-muted-foreground">completadas</p>
              </div>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-5">
            <div className="flex items-center gap-3">
              <TrendingUp className="h-8 w-8 text-violet-500" />
              <div>
                <p className="text-2xl font-bold">
                  {data.velocity_per_day > 0 ? data.velocity_per_day.toFixed(1) : "—"}
                </p>
                <p className="text-xs text-muted-foreground">tasks/día</p>
              </div>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-5">
            <div className="flex items-center gap-3">
              <Clock className="h-8 w-8 text-sky-500" />
              <div>
                <p className="text-2xl font-bold">{data.remaining_hours}h</p>
                <p className="text-xs text-muted-foreground">horas restantes</p>
              </div>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-5">
            <div className="flex items-center gap-3">
              <Zap
                className={cn(
                  "h-8 w-8",
                  data.blocked_count > 0 ? "text-rose-500" : "text-zinc-300",
                )}
              />
              <div>
                <p className="text-2xl font-bold">{data.blocked_count}</p>
                <p className="text-xs text-muted-foreground">bloqueadas</p>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* ETA card */}
      <EtaCard
        etaDate={data.eta_date}
        etaDays={data.eta_days}
        confidence={data.confidence}
        velocity={data.velocity_per_day}
        remaining={data.remaining_tasks}
        done={data.done_count}
      />

      {/* Blockers */}
      <div className="space-y-3">
        <h2 className="text-lg font-semibold tracking-tight">
          Bloqueadas
          {data.blocked_count > 0 && (
            <span className="ml-2 text-sm font-normal text-rose-500">
              {data.blocked_count} task{data.blocked_count !== 1 ? "s" : ""}
            </span>
          )}
        </h2>

        {data.blockers.length === 0 ? (
          <Card>
            <CardContent className="py-6 text-center text-sm text-muted-foreground">
              Sin tareas bloqueadas — todo en orden.
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            {data.blockers.map((b) => (
              <BlockerCard key={b.task_id} blocker={b} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
