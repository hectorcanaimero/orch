import type { ReactNode } from "react"
import { Info } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { glossary, type GlossaryTerm } from "@/i18n/glossary"
import { t } from "@/i18n"
import { cn } from "@/lib/utils"

/**
 * A term with its explanation one click away: what it is, how the number is
 * computed, and where the data comes from. Clicks inside never reach a parent
 * (a sortable table header, a clickable card).
 */
export function Explain({ term, children, className }: { term: GlossaryTerm; children?: ReactNode; className?: string }) {
  const entry = glossary[term]
  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      {children ?? entry.title}
      <Popover>
        <PopoverTrigger asChild>
          <button
            type="button"
            aria-label={t("explain.what_is", { term: entry.title })}
            onClick={(e) => e.stopPropagation()}
            className="inline-flex h-4 w-4 items-center justify-center rounded-full text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Info className="h-3 w-3" aria-hidden />
          </button>
        </PopoverTrigger>
        <PopoverContent onClick={(e) => e.stopPropagation()} className="normal-case tracking-normal">
          <p className="font-medium text-foreground">{entry.title}</p>
          <p className="mt-1 text-muted-foreground">{entry.definition}</p>
          <dl className="mt-3 space-y-2 border-t pt-3 text-xs">
            <div>
              <dt className="font-medium text-foreground">{t("explain.computed")}</dt>
              <dd className="mt-0.5 text-muted-foreground">{entry.computed}</dd>
            </div>
            <div>
              <dt className="font-medium text-foreground">{t("explain.source")}</dt>
              <dd className="mt-0.5 font-mono text-[11px] text-muted-foreground">{entry.source}</dd>
            </div>
          </dl>
        </PopoverContent>
      </Popover>
    </span>
  )
}
