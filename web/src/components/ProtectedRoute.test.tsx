import "@testing-library/jest-dom/vitest"
import { render, screen } from "@testing-library/react"
import { MemoryRouter, Route, Routes } from "react-router-dom"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ProtectedRoute } from "@/components/ProtectedRoute"

// Bug 26: ProtectedRoute used to gate on whether *something* was saved in
// localStorage, never on what the server actually says. These tests drive
// it purely through useWhoami's three states — loading, success (whatever
// made /api/whoami succeed: no token needed, or a valid saved one), and
// failure (401, or anything else) — the same three states the real hook
// produces regardless of which of those reasons is behind them.
const mockUseWhoami = vi.fn()
vi.mock("@/hooks/useWhoami", () => ({
  useWhoami: () => mockUseWhoami(),
}))

function renderProtected() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route path="/login" element={<div>login form</div>} />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <div>protected content</div>
            </ProtectedRoute>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

describe("ProtectedRoute", () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it("renders neither the form nor the content while whoami is loading", () => {
    mockUseWhoami.mockReturnValue({ data: undefined, isLoading: true })

    renderProtected()

    expect(screen.queryByText("protected content")).not.toBeInTheDocument()
    expect(screen.queryByText("login form")).not.toBeInTheDocument()
  })

  // The bug itself: an operator dashboard needs no token, so whoami
  // succeeds with none attached — this must render the app, not bounce
  // to the login form the way `Boolean(localStorage.orch_token)` did.
  it("renders the protected content once whoami succeeds (operator, no token needed)", () => {
    mockUseWhoami.mockReturnValue({ data: { profile: "operator" }, isLoading: false })

    renderProtected()

    expect(screen.getByText("protected content")).toBeInTheDocument()
    expect(screen.queryByText("login form")).not.toBeInTheDocument()
  })

  // The other success case: a stakeholder session with a valid token
  // already in localStorage — apiClient's interceptor attached it,
  // whoami succeeded, same as above.
  it("renders the protected content when whoami succeeds with a saved stakeholder token", () => {
    mockUseWhoami.mockReturnValue({ data: { profile: "stakeholder" }, isLoading: false })

    renderProtected()

    expect(screen.getByText("protected content")).toBeInTheDocument()
  })

  it("redirects to /login when whoami fails (no data)", () => {
    mockUseWhoami.mockReturnValue({ data: undefined, isLoading: false })

    renderProtected()

    expect(screen.getByText("login form")).toBeInTheDocument()
    expect(screen.queryByText("protected content")).not.toBeInTheDocument()
  })
})
