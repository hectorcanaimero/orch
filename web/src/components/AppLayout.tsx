import { Navigate, NavLink, useLocation, useNavigate } from "react-router-dom"
import { ChevronLeft, ChevronRight, LogOut } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useAuth } from "@/hooks/useAuth"
import { isPortfolioNavVisible, usePortfolio } from "@/hooks/usePortfolio"
import { useSidebarCollapsed } from "@/hooks/useSidebarCollapsed"
import { useWhoami } from "@/hooks/useWhoami"
import { isPathAllowed, visibleNavItems } from "@/lib/nav"
import { cn } from "@/lib/utils"
import type { ReactNode } from "react"

interface AppLayoutProps {
  children: ReactNode
}

export function AppLayout({ children }: AppLayoutProps) {
  const { clearToken } = useAuth()
  const navigate = useNavigate()
  const [collapsed, , toggle] = useSidebarCollapsed()
  const { data: whoami } = useWhoami()
  const { pathname } = useLocation()

  const handleLogout = () => {
    clearToken()
    navigate("/login", { replace: true })
  }

  // Sprint E-6: fail-open. If /api/whoami hasn't answered yet (undefined) OR
  // errored out, assume operator so we never hide UI from an operator who
  // hit a transient hiccup. Stakeholder gating only kicks in on an EXPLICIT
  // "stakeholder" answer, and then follows the allow-list the server sent.
  const isStakeholder = whoami?.profile === "stakeholder"
  const allowedRoutes = whoami?.routes

  // G8.5: never even requested for a stakeholder session — portfolio data
  // (other projects' names, paths, spend) is operator-only.
  const { data: portfolioData, error: portfolioError } = usePortfolio({
    enabled: !isStakeholder,
  })
  const portfolioAvailable = isPortfolioNavVisible({
    isStakeholder,
    data: portfolioData,
    error: portfolioError,
  })

  const visibleNav = visibleNavItems({ isStakeholder, allowedRoutes, portfolioAvailable })

  // A page opened by URL that the server would refuse sends the reader to
  // the summary rather than to a 403.
  if (!isPathAllowed(pathname, { isStakeholder, allowedRoutes })) {
    return <Navigate to="/" replace />
  }

  return (
    <div className="min-h-screen bg-background md:flex">
      {/* Small screens: a top bar with the same pages, scrolling sideways on
          its own when they do not fit, so the page itself never does. */}
      <header className="sticky top-0 z-30 border-b border-zinc-800 bg-zinc-950 text-zinc-100 md:hidden">
        <div className="flex items-center justify-between px-4 py-2.5">
          <div className="flex items-center gap-2">
            <img src="/favicon.svg" alt="" className="h-7 w-7 shrink-0" />
            <span className="text-base font-semibold tracking-tight">Orch</span>
          </div>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Logout"
            className="h-10 w-10 text-zinc-300 hover:bg-zinc-900 hover:text-white"
            onClick={handleLogout}
          >
            <LogOut className="h-4 w-4" />
          </Button>
        </div>
        <nav aria-label="Main (small screens)" className="flex gap-1 overflow-x-auto px-3 pb-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {visibleNav.map((item) => {
            const Icon = item.icon
            return (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                className={({ isActive }) =>
                  cn(
                    "flex min-h-10 shrink-0 items-center gap-1.5 rounded-md px-3 text-sm transition-colors",
                    isActive ? "bg-zinc-800 text-white" : "text-zinc-300 hover:bg-zinc-900 hover:text-white",
                  )
                }
              >
                <Icon className="h-4 w-4 shrink-0" />
                <span>{item.label}</span>
              </NavLink>
            )
          })}
        </nav>
      </header>

      <aside
        className={cn(
          "fixed left-0 top-0 z-30 hidden h-screen flex-col border-r border-zinc-800 bg-zinc-950 text-zinc-100 md:flex",
          "transition-[width] duration-200 ease-out",
          collapsed ? "w-16" : "w-60",
        )}
      >
        {/* Toggle button — overlaps the sidebar's right edge */}
        <button
          type="button"
          onClick={toggle}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          aria-expanded={!collapsed}
          className={cn(
            "absolute -right-3 top-12 z-40 flex h-6 w-6 items-center justify-center",
            "rounded-full border border-zinc-700 bg-zinc-950 text-zinc-300",
            "shadow-sm shadow-black/40 transition-colors hover:bg-zinc-900 hover:text-white",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-zinc-400",
          )}
        >
          {collapsed ? (
            <ChevronRight className="h-3.5 w-3.5" />
          ) : (
            <ChevronLeft className="h-3.5 w-3.5" />
          )}
        </button>

        {/* Logo */}
        <div
          className={cn(
            "flex items-center py-5",
            collapsed ? "justify-center px-0" : "px-6",
          )}
        >
          {collapsed ? (
            <img
              src="/favicon.svg"
              alt="Orch"
              aria-label="Orch"
              className="h-9 w-9 shrink-0"
            />
          ) : (
            <div className="flex items-center gap-2.5">
              <img
                src="/favicon.svg"
                alt="Orch"
                className="h-8 w-8 shrink-0"
              />
              <div>
                <div className="text-lg font-semibold tracking-tight">Orch</div>
                <div className="text-xs text-zinc-400">Dashboard</div>
              </div>
            </div>
          )}
        </div>

        {/* Nav */}
        <nav
          aria-label="Main"
          className={cn(
            "flex-1 space-y-1",
            collapsed ? "px-2" : "px-3",
          )}
        >
          {visibleNav.map((item) => {
            const Icon = item.icon
            return (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                title={collapsed ? item.label : undefined}
                aria-label={collapsed ? item.label : undefined}
                className={({ isActive }) =>
                  cn(
                    "flex items-center rounded-md text-sm transition-colors",
                    collapsed
                      ? "h-10 w-full justify-center px-0"
                      : "gap-2 px-3 py-2",
                    isActive
                      ? "bg-zinc-800 text-white"
                      : "text-zinc-300 hover:bg-zinc-900 hover:text-white",
                  )
                }
              >
                <Icon className="h-4 w-4 shrink-0" />
                {collapsed ? null : <span>{item.label}</span>}
              </NavLink>
            )
          })}
        </nav>

        {/* Logout */}
        <div className={cn(collapsed ? "p-2" : "p-3")}>
          <Button
            variant="outline"
            title={collapsed ? "Logout" : undefined}
            aria-label={collapsed ? "Logout" : undefined}
            className={cn(
              "border-zinc-800 bg-transparent text-zinc-100 hover:bg-zinc-900 hover:text-white",
              collapsed
                ? "h-10 w-full justify-center gap-0 px-0"
                : "w-full justify-start gap-2",
            )}
            onClick={handleLogout}
          >
            <LogOut className="h-4 w-4 shrink-0" />
            {collapsed ? null : <span>Logout</span>}
          </Button>
        </div>
      </aside>

      <main
        className={cn(
          "min-w-0 flex-1 transition-[margin-left] duration-200 ease-out",
          collapsed ? "md:ml-16" : "md:ml-60",
        )}
      >
        <div className="px-4 py-6 md:px-6 md:py-8 lg:px-8">{children}</div>
      </main>
    </div>
  )
}
