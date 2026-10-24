import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { TaskChanges } from "@/components/TaskChanges"
import type { FormattedEvent, Task } from "@/lib/types"

const api = vi.hoisted(() => ({
  getTaskDiff: vi.fn(),
  acceptTask: vi.fn(),
  sendBackTask: vi.fn(),
  moveTask: vi.fn(),
}))
vi.mock("@/lib/api", () => api)

const task = { id: "F1.T2", title: "Refill", status: "done", pr_url: "https://github.com/o/r/pull/7", comments: [] } as unknown as Task

const bigPatch = "@@ -0,0 +1,500 @@\n" + Array.from({ length: 500 }, (_, i) => `+line ${i}`).join("\n") + "\n"

const diff = {
  task_id: "F1.T2",
  base: "origin/main",
  branch: "orch/F1.T2",
  files: [
    { path: "app.go", additions: 2, deletions: 1, patch: "@@ -1,3 +1,4 @@\n package app\n-func A() {}\n+func A() { B() }\n+func B() {}\n \n" },
    { path: "dist/bundle.js", additions: 500, deletions: 0, patch: bigPatch },
  ],
}

const events = [
  {
    ts: "2026-10-12T21:40:00Z",
    task_id: "F1.T2",
    event_type: "verify_failed",
    backend: "claude",
    human: "",
    severity: "warn",
    raw: { extra: { attempt: 1, reason: "the review asks for changes.\n\n### Blocking\n\n- `app.go:3` **B is never tested**: add a test\n- **Missing changelog**" } },
  },
] as unknown as FormattedEvent[]

function renderChanges(canAct: boolean, evs: FormattedEvent[] = events) {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <TaskChanges task={task} events={evs} canAct={canAct} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset()
  api.getTaskDiff.mockResolvedValue(diff)
  api.acceptTask.mockResolvedValue({ message: "F1.T2 accepted" })
  api.sendBackTask.mockResolvedValue({ message: "sent back" })
  api.moveTask.mockResolvedValue({ message: "blocked" })
})

