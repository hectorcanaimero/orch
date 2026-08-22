import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ReactQueryDevtools } from "@tanstack/react-query-devtools"
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom"
import { AppLayout } from "@/components/AppLayout"
import { ProtectedRoute } from "@/components/ProtectedRoute"
import { ArchitecturePage } from "@/pages/ArchitecturePage"
import { BoardPage } from "@/pages/BoardPage"
import { DoctorPage } from "@/pages/DoctorPage"
import { KanbanPage } from "@/pages/KanbanPage"
import { ListPage } from "@/pages/ListPage"
import { LoginPage } from "@/pages/LoginPage"
import { LogsPage } from "@/pages/LogsPage"
import { MetricsPage } from "@/pages/MetricsPage"
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

// Derive the router basename from whatever Vite decided at build time:
//   - dev  (base = "/")     → basename = ""     (React Router wants no trailing slash)
//   - prod (base = "/spa/") → basename = "/spa"
// Keeping this in one place avoids drift between vite.config.ts and the router.
const routerBasename = import.meta.env.BASE_URL.replace(/\/$/, "")

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={routerBasename}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route
            path="/"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <StakeholderSummaryPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/kanban"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <KanbanPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/list"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <ListPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/board"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <BoardPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/metrics"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <MetricsPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/logs"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <LogsPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/architecture"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <ArchitecturePage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/doctor"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <DoctorPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route
            path="/tunnel"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <TunnelPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
      <ReactQueryDevtools initialIsOpen={false} />
    </QueryClientProvider>
  )
}
