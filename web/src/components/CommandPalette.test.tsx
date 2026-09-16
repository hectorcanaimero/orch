import "@testing-library/jest-dom/vitest"
import { act, fireEvent, render, screen, within } from "@testing-library/react"
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { CommandPalette } from "@/components/CommandPalette"
import { AppLayout } from "@/components/AppLayout"
import { DESTINATIONS } from "@/lib/nav"

const openedTask = vi.fn()
vi.mock("@/components/TaskDetailModal", () => ({
  TaskDetailModal: ({ taskId }: { taskId: string | null }) => {
    if (taskId) openedTask(taskId)
    return taskId ? <p>detail of {taskId}</p> : null
  },
}))
vi.mock("@/hooks/useTasks", () => ({
  useTasks: () => ({
    data: {
      tasks: [
        { id: "F2.T3", title: "Add DELETE /items/{id}", status: "blocked", phase: 2, model: "claude/claude-sonnet-4-6" },
        { id: "F1.T1", title: "Health endpoint", status: "done", phase: 1, model: "codex/gpt-5" },
      ],
    },
  }),
}))
vi.mock("@/hooks/useWhoami", () => ({ useWhoami: () => ({ data: { profile: "operator" } }) }))
vi.mock("@/hooks/usePortfolio", () => ({
  usePortfolio: () => ({ data: undefined, error: undefined }),
  isPortfolioNavVisible: () => false,
}))
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ clearToken: vi.fn() }) }))
vi.mock("@/pages/LogsPage", () => ({ LogsPage: () => <p>event log</p> }))

const OPERATOR_NAV = DESTINATIONS.map((d) => ({ ...d, tabs: d.tabs.filter((tab) => !tab.portfolioGated) }))

function Where() {
  const { pathname, search } = useLocation()
  return <output aria-label="location">{pathname + search}</output>
}

function renderPalette(path = "/", props: Partial<Parameters<typeof CommandPalette>[0]> = {}) {
  const onOpenChange = vi.fn()
  const onOpenLogs = vi.fn()
  render(
    <MemoryRouter initialEntries={[path]}>
      <CommandPalette
        open
        onOpenChange={onOpenChange}
        destinations={OPERATOR_NAV}
        isStakeholder={false}
        canSeeLogs
        onOpenLogs={onOpenLogs}
        onShowShortcuts={vi.fn()}
        {...props}
      />
      <Where />
    </MemoryRouter>,
  )
  return { onOpenChange, onOpenLogs, input: screen.getByRole("combobox") }
}

const location = () => screen.getByRole("status", { name: "location" }).textContent
const type = (input: HTMLElement, value: string) => fireEvent.change(input, { target: { value } })
const key = (el: HTMLElement, k: string) => fireEvent.keyDown(el, { key: k })

