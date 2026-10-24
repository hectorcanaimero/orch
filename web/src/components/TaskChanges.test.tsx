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
const ci = vi.hoisted(() => ({ provider: "github" }))
vi.mock("@/hooks/useCI", () => ({ useCI: () => ({ data: { config: { provider: ci.provider } } }) }))

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

function renderChanges(canAct: boolean, shown: Task = task) {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <TaskChanges task={shown} events={events} canAct={canAct} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  ci.provider = "github"
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

  // With vcs.provider local there is no PR: Accept merges the task's branch
  // into the base here, and is offered until the task has landed.
  it("accepts in local mode, where there is no PR", async () => {
    ci.provider = "local"
    const local = { ...task, pr_url: undefined } as unknown as Task
    const { unmount } = renderChanges(true, local)
    expect(await screen.findByText(/Accept merges the task's branch into the base here/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Accept" }))
    await waitFor(() => expect(api.acceptTask).toHaveBeenCalledWith("F1.T2"))
    unmount()

    renderChanges(true, { ...local, merged_locally: true } as Task)
    expect(await screen.findByRole("button", { name: "Accept" })).toBeDisabled()
  })

  it("says why there is nothing to show", async () => {
    api.getTaskDiff.mockRejectedValue({ response: { data: { detail: "the task's work is on no branch here (orch/F1.T2)" } } })
    renderChanges(false)
    expect(await screen.findByRole("alert")).toHaveTextContent("No changes to show: the task's work is on no branch here (orch/F1.T2)")
  })
})