describe("TaskChanges", () => {
  // #454: the branch's diff, file by file with its counts; a large file
  // stays collapsed; the review's finding sits under the line it names.
  it("shows the diff with the review's findings at their line", async () => {
    renderChanges(false)
    expect(await screen.findByText("2 files changed on orch/F1.T2 against origin/main")).toBeInTheDocument()
    const list = screen.getByRole("list", { name: "Changed files" })
    expect(within(list).getByText("app.go")).toBeInTheDocument()
    expect(within(list).getByText("+500")).toBeInTheDocument()

    const added = screen.getByText((_, el) => el?.tagName === "TD" && el.textContent === "func A() { B() }")
    expect(added.closest("tr")).toHaveAttribute("data-kind", "add")
    // Line 3 of the new file is `func B() {}`; the finding follows its row.
    const row = screen.getByText((_, el) => el?.tagName === "TD" && el.textContent === "func B() {}").closest("tr")!
    expect(row.nextElementSibling).toHaveTextContent("line 3 · B is never tested: add a test")
    // A finding with no file is listed with the verify result.
    expect(screen.getByText("Missing changelog")).toBeInTheDocument()
    expect(screen.getByText("Verify sent attempt 1 back")).toBeInTheDocument()

    // The 500-line bundle is collapsed: none of its lines are drawn.
    expect(screen.queryByText("line 499")).not.toBeInTheDocument()
    expect(document.getElementById("diff-file-1")).not.toHaveAttribute("open")
    // No actions for anyone but the local operator.
    expect(screen.queryByRole("button", { name: "Accept" })).not.toBeInTheDocument()
  })

  it("accepts, sends back and blocks through the CLI's functions", async () => {
    renderChanges(true)
    fireEvent.click(await screen.findByRole("button", { name: "Accept" }))
    await waitFor(() => expect(api.acceptTask).toHaveBeenCalledWith("F1.T2"))
    expect(await screen.findByRole("status")).toHaveTextContent("F1.T2 accepted")

    const note = screen.getByRole("textbox", { name: /A note/ })
    expect(screen.getByRole("button", { name: "Send back" })).toBeDisabled()
    fireEvent.change(note, { target: { value: "test B" } })
    fireEvent.click(screen.getByRole("button", { name: "Send back" }))
    await waitFor(() => expect(api.sendBackTask).toHaveBeenCalledWith("F1.T2", "test B"))

    fireEvent.change(note, { target: { value: "wrong approach" } })
    fireEvent.click(screen.getByRole("button", { name: "Block" }))
    await waitFor(() => expect(api.moveTask).toHaveBeenCalledWith("F1.T2", "blocked", "wrong approach"))
  })

  // #463: the findings come structured in the event, uncut, so all twelve
  // sit at their lines even though the reason was cut after a few of them.
  it("anchors every structured finding, past the reason's cut", async () => {
    const twelve = Array.from({ length: 12 }, (_, i) => ({ severity: "blocking", file: "store.go", line: i + 1, title: `finding ${i + 1}`, detail: "x".repeat(80) }))
    const md = twelve.map((f) => `- \`${f.file}:${f.line}\` **${f.title}**: ${f.detail}`).join("\n")
    api.getTaskDiff.mockResolvedValue({
      ...diff,
      files: [{ path: "store.go", additions: 12, deletions: 0, patch: "@@ -0,0 +1,12 @@\n" + Array.from({ length: 12 }, (_, i) => `+code ${i + 1}`).join("\n") + "\n" }],
    })
    const failed = [
      {
        ...events[0],
        raw: { extra: { attempt: 1, sent_back: true, reason: ("the review asks for changes.\n\n" + md).slice(0, 497) + "...", findings: twelve } },
      },
    ] as unknown as FormattedEvent[]
    renderChanges(false, failed)
    await screen.findByText("1 file changed on orch/F1.T2 against origin/main")
    for (let i = 1; i <= 12; i++) {
      const row = screen.getByText((_, el) => el?.tagName === "TD" && el.textContent === `code ${i}`).closest("tr")!
      expect(row.nextElementSibling).toHaveTextContent(`line ${i} · finding ${i}: ${"x".repeat(80)}`)
    }
    expect(screen.getByText("12 findings", { selector: "p" })).toBeInTheDocument()
  })

  // #463: a verify that passed says so, with what it ran and who reviewed.
  it("says the verify passed", async () => {
    const passed = [
      ...events,
      {
        ts: "2026-10-12T21:50:00Z",
        task_id: "F1.T2",
        event_type: "verify_passed",
        backend: "claude",
        human: "",
        severity: "info",
        raw: { extra: { attempt: 2, reviewer: "gemini", commands: [{ command: "go test ./...", duration_s: 12.5 }] } },
      },
    ] as unknown as FormattedEvent[]
    renderChanges(false, passed)
    expect(await screen.findByText("Verify passed attempt 2")).toBeInTheDocument()
    expect(screen.getByText("· reviewed by gemini", { exact: false })).toBeInTheDocument()
    expect(screen.getByText("12.5s")).toBeInTheDocument()
    expect(screen.queryByText("The local verify sent nothing back.")).not.toBeInTheDocument()
    // The pass is about the diff shown; the earlier send-back's finding is not anchored in it.
    await screen.findByText("2 files changed on orch/F1.T2 against origin/main")
    expect(screen.queryByText("line 3 · B is never tested: add a test")).not.toBeInTheDocument()
  })

  it("says why there is nothing to show", async () => {
    api.getTaskDiff.mockRejectedValue({ response: { data: { detail: "the task's work is on no branch here (orch/F1.T2)" } } })
    renderChanges(false)
    expect(await screen.findByRole("alert")).toHaveTextContent("No changes to show: the task's work is on no branch here (orch/F1.T2)")
  })
})
