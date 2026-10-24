import { useState, type ReactNode } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, ArrowLeft, ChevronDown, ChevronUp, FileText, GitMerge, GitPullRequest, PanelRightClose, PanelRightOpen } from "lucide-react"
import { Link, useLocation, useNavigate, useParams } from "react-router-dom"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ActivityFeed } from "@/components/ActivityFeed"
import { Explain } from "@/components/Explain"
import { Markdown } from "@/components/Markdown"
import { StatusRing } from "@/components/StatusRing"
import { TaskChanges } from "@/components/TaskChanges"
import { TaskSteer } from "@/components/TaskSteer"
import { useTicker } from "@/hooks/useNow"
import { useEventsHistory } from "@/hooks/useEventsHistory"
import { useTaskDetail } from "@/hooks/useTaskDetail"
import { useTasks } from "@/hooks/useTasks"
import { useWhoami } from "@/hooks/useWhoami"
import { fmt, t, type MessageKey } from "@/i18n"
import { editTask, moveTask, type TaskFields } from "@/lib/api"
import { taskPath } from "@/lib/nav"
import { STATUS, statusKey, statusMeta } from "@/lib/status"
import { formatDuration, formatRelative } from "@/lib/time"
import { cn } from "@/lib/utils"
import type { FormattedEvent, Task } from "@/lib/types"

interface TaskComment {
  at: string
  author: string
  body: string
  kind?: string
}

/** A comment as the API sends it, or null for an entry that is not one. */
function asComment(raw: unknown): TaskComment | null {
  if (!raw || typeof raw !== "object") return null
  const c = raw as Record<string, unknown>
  if (typeof c.body !== "string" || c.body.trim() === "") return null
  return {
    at: typeof c.at === "string" ? c.at : "",
    author: typeof c.author === "string" && c.author ? c.author : "—",
    body: c.body,
    kind: typeof c.kind === "string" ? c.kind : undefined,
  }
}

type ActivityTab = "all" | "comments" | "agent"

const ACTIVITY_TABS: { id: ActivityTab; label: MessageKey }[] = [
  { id: "all", label: "task.activity_all" },
  { id: "comments", label: "task.activity_comments" },
  { id: "agent", label: "task.activity_agent" },
]

// Events that are an agent's own outcome, shown under its backend's name.
const AGENT_EVENTS = new Set(["success", "fail", "timeout"])
const DOT: Record<string, string> = {
  dispatch: "bg-muted-foreground",
  success: "bg-status-done",
  pr_merged: "bg-status-done",
  pr_auto_merged: "bg-status-done",
  ci_success: "bg-status-done",
  fail: "bg-status-failed",
  timeout: "bg-status-failed",
  verify_failed: "bg-status-blocked",
  verify_passed: "bg-status-done",
  block: "bg-status-blocked",
  ci_blocked: "bg-status-blocked",
  ci_failure_retry: "bg-status-blocked",
  ci_conflict_retry: "bg-status-blocked",
  budget_pause: "bg-status-blocked",
}

interface TimelineItem {
  at: string
  who: string
  verb: string
  body?: string
  dot: string
  muted: boolean
  operator: boolean
  comment: boolean
}

/** What an event says, in words; the type itself for one this page does not know. */
function eventItem(e: FormattedEvent): TimelineItem {
  const extra = (e.raw?.extra ?? {}) as Record<string, unknown>
  const str = (v: unknown) => (typeof v === "string" && v.trim() ? v : undefined)
  const key = `task.ev.${e.event_type}` as MessageKey
  const known = t(key) !== key
  const who = AGENT_EVENTS.has(e.event_type)
    ? e.backend || "agent"
    : e.event_type === "verify_failed" || e.event_type === "verify_passed"
      ? "verify"
      : e.event_type.startsWith("ci_") || e.event_type.startsWith("pr_")
        ? "CI"
        : e.event_type === "operator_stop" || e.event_type === "task_edited" || e.event_type === "task_added"
          ? t("task.ev.operator")
          : "orch"
  return {
    at: e.ts,
    who,
    verb: known
      ? t(key, { attempt: String(extra.attempt ?? ""), model: String(extra.cli_model ?? e.backend ?? "") })
      : e.event_type.replaceAll("_", " "),
    body: str(extra.reason) ?? str(extra.error) ?? str(extra.detail) ?? str(extra.note),
    dot: DOT[e.event_type] ?? "bg-status-running",
    muted: who === "orch",
    operator: false,
    comment: false,
  }
}

