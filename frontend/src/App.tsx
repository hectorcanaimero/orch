import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ReactQueryDevtools } from "@tanstack/react-query-devtools"
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom"
import { AppLayout } from "@/components/AppLayout"
import { ProtectedRoute } from "@/components/ProtectedRoute"
import { ArchitecturePage } from "@/pages/ArchitecturePage"
import { BoardPage } from "@/pages/BoardPage"
import { DoctorPage } from "@/pages/DoctorPage"
import { GraphPage } from "@/pages/GraphPage"
import { KanbanPage } from "@/pages/KanbanPage"
import { ListPage } from "@/pages/ListPage"
import { LoginPage } from "@/pages/LoginPage"
import { LogsPage } from "@/pages/LogsPage"
import { MetricsPage } from "@/pages/MetricsPage"
import { DocumentsPage } from "@/pages/DocumentsPage"
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

// Router basename is `/` (dashboard root) in both dev and prod after we
// removed the legacy Jinja UI and moved the SPA from `/spa/` to `/`.
// Passing no basename lets React Router use `/` implicitly.

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
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
            path="/docs"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <DocumentsPage />
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
          <Route
            path="/graph"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <GraphPage />
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
