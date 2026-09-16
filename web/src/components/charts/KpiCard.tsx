import type { ReactNode } from "react"
import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

export interface KpiCardProps {
  label: ReactNode
  value: ReactNode
  icon?: LucideIcon
  /** Colors the icon; the number stays in the foreground so it reads in both themes. */
  iconClassName?: string
  hint?: ReactNode
  children?: ReactNode
}

/**
 * The one figure tile (Summary, Metrics). Flat: a border and the number, no
 * shadow and no uppercase kicker, so a row of them reads as one instrument.
 */
export function KpiCard({ label, value, icon: Icon, iconClassName, hint, children }: KpiCardProps) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5 rounded-xl border bg-card p-4">
      <div className="flex items-center justify-between gap-2 text-sm text-muted-foreground">
        <span className="truncate">{label}</span>
        {Icon ? <Icon className={cn("h-4 w-4 shrink-0", iconClassName)} aria-hidden /> : null}
      </div>
      <div className="text-2xl font-semibold leading-tight tracking-[-0.02em] tabular-nums">{value}</div>
      {hint ? <div className="text-xs text-muted-foreground">{hint}</div> : null}
      {children}
    </div>
  )
}