function commentItem(c: TaskComment): TimelineItem {
  const operator = c.kind === "operator_note"
  return {
    at: c.at,
    who: c.author,
    verb: operator ? t("task.ev.note") : t("task.ev.commented"),
    body: c.body,
    dot: operator ? "bg-brand" : c.author === "orch" ? "bg-muted-foreground" : "bg-status-running",
    muted: c.author === "orch",
    operator,
    comment: true,
  }
}

function Timeline({ items, label }: { items: TimelineItem[]; label: string }) {
  const now = useTicker(60_000)
  return (
    <ol aria-label={label} className="flex flex-col">
      {items.map((x, i) => (
        <li key={`${x.at}-${i}`} data-muted={x.muted ? "true" : undefined} className="grid grid-cols-[24px_minmax(0,1fr)] gap-x-3 pb-3.5">
          <span className="flex justify-center pt-1" aria-hidden>
            <span className={cn("h-2 w-2 rounded-full", x.dot)} />
          </span>
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-baseline gap-x-1.5">
              <span className={cn("font-medium", x.muted && "text-muted-foreground")}>{x.who}</span>
              <span className="text-muted-foreground">{x.verb}</span>
              {x.operator ? <span className="rounded border px-1 text-[10px] text-muted-foreground">{t("task.operator_note")}</span> : null}
              {x.at ? (
                <time dateTime={x.at} title={fmt.dateTime(x.at)} className="text-xs text-muted-foreground">
                  · {formatRelative(x.at, now)}
                </time>
              ) : null}
            </div>
            {x.body ? (
              <p
                className={cn(
                  "whitespace-pre-wrap break-words leading-5",
                  x.comment && !x.muted
                    ? "rounded-lg border bg-card px-2.5 py-2 text-foreground/90"
                    : "rounded-md border bg-sidebar px-2.5 py-1.5 font-mono text-xs text-muted-foreground",
                )}
              >
                {x.body}
              </p>
            ) : null}
          </div>
        </li>
      ))}
    </ol>
  )
}

/**
 * The task's history as one timeline, as the design draws it: orch's events
 * for this task (dispatches with their attempt and model, outcomes, verify,
 * CI and PRs, stops and edits) interleaved with the comments by time. The
 * Agent tab is the latest dispatch's own log.
 */
