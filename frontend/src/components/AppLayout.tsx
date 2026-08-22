import { NavLink, useNavigate } from "react-router-dom"
import {
  BarChart3,
  ChevronLeft,
  ChevronRight,
  KanbanSquare,
  LayoutDashboard,
  ListIcon,
  LogOut,
  Network,
  PenTool,
  ScrollText,
  Stethoscope,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { useAuth } from "@/hooks/useAuth"
import { useSidebarCollapsed } from "@/hooks/useSidebarCollapsed"
import { cn } from "@/lib/utils"
import type { ReactNode } from "react"

interface AppLayoutProps {
  children: ReactNode
}

interface NavItem {
  to: string
  label: string
  icon: typeof LayoutDashboard
  end?: boolean
}

const NAV_ITEMS: NavItem[] = [
  { to: "/", label: "Summary", icon: LayoutDashboard, end: true },
  { to: "/list", label: "List", icon: ListIcon },
  { to: "/kanban", label: "Kanban", icon: KanbanSquare },
  { to: "/board", label: "Board", icon: PenTool },
  { to: "/architecture", label: "Architecture", icon: Network },
  { to: "/doctor", label: "Doctor", icon: Stethoscope },
  { to: "/metrics", label: "Metrics", icon: BarChart3 },
  { to: "/logs", label: "Logs", icon: ScrollText },
]

export function AppLayout({ children }: AppLayoutProps) {
  const { clearToken } = useAuth()
  const navigate = useNavigate()
  const [collapsed, , toggle] = useSidebarCollapsed()

  const handleLogout = () => {
    clearToken()
    navigate("/login", { replace: true })
  }

  return (
    <div className="flex min-h-screen bg-background">
      <aside
        className={cn(
          "fixed left-0 top-0 z-30 flex h-screen flex-col border-r border-zinc-800 bg-zinc-950 text-zinc-100",
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
            <div
              className="flex h-9 w-9 items-center justify-center rounded-md bg-zinc-800 text-sm font-semibold tracking-tight text-white"
              aria-label="Orch"
            >
              O
            </div>
          ) : (
            <div>
              <div className="text-lg font-semibold tracking-tight">Orch</div>
              <div className="text-xs text-zinc-400">Dashboard</div>
            </div>
          )}
        </div>

        {/* Nav */}
        <nav
          className={cn(
            "flex-1 space-y-1",
            collapsed ? "px-2" : "px-3",
          )}
        >
          {NAV_ITEMS.map((item) => {
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
          "flex-1 transition-[margin-left] duration-200 ease-out",
          collapsed ? "ml-16" : "ml-60",
        )}
      >
        <div className="px-6 py-8 lg:px-8">{children}</div>
      </main>
    </div>
  )
}
