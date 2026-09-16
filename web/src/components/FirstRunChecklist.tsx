import { Check, Circle, RefreshCw } from "lucide-react"
import { Link } from "react-router-dom"
import { CopyCommand } from "@/components/CopyCommand"
import { Button } from "@/components/ui/button"
import type { Onboarding } from "@/hooks/useReceipt"
import { t } from "@/i18n"
import { cn } from "@/lib/utils"

/** Before a project's first finished run: what is set up, what is not, and the command that fixes it. */
export function FirstRunChecklist({
  onboarding,
  onRecheck,
  checking,
}: {
  onboarding: Onboarding
  onRecheck: () => void
  checking: boolean
}) {
  const required = onboarding.items.filter((i) => !i.optional)
  const doneCount = required.filter((i) => i.done).length

  return (
    <section aria-labelledby="now-first-run">
      <div className="mb-3 flex items-baseline justify-between gap-3">
        <h2 id="now-first-run" className="text-sm font-semibold tracking-[-0.01em]">
          {t("onboarding.title")}
        </h2>
        <span className="text-xs text-muted-foreground tabular-nums">
          {t("onboarding.progress", { done: doneCount, total: required.length })}
        </span>
      </div>
      <div className="overflow-hidden rounded-xl border bg-card">
        <div className="flex gap-1 px-4 pt-4" aria-hidden>
          {required.map((i) => (
            <span key={i.id} className={cn("h-1.5 flex-1 rounded-full", i.done ? "bg-status-done" : "bg-muted")} />
          ))}
        </div>
        <ul className="divide-y">
          {onboarding.items.map((item) => (
            <li key={item.id} className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-3 px-4 py-3">
              {item.done ? (
                <Check className="mt-0.5 h-4 w-4 text-status-done" aria-label={t("onboarding.done")} />
              ) : (
                <Circle className="mt-0.5 h-4 w-4 text-muted-foreground" aria-label={t("onboarding.todo")} />
              )}
              <div className="min-w-0">
                <p className={cn("text-sm", item.done ? "text-muted-foreground" : "font-medium")}>
                  {t(`onboarding.${item.id}`)}
                  {item.optional ? <span className="ml-2 text-xs font-normal text-muted-foreground">{t("onboarding.optional")}</span> : null}
                </p>
                {!item.done ? (
                  <>
                    <p className="mt-0.5 text-xs text-muted-foreground">{item.detail || t(`onboarding.${item.id}.hint`)}</p>
                    {item.command ? <CopyCommand command={item.command} className="mt-2" /> : null}
                    {item.link ? (
                      <Link to={item.link} className="mt-2 inline-block text-xs underline underline-offset-4 hover:text-foreground">
                        {t("onboarding.open_page")}
                      </Link>
                    ) : null}
                  </>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
        <div className="flex justify-end border-t px-4 py-3">
          <Button type="button" size="sm" variant="outline" onClick={onRecheck} disabled={checking}>
            <RefreshCw className={cn("h-4 w-4", checking && "animate-spin motion-reduce:animate-none")} aria-hidden />
            {t("onboarding.recheck")}
          </Button>
        </div>
      </div>
    </section>
  )
}