function Activity({ task, events }: { task: Task; events: FormattedEvent[] }) {
  const [tab, setTab] = useState<ActivityTab>("all")
  const live = statusKey(task.status) === "in_progress"
  const comments = task.comments.map(asComment).filter((c): c is TaskComment => c !== null)
  // orch's own comments restate an event the timeline already shows; with events, they go.
  const timeline = [...events.map(eventItem), ...comments.filter((c) => events.length === 0 || c.author !== "orch").map(commentItem)].sort(
    (a, b) => (Date.parse(a.at) || Infinity) - (Date.parse(b.at) || Infinity),
  )
  const people = comments.filter((c) => c.author !== "orch").map(commentItem)
  const shown = tab === "comments" ? people : timeline
  return (
    <section aria-labelledby="task-activity" className="space-y-3.5">
      <div className="flex flex-wrap items-center gap-2.5 border-b pb-2.5">
        <h2 id="task-activity" className="text-sm font-semibold">
          {t("activity.title")}
        </h2>
        <div role="tablist" aria-label={t("task.activity_filter")} className="flex gap-0.5">
          {ACTIVITY_TABS.map((x) => (
            <button
              key={x.id}
              type="button"
              role="tab"
              aria-selected={tab === x.id}
              onClick={() => setTab(x.id)}
              className={cn(
                "h-[26px] rounded-md border px-2 text-xs",
                tab === x.id ? "border-input bg-secondary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {t(x.label)}
            </button>
          ))}
        </div>
        {live ? (
          <span className="ml-auto inline-flex items-center gap-1.5 text-xs text-status-done">
            <span className="h-1.5 w-1.5 rounded-full bg-status-done" aria-hidden />
            {t("task.live")}
          </span>
        ) : null}
      </div>
      {tab !== "agent" ? (
        shown.length > 0 ? (
          <Timeline items={shown} label={t("task.comments", { count: task.comments.length })} />
        ) : (
          <p className="text-muted-foreground">{tab === "comments" ? t("task.no_comments") : t("task.no_activity")}</p>
        )
      ) : null}
      {tab === "all" ? (
        <button type="button" onClick={() => setTab("agent")} className="h-7 rounded-md border px-2.5 text-xs text-foreground/90 hover:bg-secondary">
          {t("task.full_log")}
        </button>
      ) : null}
      {tab === "agent" ? (
        // The model's route names its provider: "claude/sonnet".
        <ActivityFeed key={task.id} taskId={task.id} live={live} provider={task.model.split("/")[0]} className="rounded-md border bg-sidebar p-2.5" full wrap />
      ) : null}
    </section>
  )
}

/** One label/value row of the properties list. */
function Prop({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  )
}

const fieldClass =
  "h-[30px] w-full min-w-0 rounded-md border border-transparent bg-transparent px-2 text-[13px] hover:border-input focus:border-input focus:outline-none disabled:cursor-default disabled:hover:border-transparent"

/** A property edited in place: saved on blur or Enter when it changed, Escape undoes. */
function InlineField({
  label,
  value,
  type = "text",
  list,
  disabled,
  onSave,
}: {
  label: string
  value: string
  type?: "text" | "number"
  list?: string
  disabled: boolean
  onSave: (value: string) => void
}) {
  const [draft, setDraft] = useState(value)
  const [shown, setShown] = useState(value)
  // A refetch after a save replaces what the field shows.
  if (value !== shown) {
    setShown(value)
    setDraft(value)
  }
  return (
    <input
      aria-label={label}
      type={type}
      list={list}
      min={type === "number" ? 0 : undefined}
      step={type === "number" ? "any" : undefined}
      value={draft}
      disabled={disabled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => {
        if (draft.trim() !== value) onSave(draft.trim())
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur()
        if (e.key === "Escape") setDraft(value)
      }}
      className={cn(fieldClass, type === "number" && "tabular-nums", list && "font-mono text-xs")}
    />
  )
}

// What a task can be moved to from here; in progress is the run's to set.
const MOVABLE = ["backlog", "todo", "blocked", "done"] as const

function TaskRef({ task, id }: { task?: Task; id: string }) {
  return (
    <Link to={taskPath(id)} className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1 hover:bg-secondary">
      <StatusRing status={task?.status ?? "backlog"} />
      <span className="shrink-0 font-mono text-xs text-muted-foreground">{id}</span>
      <span className="truncate">{task?.title ?? ""}</span>
    </Link>
  )
}

function Properties({
  task,
  all,
  canEdit,
  save,
  move,
  attempt,
  runningSince,
  onFold,
}: {
  task: Task
  all: Task[]
  canEdit: boolean
  save: (fields: TaskFields) => void
  move: (status: string) => void
  /** The latest dispatch's attempt, from the task's events. */
  attempt?: number
  /** When the latest dispatch started, for the running card's clock. */
  runningSince?: string
  onFold: () => void
}) {
  const now = useTicker(60_000)
  const [adding, setAdding] = useState(false)
  const byId = new Map(all.map((x) => [x.id, x]))
  const blocks = all.filter((x) => x.dependencies.includes(task.id))
  // ponytail: the models other tasks already route to; an endpoint of model_router.yaml's routes if that proves too narrow.
  const models = [...new Set(all.map((x) => x.model).filter(Boolean))].sort()
  const key = statusKey(task.status)
  const running = key === "in_progress"

  return (
    <aside aria-label={t("task.properties")} className="flex min-w-0 flex-col gap-5 border-t px-4 py-5 lg:w-[330px] lg:shrink-0 lg:border-l lg:border-t-0">
      <div className="-mb-2 flex items-center justify-between">
        <h2 className="text-xs font-medium text-muted-foreground">{t("task.properties")}</h2>
        <button
          type="button"
          onClick={onFold}
          aria-label={t("task.hide_properties")}
          aria-expanded
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <PanelRightClose className="h-4 w-4" aria-hidden />
        </button>
      </div>
      <dl className="grid grid-cols-[96px_minmax(0,1fr)] items-center gap-x-2 gap-y-1">
        <Prop label={t("tasks.col.status")}>
          <span className="flex items-center gap-1 pl-2">
            <StatusRing status={task.status} />
            <select
              aria-label={t("tasks.col.status")}
              value={key}
              disabled={!canEdit || running}
              onChange={(e) => move(e.target.value)}
              className="h-[30px] min-w-0 flex-1 cursor-pointer rounded-md bg-transparent px-1 text-[13px] focus:outline-none disabled:cursor-default"
            >
              {running || key === "failed" ? <option value={key}>{t(STATUS[key].label)}</option> : null}
              {MOVABLE.map((s) => (
                <option key={s} value={s}>
                  {t(STATUS[s].label)}
                </option>
              ))}
            </select>
          </span>
        </Prop>
        {attempt ? (
          <Prop label={t("task.attempt")}>
            <span className="pl-2 tabular-nums">
              {task.max_attempts ? t("task.attempt_of", { attempt, max: task.max_attempts }) : attempt}
            </span>
          </Prop>
        ) : null}
        <Prop label={t("tasks.col.model")}>
          <InlineField label={t("tasks.col.model")} value={task.model} list="task-models" disabled={!canEdit} onSave={(model) => save({ model })} />
          <datalist id="task-models">
            {models.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
        </Prop>
        <Prop label={t("tasks.col.phase")}>
          <InlineField label={t("tasks.col.phase")} type="number" value={String(task.phase)} disabled={!canEdit} onSave={(v) => save({ phase: Number(v) })} />
        </Prop>
        <Prop label={<Explain term="estimate" />}>
          <InlineField
            label={t("tasks.col.estimate")}
            type="number"
            value={String(task.estimate_hours)}
            disabled={!canEdit}
            onSave={(v) => save({ estimate_hours: Number(v) })}
          />
        </Prop>
        <Prop label={<Explain term="agent_time" />}>
          <span className="block px-2 tabular-nums">{t("task.hours", { hours: task.human_hours })}</span>
        </Prop>
        <Prop label={t("task.critical")}>
          <span className={cn("block px-2", task.on_critical_path ? "text-brand" : "text-muted-foreground")}>
            {task.on_critical_path ? t("task.critical_yes", { count: task.downstream_impact }) : t("common.no")}
          </span>
        </Prop>
      </dl>

      <section aria-labelledby="task-deps" className="flex flex-col gap-1.5 border-t pt-4">
        <h3 id="task-deps" className="text-xs font-medium text-muted-foreground">
          {t("task.depends_on")}
        </h3>
        {task.dependencies.map((d) => (
          <TaskRef key={d} id={d} task={byId.get(d)} />
        ))}
        {canEdit && adding ? (
          <InlineField
            label={t("task.dependency_id")}
            value=""
            list="task-ids"
            disabled={false}
            onSave={(id) => {
              setAdding(false)
              if (id) save({ dependencies: [...task.dependencies, id] })
            }}
          />
        ) : null}
        {canEdit && !adding ? (
          <button type="button" onClick={() => setAdding(true)} className="self-start px-2 py-0.5 text-xs text-muted-foreground hover:text-foreground">
            {t("task.add_dependency")}
          </button>
        ) : null}
        <datalist id="task-ids">
          {all
            .filter((x) => x.id !== task.id && !task.dependencies.includes(x.id))
            .map((x) => (
              <option key={x.id} value={x.id}>
                {x.title}
              </option>
            ))}
        </datalist>
        {blocks.length > 0 ? (
          <>
            <h3 className="mt-2 text-xs font-medium text-muted-foreground">{t("task.blocks")}</h3>
            {blocks.map((x) => (
              <TaskRef key={x.id} id={x.id} task={x} />
            ))}
          </>
        ) : null}
      </section>

      {task.pr_url || task.spec_ref || task.merged_locally ? (
        <section aria-labelledby="task-links" className="flex flex-col gap-1.5 border-t pt-4">
          <h3 id="task-links" className="text-xs font-medium text-muted-foreground">
            {t("task.links")}
          </h3>
          {task.pr_url ? (
            <a href={task.pr_url} target="_blank" rel="noopener noreferrer" className="flex items-center gap-2 rounded-md px-2 py-1 hover:bg-secondary">
              <GitPullRequest className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
              {t("task.pr")}
              {task.ci_status ? (
                <span
                  className={cn(
                    "ml-auto text-xs",
                    task.ci_status === "failure" ? "text-status-failed" : task.ci_status === "success" ? "text-status-done" : "text-status-running",
                  )}
                >
                  {t(`ci.${task.ci_status}` as MessageKey)}
                </span>
              ) : null}
            </a>
          ) : null}
          {task.merged_locally ? (
            <span className="flex items-center gap-2 px-2 py-1">
              <GitMerge className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
              {t("task.merged_locally")}
            </span>
          ) : null}
          {task.spec_ref ? (
            <span className="flex min-w-0 items-center gap-2 px-2 py-1">
              <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="truncate font-mono text-xs">{task.spec_ref}</span>
            </span>
          ) : null}
        </section>
      ) : null}

      {running ? (
        <section aria-labelledby="task-run" className="space-y-1 rounded-lg border bg-card p-3">
          <h3 id="task-run" className="flex items-center gap-1.5 text-xs font-medium text-status-done">
            <span className="h-1.5 w-1.5 rounded-full bg-status-done" aria-hidden />
            {t("task.agent_working")}
          </h3>
          <p className="font-mono text-xs text-muted-foreground">{task.model}</p>
          {runningSince ? (
            <p className="text-xs tabular-nums" title={fmt.dateTime(runningSince)}>
              {t("task.running_for", { duration: formatDuration((now - Date.parse(runningSince)) / 1000) })}
            </p>
          ) : null}
        </section>
      ) : null}

      {task.last_updated ? (
        <p className="text-xs text-muted-foreground" title={fmt.dateTime(task.last_updated)}>
          {t("task.updated", { time: formatRelative(task.last_updated, now) })}
        </p>
      ) : null}
    </aside>
  )
}

/** Title and description, edited together: the header's Edit. */
function DefinitionEditor({ task, onSave, onCancel }: { task: Task; onSave: (f: TaskFields) => void; onCancel: () => void }) {
  const [title, setTitle] = useState(task.title)
  const [description, setDescription] = useState(task.description)
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault()
        onSave({ title: title.trim(), description })
      }}
    >
      <input
        aria-label={t("task.title_label")}
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        className="w-full rounded-md border bg-background px-2.5 py-1.5 text-2xl font-semibold tracking-[-0.02em]"
      />
      <textarea
        aria-label={t("task.description_label")}
        value={description}
        rows={5}
        onChange={(e) => setDescription(e.target.value)}
        className="w-full resize-y rounded-md border bg-background px-2.5 py-2 text-sm leading-[22px]"
      />
      <div className="flex justify-end gap-1.5">
        <Button type="button" size="sm" variant="outline" onClick={onCancel}>
          {t("task.cancel")}
        </Button>
        <Button type="submit" size="sm" disabled={title.trim() === ""}>
          {t("task.save")}
        </Button>
      </div>
    </form>
  )
}

