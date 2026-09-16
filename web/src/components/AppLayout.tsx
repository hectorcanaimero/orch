import { useState, type ReactNode } from "react"
import { Navigate, NavLink, useLocation, useNavigate } from "react-router-dom"
import { ChevronLeft, ChevronRight, LogOut, Menu, Monitor, Moon, Sun } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Tooltip } from "@/components/ui/tooltip"
import { t } from "@/i18n"
import { useAuth } from "@/hooks/useAuth"
import { isPortfolioNavVisible, usePortfolio } from "@/hooks/usePortfolio"
import { useSidebarCollapsed } from "@/hooks/useSidebarCollapsed"
import { useWhoami } from "@/hooks/useWhoami"
import { isPathAllowed, visibleNavItems, type NavItem } from "@/lib/nav"
import { useTheme, type ThemeChoice } from "@/lib/theme"
import { cn } from "@/lib/utils"

interface AppLayoutProps {
  children: ReactNode
}

const THEME_ORDER: ThemeChoice[] = ["dark", "light", "system"]
const THEME_ICON = { dark: Moon, light: Sun, system: Monitor } as const

function ThemeToggle({ compact }: { compact: boolean }) {
  const [choice, setChoice] = useTheme()
  const next = THEME_ORDER[(THEME_ORDER.indexOf(choice) + 1) % THEME_ORDER.length]
  const Icon = THEME_ICON[choice]
  const label = t("theme.current", { theme: t(`theme.${choice}` as const) })
  const button = (
    <button
      type="button"
      onClick={() => setChoice(next)}
      aria-label={`${label}. ${t("theme.switch_to", { theme: t(`theme.${next}` as const) })}`}
      className={cn(
        "flex h-9 items-center gap-2 rounded-md text-sm text-sidebar-muted transition-colors hover:bg-sidebar-active hover:text-sidebar-foreground",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        compact ? "w-full justify-center" : "w-full px-3",
      )}
    >
      <Icon className="h-4 w-4 shrink-0" aria-hidden />
      {compact ? null : <span>{t(`theme.${choice}` as const)}</span>}
    </button>
  )
  return compact ? <Tooltip content={label}>{button}</Tooltip> : button
}

function NavLinks({ items, collapsed, onNavigate }: { items: NavItem[]; collapsed?: boolean; onNavigate?: () => void }) {
  return (
    <>
      {items.map((item) => {
        const Icon = item.icon
        const link = (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.end}
            onClick={onNavigate}
            aria-label={collapsed ? item.label : undefined}
            className={({ isActive }) =>
              cn(
                "relative flex items-center rounded-md text-sm transition-colors",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                collapsed ? "h-9 w-full justify-center" : "h-9 gap-2.5 px-3",
                isActive
                  ? "bg-sidebar-active font-medium text-sidebar-foreground"
                  : "text-sidebar-muted hover:bg-sidebar-active/60 hover:text-sidebar-foreground",
              )
            }
          >
            {({ isActive }) => (
              <>
                <Icon className={cn("h-4 w-4 shrink-0", isActive && "text-brand")} aria-hidden />
                {collapsed ? null : <span>{item.label}</span>}
              </>
            )}
          </NavLink>
        )
        return collapsed ? (
          <Tooltip key={item.to} content={item.label}>
            {link}
          </Tooltip>
        ) : (
          link
        )
      })}
    </>
  )
}

