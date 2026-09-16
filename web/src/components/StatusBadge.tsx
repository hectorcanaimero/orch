import { t } from "@/i18n"
import { statusMeta } from "@/lib/status"
import { cn } from "@/lib/utils"

/** A task status as shape + color + word, from the shared STATUS scale. */
export function StatusBadge({ status, className }: { status: string; className?: string }) {
  const meta = statusMeta(status)
  const Icon = meta.icon
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium",
        meta.soft,
        meta.text,
        className,
      )}
    >
      <Icon className="h-3 w-3 shrink-0" aria-hidden />
      {t(meta.label)}
    </span>
  )
}