/**
 * A task's own page (#374): /work/tasks/<id>, laid out like an issue — title,
 * description and activity in a centered column, the properties in a side
 * panel the local operator edits in place (#421: the same editor as `orch
 * task edit`, and moves through the board's own path). Having an address, it
 * opens in a new tab, survives a reload and can be shared.
 */
export function TaskPage() {
  const { id = "" } = useParams()
  const { data, isLoading, isError, error } = useTaskDetail(id)
  const [propsFolded, setPropsFolded] = useState(readPropsFolded)
  const foldProps = (folded: boolean) => {
    setPropsFolded(folded)
    try {
      window.localStorage.setItem(PROPS_FOLDED_KEY, folded ? "1" : "0")
    } catch {
      // A browser that keeps nothing still folds; it just forgets.
    }
  }
  const { data: history } = useEventsHistory({ taskId: id, enabled: id !== "" })
  // Newest last, as the timeline reads; the latest dispatch names the attempt.
  const events = [...(history?.events ?? [])].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts))
  const lastDispatch = [...events].reverse().find((e) => e.event_type === "dispatch")
  const attempt = Number((lastDispatch?.raw?.extra as Record<string, unknown> | undefined)?.attempt) || undefined
  const { data: list } = useTasks()
  const { data: whoami } = useWhoami()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const [editing, setEditing] = useState(false)
  const [copied, setCopied] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  // Back to wherever the operator came from, filters and all; a page opened
  // straight from a link has nowhere to go back to, so it goes to the list.
  const back = () => (location.key === "default" ? navigate("/work/list") : navigate(-1))

  const all = Array.isArray(list?.tasks) ? list.tasks : []
  const index = all.findIndex((x) => x.id === id)
  const prev = index > 0 ? all[index - 1] : undefined
  const next = index >= 0 && index < all.length - 1 ? all[index + 1] : undefined
  const canEdit = whoami?.profile === "operator"
  // A stakeholder sees the code only when the operator put its route on their allow-list.
  const showChanges = whoami?.profile !== "stakeholder" || (whoami.routes ?? []).includes("api_task_diff")
  const [view, setView] = useState<"details" | "changes">("details")

  // A refused edit (another session's change, a cycle, a stakeholder) says
  // why, from the server's detail.
  const write = async (action: () => Promise<unknown>) => {
    setSaveError(null)
    try {
      await action()
      void queryClient.invalidateQueries({ queryKey: ["task", id] })
      void queryClient.invalidateQueries({ queryKey: ["tasks"] })
      return true
    } catch (err) {
      setSaveError(t("task.edit_failed", { detail: err instanceof Error ? err.message : String(err) }))
      return false
    }
  }
  const save = (fields: TaskFields) => void write(() => editTask(id, fields))
  const move = (status: string) => {
    let note = ""
    if (status === "blocked") {
      note = window.prompt(t("task.block_reason"))?.trim() ?? ""
      if (!note) return
    }
    void write(() => moveTask(id, status, note))
  }
  const copyId = async () => {
    try {
      await navigator.clipboard.writeText(id)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // The id is on screen to copy by hand.
    }
  }

  const arrow = "flex h-7 w-7 items-center justify-center rounded-md border bg-card text-foreground/80 hover:text-foreground disabled:opacity-40"

  return (
    <div className="text-[13px]">
      <div className="flex flex-wrap items-center gap-1.5 border-b px-4 py-2 md:px-6">
        <Button variant="ghost" size="sm" onClick={back} className="gap-1 text-muted-foreground">
          <ArrowLeft className="h-4 w-4" aria-hidden />
          {t("task.back")}
        </Button>
        <span className="font-mono text-xs text-muted-foreground">{id}</span>
        <div className="ml-auto flex items-center gap-1.5">
          {index >= 0 ? <span className="text-xs tabular-nums text-muted-foreground">{t("task.position", { n: index + 1, total: all.length })}</span> : null}
          <button type="button" aria-label={t("task.previous")} disabled={!prev} onClick={() => prev && navigate(taskPath(prev.id))} className={arrow}>
            <ChevronUp className="h-3.5 w-3.5" aria-hidden />
          </button>
          <button type="button" aria-label={t("task.next")} disabled={!next} onClick={() => next && navigate(taskPath(next.id))} className={arrow}>
            <ChevronDown className="h-3.5 w-3.5" aria-hidden />
          </button>
          <Button type="button" size="sm" variant="outline" onClick={() => void copyId()}>
            {copied ? t("task.copied") : t("task.copy_id")}
          </Button>
          {canEdit && data ? (
            <Button type="button" size="sm" variant="outline" onClick={() => setEditing(true)} disabled={editing}>
              {t("task.edit")}
            </Button>
          ) : null}
        </div>
      </div>

      {isLoading ? (
        <div className="mx-auto max-w-[760px] space-y-4 px-4 py-8">
          <Skeleton className="h-7 w-1/2" />
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="h-64 w-full" />
        </div>
      ) : isError ? (
        <div className="p-6">
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t("task.load_failed")}</AlertTitle>
            <AlertDescription>{error?.message ?? t("common.unknown_error")}</AlertDescription>
          </Alert>
        </div>
      ) : data ? (
        <div className="flex flex-col lg:flex-row">
          <div className="min-w-0 flex-1 px-4 pb-10 pt-8 md:px-10">
            <div className="mx-auto flex max-w-[760px] flex-col gap-7">
              {showChanges ? (
                <div role="tablist" aria-label={t("task.view")} className="-mb-3 flex gap-0.5 border-b pb-2">
                  {(["details", "changes"] as const).map((x) => (
                    <button
                      key={x}
                      type="button"
                      role="tab"
                      aria-selected={view === x}
                      onClick={() => setView(x)}
                      className={cn(
                        "h-[26px] rounded-md border px-2.5 text-xs",
                        view === x ? "border-input bg-secondary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
                      )}
                    >
                      {t(x === "details" ? "task.tab_details" : "task.tab_changes")}
                    </button>
                  ))}
                </div>
              ) : null}
              {showChanges && view === "changes" ? (
                <>
                  <h1 className="text-2xl font-semibold leading-tight tracking-[-0.02em]">{data.title}</h1>
                  <TaskChanges task={data} events={events} canAct={canEdit} />
                </>
              ) : (
                <>
                  {editing ? (
                    <DefinitionEditor
                      task={data}
                      onCancel={() => setEditing(false)}
                      onSave={(fields) => void write(() => editTask(id, fields)).then((ok) => ok && setEditing(false))}
                    />
                  ) : (
                    <div className="space-y-2.5">
                      <h1 className="text-2xl font-semibold leading-tight tracking-[-0.02em]">{data.title}</h1>
                      {data.description ? (
                        <Markdown source={data.description} breaks className="text-sm leading-[22px] text-foreground/85" />
                      ) : (
                        <p className="text-sm leading-[22px] text-muted-foreground">{t("task.no_description")}</p>
                      )}
                    </div>
                  )}

                  {saveError ? (
                    <p role="alert" className="text-xs text-destructive">
                      {saveError}
                    </p>
                  ) : null}

                  {data.reason ? (
                    <div role="note" className="flex gap-2.5 rounded-lg border border-status-blocked/30 bg-status-blocked/10 px-3 py-2.5">
                      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-status-blocked" aria-hidden />
                      <div className="space-y-0.5">
                        <p className="font-medium text-status-blocked">{t(statusMeta(data.status).label)}</p>
                        <p className="whitespace-pre-wrap">{data.reason}</p>
                      </div>
                    </div>
                  ) : null}

                  {data.files.length > 0 ? (
                    <section aria-labelledby="task-files" className="space-y-2">
                      <h2 id="task-files" className="text-xs font-medium text-muted-foreground">
                        {t("task.files_touched", { count: data.files.length })}
                      </h2>
                      <ul className="flex flex-wrap gap-1.5">
                        {data.files.map((f) => (
                          <li key={f} className="rounded-md border px-2 py-0.5 font-mono text-xs text-foreground/85">
                            {f}
                          </li>
                        ))}
                      </ul>
                    </section>
                  ) : null}

                  <Activity task={data} events={events} />
                  <TaskSteer task={data} />
                </>
              )}
            </div>
          </div>
          {propsFolded ? (
            <aside aria-label={t("task.properties")} className="flex justify-end border-t px-2 py-3 lg:w-11 lg:shrink-0 lg:justify-center lg:border-l lg:border-t-0">
              <button
                type="button"
                onClick={() => foldProps(false)}
                aria-label={t("task.show_properties")}
                aria-expanded={false}
                className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <PanelRightOpen className="h-4 w-4" aria-hidden />
              </button>
            </aside>
          ) : (
            <Properties task={data} all={all} canEdit={canEdit} save={save} move={move} attempt={attempt} runningSince={lastDispatch?.ts} onFold={() => foldProps(true)} />
          )}
        </div>
      ) : null}
    </div>
  )
}

const PROPS_FOLDED_KEY = "orch.taskPropsFolded"

/** Whether the properties panel was left folded in this browser. */
function readPropsFolded(): boolean {
  try {
    return window.localStorage.getItem(PROPS_FOLDED_KEY) === "1"
  } catch {
    return false
  }
}

/** A link to a task's page, for places that list tasks. */
export function TaskLink({ id, className, children }: { id: string; className?: string; children: ReactNode }) {
  return (
    <Link to={taskPath(id)} className={className}>
      {children}
    </Link>
  )
}
