import "@testing-library/jest-dom/vitest"
import { act, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { RunReceipt } from "@/components/RunReceipt"
import { receiptMarkdown, type ReceiptPayload } from "@/hooks/useReceipt"

const payload: ReceiptPayload = {
  markdown: "## orch run `r` — finished\n",
  built_with: "Built with [orch](https://github.com/hectorcanaimero/orch)",
  receipt: {
    run_id: "r", started_at: "", finished_at: "", finished: true, wall_seconds: 60, agent_seconds: 30,
    dispatches: 1, failed_attempts: 0, retries: 0, done: [], blocked: [], providers: [],
    total_cost_usd: 0, estimated_cost_usd: 0, prs: [],
  },
}

afterEach(() => {
  window.localStorage.clear()
  vi.unstubAllGlobals()
})

describe("RunReceipt", () => {
  it("adds the Built with line unless unticked", () => {
    expect(receiptMarkdown(payload, true)).toBe(`${payload.markdown}\n${payload.built_with}\n`)
    expect(receiptMarkdown(payload, false)).toBe(payload.markdown)
  })

  it("copies the Markdown, remembering the opt-out", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { clipboard: { writeText } })
    render(<RunReceipt payload={payload} now={0} />)

    fireEvent.click(screen.getByRole("checkbox", { name: /Built with orch/ }))
    expect(window.localStorage.getItem("orch.receipt.builtWith")).toBe("off")
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy as Markdown" }))
    })
    expect(writeText).toHaveBeenCalledWith(payload.markdown)
  })

  it("shows the text to select when the clipboard is refused", async () => {
    vi.stubGlobal("navigator", { clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } })
    render(<RunReceipt payload={payload} now={0} />)
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy as Markdown" }))
    })
    expect(screen.getByRole("textbox")).toHaveValue(`${payload.markdown}\n${payload.built_with}\n`)
  })
})
