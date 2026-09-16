import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { Explain } from "@/components/Explain"
import { glossary } from "@/i18n/glossary"

describe("Explain", () => {
  it("renders the term, and its own label when given one", () => {
    const { unmount } = render(<Explain term="critical_path" />)
    expect(screen.getByText("Critical path")).toBeInTheDocument()
    unmount()
    render(<Explain term="estimate">Est.</Explain>)
    expect(screen.getByText("Est.")).toBeInTheDocument()
  })

  it("opens the definition, how it is computed and the data source", () => {
    const entry = glossary.downstream_impact
    render(<Explain term="downstream_impact" />)
    expect(screen.queryByText(entry.definition)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: `What is ${entry.title}?` }))

    expect(screen.getByText(entry.definition)).toBeInTheDocument()
    expect(screen.getByText(entry.computed)).toBeInTheDocument()
    expect(screen.getByText(entry.source)).toBeInTheDocument()
  })

  // It sits inside sortable headers and clickable cards; opening it must not
  // also sort the column or open the task.
  it("does not pass the click to its parent", () => {
    const parent = vi.fn()
    render(
      <div onClick={parent}>
        <Explain term="deps" />
      </div>,
    )
    fireEvent.click(screen.getByRole("button", { name: "What is Dependencies?" }))
    expect(parent).not.toHaveBeenCalled()
  })
})