describe("CommandPalette", () => {
  beforeEach(() => openedTask.mockReset())
  afterEach(() => vi.unstubAllGlobals())

  it("focuses its search field and keeps Tab there, so arrows are the only way through the results", () => {
    const { input } = renderPalette()
    expect(input).toHaveFocus()
    expect(input).toHaveAttribute("aria-controls", screen.getByRole("listbox").id)
    expect(fireEvent.keyDown(input, { key: "Tab" })).toBe(false) // default prevented: focus does not leave
    expect(input).toHaveFocus()
  })

  it("moves through the results with the arrows and opens the chosen page with Enter", () => {
    const { input, onOpenChange } = renderPalette()
    type(input, "budget")
    const options = screen.getAllByRole("option")
    expect(options[0]).toHaveTextContent("Cost › Budget")
    expect(options[0]).toHaveAttribute("aria-selected", "true")
    expect(input).toHaveAttribute("aria-activedescendant", options[0].id)
    key(input, "ArrowUp") // wraps to the last result
    expect(screen.getAllByRole("option").at(-1)).toHaveAttribute("aria-selected", "true")
    key(input, "ArrowDown")
    key(input, "Enter")
    expect(location()).toBe("/cost/budget")
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("finds a task by id and opens its detail", () => {
    const { input } = renderPalette()
    type(input, "F2.T3")
    const tasks = screen.getByRole("group", { name: "Tasks" })
    expect(within(tasks).getByRole("option")).toHaveTextContent("Add DELETE /items/{id}")
    fireEvent.click(within(tasks).getByRole("option"))
    expect(openedTask).toHaveBeenCalledWith("F2.T3")
  })

  it("adds a filter to the ones already set on a filtered page", () => {
    const { input } = renderPalette("/work/kanban?phase=2")
    type(input, "status blocked")
    fireEvent.click(within(screen.getByRole("group", { name: "Filter the work" })).getAllByRole("option")[0])
    expect(location()).toBe("/work/kanban?phase=2&status=blocked")
  })

  it("starts the task list fresh when filtering from another page", () => {
    const { input } = renderPalette("/cost/budget?logs=1")
    type(input, "model codex")
    fireEvent.click(within(screen.getByRole("group", { name: "Filter the work" })).getByRole("option"))
    expect(location()).toBe("/work/list?model=codex%2Fgpt-5")
  })

  it("copies a command instead of running anything, and offers the unblock command for a blocked task", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } })
    const { input, onOpenChange } = renderPalette()
    type(input, "F2.T3")
    const commands = screen.getByRole("group", { name: "Copy a command" })
    expect(within(commands).getByRole("option")).toHaveTextContent("orch task set --id F2.T3 --status todo")
    type(input, "doctor")
    await act(async () => key(input, "Enter"))
    expect(writeText).toHaveBeenCalledWith("orch doctor")
    expect(screen.getByText("Copied: orch doctor")).toBeInTheDocument()
    expect(onOpenChange).not.toHaveBeenCalled()
  })

  it("shows the command to copy by hand when the browser refuses the clipboard", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined })
    const { input } = renderPalette()
    type(input, "orch status")
    await act(async () => key(input, "Enter"))
    expect(screen.getByText("orch status", { selector: "code" })).toBeInTheDocument()
  })

  it("offers a client no commands and no task search", () => {
    const { input } = renderPalette("/", { isStakeholder: true, canSeeLogs: false })
    type(input, "o")
    expect(screen.queryByRole("group", { name: "Copy a command" })).not.toBeInTheDocument()
    expect(screen.queryByRole("group", { name: "Tasks" })).not.toBeInTheDocument()
    expect(screen.queryByText("Open the event log")).not.toBeInTheDocument()
  })

  it("says so when nothing matches", () => {
    const { input } = renderPalette()
    type(input, "zzqx")
    expect(screen.getByText("Nothing matches “zzqx”.")).toBeInTheDocument()
    expect(input).not.toHaveAttribute("aria-activedescendant")
  })

  it("closes on Escape without letting the key reach the log panel underneath", () => {
    const { input, onOpenChange } = renderPalette()
    const reachedWindow = vi.fn()
    window.addEventListener("keydown", reachedWindow)
    key(input, "Escape")
    window.removeEventListener("keydown", reachedWindow)
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(reachedWindow).not.toHaveBeenCalled()
  })
})

function renderLayout(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="*"
          element={
            <AppLayout>
              <input aria-label="notes" />
              <Where />
            </AppLayout>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

describe("keyboard shortcuts", () => {
  it("opens and closes the palette with Ctrl+K, even from a text field", () => {
    renderLayout("/")
    fireEvent.keyDown(screen.getByRole("textbox", { name: "notes" }), { key: "k", ctrlKey: true })
    expect(screen.getByRole("dialog", { name: "Search pages, tasks and commands" })).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "k", ctrlKey: true })
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("goes to a destination with g then its letter", () => {
    renderLayout("/")
    fireEvent.keyDown(document.body, { key: "g" })
    fireEvent.keyDown(document.body, { key: "c" })
    expect(location()).toBe("/cost/budget")
  })

  it("ignores letters typed into a field", () => {
    renderLayout("/")
    const notes = screen.getByRole("textbox", { name: "notes" })
    fireEvent.keyDown(notes, { key: "g" })
    fireEvent.keyDown(notes, { key: "w" })
    fireEvent.keyDown(notes, { key: "?" })
    expect(location()).toBe("/")
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("lists the shortcuts on ?, and / goes to the task list", () => {
    renderLayout("/cost/budget")
    fireEvent.keyDown(document.body, { key: "?" })
    const dialog = screen.getByRole("dialog")
    expect(within(dialog).getByText("Keyboard shortcuts")).toBeInTheDocument()
    expect(within(dialog).getByText("Go to Work")).toBeInTheDocument()
    fireEvent.keyDown(window, { key: "Escape" })
    fireEvent.keyDown(document.body, { key: "/" })
    expect(location()).toBe("/work/list")
  })

  it("opens the palette from the search button", () => {
    renderLayout("/")
    fireEvent.click(within(screen.getByRole("complementary")).getByRole("button", { name: /Search/ }))
    expect(screen.getByRole("combobox")).toHaveFocus()
  })
})
