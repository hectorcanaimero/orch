import { Fragment, useEffect, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, MessageSquareWarning } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useCI } from "@/hooks/useCI"
import { t } from "@/i18n"
import { acceptTask, getTaskDiff, moveTask, sendBackTask, type DiffFile } from "@/lib/api"
import { statusKey } from "@/lib/status"
import type { FormattedEvent, Task } from "@/lib/types"

type Highlighter = typeof import("@/lib/highlight")

/** A file this large (changed lines) opens collapsed. */
const LARGE = 400

interface Row {
  kind: "hunk" | "add" | "del" | "ctx" | "meta"
  old?: number
  new?: number
  text: string
}

/** A file's hunks as rows, each with its old and new line number. */
function parsePatch(patch: string): Row[] {
  const rows: Row[] = []
  let o = 0
  let n = 0
  const lines = patch.split("\n")
  if (lines.at(-1) === "") lines.pop()
  for (const line of lines) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)/.exec(line)
    if (hunk) {
      o = Number(hunk[1])
      n = Number(hunk[2])
      rows.push({ kind: "hunk", text: line })
    } else if (line.startsWith("+")) rows.push({ kind: "add", new: n++, text: line.slice(1) })
    else if (line.startsWith("-")) rows.push({ kind: "del", old: o++, text: line.slice(1) })
    else if (line.startsWith("\\")) rows.push({ kind: "meta", text: line })
    else rows.push({ kind: "ctx", old: o++, new: n++, text: line.slice(1) })
  }
  return rows
}

/** A review finding, at file:line when it names one. */
interface Finding {
  file?: string
  line?: number
  text: string
}

/** What the local verify said: each send-back, with the review's findings it listed. */
interface VerifyEntry {
  at: string
  attempt?: number
  reason: string
  findings: Finding[]
}

