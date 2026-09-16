import type { ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ReactQueryDevtools } from "@tanstack/react-query-devtools"
import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom"
import { AppLayout } from "@/components/AppLayout"
import { ProtectedRoute } from "@/components/ProtectedRoute"
import { LEGACY_REDIRECTS } from "@/lib/nav"
import { BudgetPage } from "@/pages/BudgetPage"
import { CIPage } from "@/pages/CIPage"
import { GraphPage } from "@/pages/GraphPage"
import { KanbanPage } from "@/pages/KanbanPage"
import { ListPage } from "@/pages/ListPage"
import { LoginPage } from "@/pages/LoginPage"
import { MetricsPage } from "@/pages/MetricsPage"
import { MilestonesPage } from "@/pages/MilestonesPage"
import { NowPage } from "@/pages/NowPage"
import { PacePage } from "@/pages/PacePage"
import { PortfolioPage } from "@/pages/PortfolioPage"
import { StakeholderSummaryPage } from "@/pages/StakeholderSummaryPage"
import { TunnelPage } from "@/pages/TunnelPage"

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
    },
  },
})

// One element per tab path in lib/nav.ts DESTINATIONS.
const PAGES: Record<string, ReactNode> = {
  "/": <NowPage />,
  "/now/portfolio": <PortfolioPage />,
  "/work/list": <ListPage />,
  "/work/kanban": <KanbanPage />,
  "/work/graph": <GraphPage />,
  "/work/phases": <MilestonesPage />,
  "/work/pace": <PacePage />,
  "/cost/budget": <BudgetPage />,
  "/cost/metrics": <MetricsPage />,
  "/delivery/summary": <StakeholderSummaryPage />,
  "/delivery/ci": <CIPage />,
  "/delivery/share": <TunnelPage />,
}

/** A redirect that keeps the query string, so filters and `?token=` survive it. */
function Redirect({ to }: { to: string }) {
  const { search, hash } = useLocation()
  const [path, query] = to.split("?")
  const merged = new URLSearchParams(search)
  new URLSearchParams(query).forEach((v, k) => merged.set(k, v))
  const qs = merged.toString()
  return <Navigate to={`${path}${qs ? `?${qs}` : ""}${hash}`} replace />
}

function Shell() {
  return (
    <ProtectedRoute>
      <AppLayout>
        <Outlet />
      </AppLayout>
    </ProtectedRoute>
  )
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<Shell />}>
        {Object.entries(PAGES).map(([path, element]) => (
          <Route key={path} path={path} element={element} />
        ))}
      </Route>
      {Object.entries(LEGACY_REDIRECTS).map(([from, to]) => (
        <Route key={from} path={from} element={<Redirect to={to} />} />
      ))}
      {/* A destination's own address opens its first page; logs open over any page. */}
      <Route path="/now" element={<Redirect to="/" />} />
      <Route path="/work" element={<Redirect to="/work/list" />} />
      <Route path="/cost" element={<Redirect to="/cost/budget" />} />
      <Route path="/delivery" element={<Redirect to="/delivery/summary" />} />
      <Route path="/logs" element={<Redirect to="/?logs=1" />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

// Router basename is `/` (dashboard root) in both dev and prod after we
// removed the legacy Jinja UI and moved the SPA from `/spa/` to `/`.
// Passing no basename lets React Router use `/` implicitly.

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AppRoutes />
      </BrowserRouter>
      <ReactQueryDevtools initialIsOpen={false} />
    </QueryClientProvider>
  )
}
