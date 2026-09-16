import { Navigate } from "react-router-dom"
import { Skeleton } from "@/components/ui/skeleton"
import { useWhoami } from "@/hooks/useWhoami"
import type { ReactNode } from "react"

interface ProtectedRouteProps {
  children: ReactNode
}

/**
 * Bug 26 (Python line, shared by both dashboards — one compiled `web/`
 * bundle serves them, see docs/brainstorm/go-migration-notes/sonnet-2.md):
 * this used to gate on `useAuth().isAuthenticated`, which is
 * `Boolean(localStorage.orch_token)` and never asks the server anything.
 * An `operator` dashboard needs no token at all — every request succeeds
 * with none — so a browser with nothing saved (the normal case for an
 * operator on their own machine) was bounced to the token form anyway,
 * for a server that was never going to ask for one.
 *
 * Fixed by asking the one question that actually matters: does
 * `/api/whoami` succeed? The apiClient interceptor already attaches
 * whatever token is in localStorage (if any) to that request, so this
 * one check covers both real cases —
 *
 *   - operator, no token needed: whoami succeeds with none attached.
 *   - stakeholder, a valid token already saved: whoami succeeds with it
 *     attached.
 *
 * — and the one case that should still show the form: no valid token
 * for a profile that requires one, where whoami fails (401, or any
 * other non-success — see the doc comment below on why those aren't
 * told apart here).
 */
export function ProtectedRoute({ children }: ProtectedRouteProps) {
  const { data, isLoading } = useWhoami()

  // Neither "let them in" nor "show the form" yet — whoami's answer
  // (including whether the request even needs a token) isn't in. A
  // blank shell for one request's round trip beats guessing either way
  // and correcting a beat later.
  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Skeleton className="h-8 w-40" />
      </div>
    )
  }

  // Any failure — 401 (no/wrong token, profile requires one), or
  // anything else after useWhoami's own retries are exhausted — means
  // "show the form." Not distinguishing the failure reason here is
  // deliberate: this is the actual access gate, unlike AppLayout's use
  // of the same hook to decide which nav items to show once already
  // past it, where failing open (assume operator) is the safer default
  // because nothing here is actually protected by that decision. Here,
  // the safer default is the opposite — an ambiguous answer must not
  // render protected content.
  if (!data) {
    return <Navigate to="/login" replace />
  }

  return <>{children}</>
}