/** The verify's send-backs from the task's events; findings are the review's "- `file:line` **title**" lines. */
function verifyEntries(events: FormattedEvent[]): VerifyEntry[] {
  return events
    .filter((e) => e.event_type === "verify_failed")
    .map((e) => {
      const extra = (e.raw?.extra ?? {}) as Record<string, unknown>
      const reason = typeof extra.reason === "string" ? extra.reason : ""
      const findings: Finding[] = []
      for (const line of reason.split("\n")) {
        const m = /^- (?:`([^`]+?)(?::(\d+))?` )?(.+)$/.exec(line.trim())
        if (m && (m[1] || line.includes("**"))) findings.push({ file: m[1], line: m[2] ? Number(m[2]) : undefined, text: m[3].replaceAll("**", "") })
      }
      return { at: e.ts, attempt: Number(extra.attempt) || undefined, reason, findings }
    })
}

function FindingNote({ f }: { f: Finding }) {
  return (
    <div role="note" className="flex gap-1.5 border-y border-status-blocked/30 bg-status-blocked/10 px-3 py-1.5 font-sans text-xs">
      <MessageSquareWarning className="mt-0.5 h-3.5 w-3.5 shrink-0 text-status-blocked" aria-hidden />
      <span>
        {f.line ? <span className="font-mono text-muted-foreground">{t("changes.line", { line: f.line })} · </span> : null}
        {f.text}
      </span>
    </div>
  )
}

const ROW: Record<Row["kind"], string> = {
  add: "bg-status-done/10",
  del: "bg-status-failed/10",
  ctx: "",
  hunk: "bg-secondary text-muted-foreground",
  meta: "text-muted-foreground",
}

function FileDiff({ file, index, findings, hl }: { file: DiffFile; index: number; findings: Finding[]; hl: Highlighter | null }) {
  const large = file.additions + file.deletions > LARGE || !!file.truncated
  const [open, setOpen] = useState(!large && !file.binary)
  const rows = open ? parsePatch(file.patch) : []
  const lang = hl?.languageOf(file.path) ?? ""
  const anchored = new Set(rows.filter((r) => r.new !== undefined && r.kind !== "del").map((r) => r.new))
  const loose = findings.filter((f) => !f.line || !open || !anchored.has(f.line))
  return (
    <details
      id={`diff-file-${index}`}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
      className="overflow-hidden rounded-lg border bg-card"
    >
      <summary className="flex cursor-pointer items-center gap-2 border-b bg-sidebar px-3 py-1.5 text-xs">
        <span className="min-w-0 truncate font-mono">
          {file.old_path ? `${file.old_path} → ` : ""}
          {file.path}
        </span>
        {findings.length > 0 ? (
          <span className="rounded border border-status-blocked/40 px-1 text-[10px] text-status-blocked">{t("changes.findings", { count: findings.length })}</span>
        ) : null}
        <span className="ml-auto shrink-0 tabular-nums">
          <span className="text-status-done">+{file.additions}</span> <span className="text-status-failed">−{file.deletions}</span>
        </span>
      </summary>
      {loose.map((f, i) => (
        <FindingNote key={i} f={f} />
      ))}
      {file.binary ? <p className="px-3 py-2 text-xs text-muted-foreground">{t("changes.binary")}</p> : null}
      {open && rows.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse font-mono text-xs leading-5">
            <tbody>
              {rows.map((r, i) => {
                const html = r.kind === "hunk" || r.kind === "meta" ? null : hl?.highlightLine(r.text, lang)
                const here = r.kind !== "del" && r.new !== undefined ? findings.filter((f) => f.line === r.new) : []
                return (
                  <Fragment key={i}>
                    <tr data-kind={r.kind} className={ROW[r.kind]}>
                      <td className="w-10 select-none px-2 text-right text-muted-foreground">{r.old ?? ""}</td>
                      <td className="w-10 select-none px-2 text-right text-muted-foreground">{r.new ?? ""}</td>
                      <td className="w-4 select-none text-center text-muted-foreground">{r.kind === "add" ? "+" : r.kind === "del" ? "−" : ""}</td>
                      {html != null ? (
                        <td className="whitespace-pre pr-3" dangerouslySetInnerHTML={{ __html: html }} />
                      ) : (
                        <td className="whitespace-pre pr-3">{r.text}</td>
                      )}
                    </tr>
                    {here.length > 0 ? (
                      <tr>
                        <td colSpan={4}>
                          {here.map((f, j) => (
                            <FindingNote key={j} f={f} />
                          ))}
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
        </div>
      ) : null}
      {file.truncated && open ? <p className="px-3 py-2 text-xs text-muted-foreground">{t("changes.truncated")}</p> : null}
    </details>
  )
}

/** Accept, Send back with a note, Block with a reason: the CLI's own functions. */
function ReviewActions({ task }: { task: Task }) {
  const queryClient = useQueryClient()
  const [note, setNote] = useState("")
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const running = statusKey(task.status) === "in_progress"
  // vcs.provider local has no PR: Accept squash-merges the task's branch into
  // the base here, until it has landed.
  const local = useCI().data?.config?.provider === "local"
  const acceptable = local ? !task.merged_locally && !running : Boolean(task.pr_url)
  const hint = local
    ? t(task.merged_locally ? "changes.merged_locally" : "changes.hint_local")
    : t(task.pr_url ? "changes.hint" : "changes.no_pr")

  async function run(action: () => Promise<string>) {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(await action())
      setNote("")
      void queryClient.invalidateQueries({ queryKey: ["task", task.id] })
      void queryClient.invalidateQueries({ queryKey: ["tasks"] })
    } catch (err) {
      setError(t("steer.failed", { detail: err instanceof Error ? err.message : String(err) }))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-labelledby="review-title" className="space-y-2 rounded-[10px] border bg-card p-3">
      <h3 id="review-title" className="text-xs font-medium text-muted-foreground">
        {t("changes.review")}
      </h3>
      <textarea
        aria-label={t("changes.note")}
        className="min-h-14 w-full resize-y rounded-md border bg-background px-2.5 py-2 text-[13px]"
        placeholder={t("changes.note")}
        rows={2}
        value={note}
        maxLength={2000}
        onChange={(e) => setNote(e.target.value)}
      />
      <div className="flex flex-wrap items-center gap-1.5">
        <p className="text-xs text-muted-foreground">{hint}</p>
        <div className="ml-auto flex flex-wrap gap-1.5">
          <Button
            size="sm"
            variant="outline"
            className="border-status-failed/40 text-status-failed hover:text-status-failed"
            disabled={busy || note.trim() === ""}
            onClick={() => run(async () => (await moveTask(task.id, "blocked", note)).message)}
          >
            {t("changes.block")}
          </Button>
          <Button size="sm" variant="outline" disabled={busy || running || note.trim() === ""} onClick={() => run(async () => (await sendBackTask(task.id, note)).message)}>
            {t("changes.send_back")}
          </Button>
          <Button size="sm" disabled={busy || !acceptable} onClick={() => run(async () => (await acceptTask(task.id)).message)}>
            {t("changes.accept")}
          </Button>
        </div>
      </div>
      {result ? (
        <p className="text-xs" role="status">
          {result}
        </p>
      ) : null}
      {error ? (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      ) : null}
    </section>
  )
}

/**
 * The task page's Changes tab (#454): what the task's branch changed against
 * its base, file by file with highlighted hunks, the local verify's
 * send-backs and the review's findings at their file:line, and — for the
 * local operator — Accept, Send back and Block.
 */
export function TaskChanges({ task, events, canAct }: { task: Task; events: FormattedEvent[]; canAct: boolean }) {
  const { data, isLoading, isError, error } = useQuery({ queryKey: ["task-diff", task.id], queryFn: () => getTaskDiff(task.id) })
  const [hl, setHl] = useState<Highlighter | null>(null)
  useEffect(() => {
    let live = true
    void import("@/lib/highlight").then((m) => live && setHl(m))
    return () => {
      live = false
    }
  }, [])

  const verify = verifyEntries(events)
  const findings = verify.flatMap((v) => v.findings)
  const paths = new Set(data?.files.map((f) => f.path))
  const e2e = [...events].reverse().find((e) => e.event_type === "e2e_report")
  const e2eExtra = (e2e?.raw?.extra ?? {}) as Record<string, unknown>
  const detail = (error as { response?: { data?: { detail?: string } } } | null)?.response?.data?.detail
  const added = data?.files.reduce((n, f) => n + f.additions, 0) ?? 0
  const removed = data?.files.reduce((n, f) => n + f.deletions, 0) ?? 0

  return (
    <div className="flex flex-col gap-4">
      {canAct ? <ReviewActions task={task} /> : null}

      <section aria-labelledby="verify-title" className="space-y-2">
        <h3 id="verify-title" className="text-xs font-medium text-muted-foreground">
          {t("changes.verify")}
        </h3>
        {e2e ? (
          <p className="text-xs">{t("changes.e2e", { passed: String(e2eExtra.passed ?? 0), failed: String(e2eExtra.failed ?? 0) })}</p>
        ) : null}
        {verify.length === 0 ? <p className="text-xs text-muted-foreground">{t("changes.verify_none")}</p> : null}
        {verify.map((v, i) => (
          <div key={i} className="space-y-1 rounded-md border bg-sidebar px-2.5 py-1.5 text-xs">
            <p className="text-status-blocked">{t("changes.verify_sent_back", { attempt: String(v.attempt ?? "?") })}</p>
            <p className="whitespace-pre-wrap break-words font-mono text-muted-foreground">{v.reason}</p>
          </div>
        ))}
        {findings
          .filter((f) => !f.file || !paths.has(f.file))
          .map((f, i) => (
            <FindingNote key={i} f={{ ...f, text: f.file ? `${f.file}: ${f.text}` : f.text }} />
          ))}
      </section>

      {isLoading ? <Skeleton className="h-40 w-full" /> : null}
      {isError ? (
        <div role="alert" className="flex gap-2 rounded-lg border border-status-blocked/30 bg-status-blocked/10 px-3 py-2 text-xs">
          <AlertTriangle className="h-4 w-4 shrink-0 text-status-blocked" aria-hidden />
          {t("changes.unavailable", { detail: detail ?? error?.message ?? "" })}
        </div>
      ) : null}
      {data ? (
        <>
          <p className="text-xs text-muted-foreground">
            {t("changes.summary", { count: data.files.length, branch: data.branch, base: data.base })}{" "}
            <span className="tabular-nums">
              <span className="text-status-done">+{added}</span> <span className="text-status-failed">−{removed}</span>
            </span>
          </p>
          {data.files.length > 0 ? (
            <ul aria-label={t("changes.files")} className="flex flex-col rounded-lg border">
              {data.files.map((f, i) => (
                <li key={f.path} className="border-b last:border-b-0">
                  <button
                    type="button"
                    onClick={() => {
                      const el = document.getElementById(`diff-file-${i}`) as HTMLDetailsElement | null
                      if (el) {
                        el.open = true
                        el.scrollIntoView?.({ block: "start" })
                      }
                    }}
                    className="flex w-full items-center gap-2 px-3 py-1 text-left text-xs hover:bg-secondary"
                  >
                    <span className="min-w-0 truncate font-mono">{f.path}</span>
                    <span className="ml-auto shrink-0 tabular-nums">
                      <span className="text-status-done">+{f.additions}</span> <span className="text-status-failed">−{f.deletions}</span>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-xs text-muted-foreground">{t("changes.empty")}</p>
          )}
          {data.files.map((f, i) => (
            <FileDiff key={f.path} file={f} index={i} findings={findings.filter((x) => x.file === f.path)} hl={hl} />
          ))}
        </>
      ) : null}
    </div>
  )
}
