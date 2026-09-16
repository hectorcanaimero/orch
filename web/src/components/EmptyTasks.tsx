import { t } from "@/i18n"

/** What Tasks, Kanban and Graph say when the project has no tasks (or none match). */
export function EmptyTasks({ filtered = false }: { filtered?: boolean }) {
  return (
    <div className="rounded-xl border border-dashed p-10 text-center">
      <h2 className="text-base font-medium">{filtered ? t("tasks.none_match") : t("tasks.none")}</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {filtered ? (
          t("tasks.clear_filters")
        ) : (
          <>
            {t("tasks.seed_before")} <code className="rounded bg-muted px-1 py-0.5 text-xs">orch atomize</code>{" "}
            {t("tasks.seed_or")} <code className="rounded bg-muted px-1 py-0.5 text-xs">orch init</code>{" "}
            {t("tasks.seed_after")}
          </>
        )}
      </p>
    </div>
  )
}