export function AppLayout({ children }: AppLayoutProps) {
  const { clearToken } = useAuth()
  const navigate = useNavigate()
  const [collapsed, , toggle] = useSidebarCollapsed()
  const { data: whoami } = useWhoami()
  const { pathname } = useLocation()
  const [menuOpen, setMenuOpen] = useState(false)

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
  const current = visibleNav.find((item) => (item.end ? pathname === item.to : pathname.startsWith(item.to)))

  // A page opened by URL that the server would refuse sends the reader to
  // the summary rather than to a 403.
  if (!isPathAllowed(pathname, { isStakeholder, allowedRoutes })) {
    return <Navigate to="/" replace />
  }

  return (
    <div className="min-h-screen bg-background md:flex">
      {/* Small screens: a top bar naming the current page, with every page one
          tap away in a menu — never a strip of labels cut off at the edge. */}
      <header className="sticky top-0 z-30 border-b bg-sidebar text-sidebar-foreground md:hidden">
        <div className="flex h-14 items-center justify-between gap-2 px-4">
          <Popover open={menuOpen} onOpenChange={setMenuOpen}>
            <PopoverTrigger asChild>
              <button
                type="button"
                aria-label={t("nav.menu")}
                className="-ml-2 flex h-10 items-center gap-2 rounded-md px-2 text-sm font-medium hover:bg-sidebar-active focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <Menu className="h-5 w-5" aria-hidden />
                <img src="/favicon.svg" alt="" className="h-6 w-6 shrink-0" />
                <span>{current?.label ?? "Orch"}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent align="start" className="w-64 bg-sidebar p-2">
              <nav aria-label="Main (small screens)" className="grid gap-0.5">
                <NavLinks items={visibleNav} onNavigate={() => setMenuOpen(false)} />
              </nav>
              <div className="mt-2 border-t pt-2">
                <ThemeToggle compact={false} />
              </div>
            </PopoverContent>
          </Popover>
          <button
            type="button"
            aria-label={t("nav.logout")}
            className="flex h-10 w-10 items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={handleLogout}
          >
            <LogOut className="h-4 w-4" aria-hidden />
          </button>
        </div>
      </header>

      <aside
        className={cn(
          "fixed left-0 top-0 z-30 hidden h-screen flex-col border-r bg-sidebar text-sidebar-foreground md:flex",
          "transition-[width] duration-200 ease-out",
          collapsed ? "w-16" : "w-60",
        )}
      >
        <div className={cn("flex h-16 items-center", collapsed ? "justify-center" : "justify-between pl-5 pr-3")}>
          <div className="flex items-center gap-2.5">
            <img src="/favicon.svg" alt="Orch" className="h-7 w-7 shrink-0" />
            {collapsed ? null : <span className="text-[15px] font-semibold tracking-[-0.01em]">orch</span>}
          </div>
          {collapsed ? null : (
            <button
              type="button"
              onClick={toggle}
              aria-label={t("nav.collapse")}
              aria-expanded
              className="flex h-7 w-7 items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <ChevronLeft className="h-4 w-4" aria-hidden />
            </button>
          )}
        </div>

        <nav aria-label="Main" className={cn("flex-1 space-y-0.5 overflow-y-auto", collapsed ? "px-3" : "px-3")}>
          <NavLinks items={visibleNav} collapsed={collapsed} />
        </nav>

        <div className="space-y-0.5 border-t p-3">
          {collapsed ? (
            <Tooltip content={t("nav.expand")}>
              <button
                type="button"
                onClick={toggle}
                aria-label={t("nav.expand")}
                aria-expanded={false}
                className="flex h-9 w-full items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <ChevronRight className="h-4 w-4" aria-hidden />
              </button>
            </Tooltip>
          ) : null}
          <ThemeToggle compact={collapsed} />
          {collapsed ? (
            <Tooltip content={t("nav.logout")}>
              <button
                type="button"
                aria-label={t("nav.logout")}
                onClick={handleLogout}
                className="flex h-9 w-full items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <LogOut className="h-4 w-4" aria-hidden />
              </button>
            </Tooltip>
          ) : (
            <button
              type="button"
              onClick={handleLogout}
              className="flex h-9 w-full items-center gap-2 rounded-md px-3 text-sm text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <LogOut className="h-4 w-4 shrink-0" aria-hidden />
              <span>{t("nav.logout")}</span>
            </button>
          )}
        </div>
      </aside>

      <main
        className={cn(
          "min-w-0 flex-1 transition-[margin-left] duration-200 ease-out",
          collapsed ? "md:ml-16" : "md:ml-60",
        )}
      >
        <div className="mx-auto max-w-[1600px] px-4 py-6 md:px-6 md:py-8 lg:px-10">{children}</div>
      </main>
    </div>
  )
}
