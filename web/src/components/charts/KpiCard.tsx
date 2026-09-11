import type { LucideIcon } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"

export interface KpiCardProps {
  label: string
  value: string | number
  icon?: LucideIcon
  hint?: string
}

/**
 * Compact stat card for the Metrics page. Consistent with the shadcn-flavored
 * Card primitive already used by other pages — label is uppercase muted small
 * text, value is `text-2xl font-semibold`, optional `hint` sits below the
 * value as muted small text. `icon` renders in the top-right corner.
 */
export function KpiCard({ label, value, icon: Icon, hint }: KpiCardProps) {
  return (
    <Card>
      <CardContent className="flex flex-col gap-1 p-4 pt-4">
        <div className="flex items-start justify-between gap-2">
          <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {label}
          </span>
          {Icon ? (
            <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
          ) : null}
        </div>
        <div className="text-2xl font-semibold leading-tight">{value}</div>
        {hint ? (
          <div className="text-xs text-muted-foreground">{hint}</div>
        ) : null}
      </CardContent>
    </Card>
  )
}
