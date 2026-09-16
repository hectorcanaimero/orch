import { useEffect, useState, type ReactNode } from "react"
import { Navigate, NavLink, useLocation, useNavigate, useSearchParams } from "react-router-dom"
import { ChevronLeft, ChevronRight, LogOut, Monitor, Moon, ScrollText, Search, Sun, X } from "lucide-react"
import { CommandPalette, ShortcutsDialog } from "@/components/CommandPalette"
import { Tooltip } from "@/components/ui/tooltip"
import { t } from "@/i18n"
import { useAuth } from "@/hooks/useAuth"
import { isPortfolioNavVisible, usePortfolio } from "@/hooks/usePortfolio"
import { useSidebarCollapsed } from "@/hooks/useSidebarCollapsed"
import { useWhoami } from "@/hooks/useWhoami"
import { TAB_ICONS, findTab, homePath, isPathAllowed, visibleDestinations, type NavDestination } from "@/lib/nav"
import { isMac, useKeyboardShortcuts } from "@/lib/shortcuts"
import { nextTheme, useTheme } from "@/lib/theme"
import { cn } from "@/lib/utils"
import { LogsPage } from "@/pages/LogsPage"

interface AppLayoutProps {
  children: ReactNode
}

const THEME_ICON = { dark: Moon, light: Sun, system: Monitor } as const
const BOTTOM_COLS: Record<number, string> = { 1: "grid-cols-1", 2: "grid-cols-2", 3: "grid-cols-3", 4: "grid-cols-4" }

const focusRing = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const sideButton = cn(
  "flex h-9 w-full items-center rounded-md text-sm text-sidebar-muted transition-colors hover:bg-sidebar-active hover:text-sidebar-foreground",
  focusRing,
)

function ThemeToggle({ compact }: { compact: boolean }) {
  const [choice, setChoice] = useTheme()
  const next = nextTheme(choice)
  const Icon = THEME_ICON[choice]
  const label = t("theme.current", { theme: t(`theme.${choice}` as const) })
  const button = (
    <button
      type="button"
      onClick={() => setChoice(next)}
      aria-label={`${label}. ${t("theme.switch_to", { theme: t(`theme.${next}` as const) })}`}
      className={cn(sideButton, compact ? "justify-center" : "gap-2 px-3")}
    >
      <Icon className="h-4 w-4 shrink-0" aria-hidden />
      {compact ? null : <span>{t(`theme.${choice}` as const)}</span>}
    </button>
  )
  return compact ? <Tooltip content={label}>{button}</Tooltip> : button
}

/** The four destinations. Each link opens the destination's first page this session may see. */
function DestinationLinks({ destinations, currentId, collapsed }: { destinations: NavDestination[]; currentId?: string; collapsed: boolean }) {
  return (
    <>
      {destinations.map((d) => {
        const Icon = d.icon
        const active = d.id === currentId
        const link = (
          <NavLink
            key={d.id}
            to={d.tabs[0].to}
            aria-current={active ? "page" : undefined}
            aria-label={collapsed ? t(d.label) : undefined}
            className={cn(
              "relative flex h-9 items-center rounded-md text-sm transition-colors",
              focusRing,
              collapsed ? "w-full justify-center" : "gap-2.5 px-3",
              active
                ? "bg-sidebar-active font-medium text-sidebar-foreground"
                : "text-sidebar-muted hover:bg-sidebar-active/60 hover:text-sidebar-foreground",
            )}
          >
            <Icon className={cn("h-4 w-4 shrink-0", active && "text-brand")} aria-hidden />
            {collapsed ? null : <span>{t(d.label)}</span>}
          </NavLink>
        )
        return collapsed ? (
          <Tooltip key={d.id} content={t(d.label)}>
            {link}
          </Tooltip>
        ) : (
          link
        )
      })}
    </>
  )
}

/** The pages inside the current destination, when it has more than one. */
function SectionTabs({ destination, pathname }: { destination: NavDestination; pathname: string }) {
  const { search } = useLocation()
  if (destination.tabs.length < 2) return null
  // Filters live in the query string; moving between List, Kanban and Graph keeps them.
  const carry = new URLSearchParams(search)
  carry.delete("logs")
  const qs = carry.toString()
  return (
    <nav aria-label={t("nav.sections")} className="-mx-1 mb-6 overflow-x-auto">
      <div className="inline-flex min-w-max gap-1 border-b px-1">
        {destination.tabs.map((tab) => {
          const Icon = TAB_ICONS[tab.to]
          const active = findTab(pathname)?.tab.to === tab.to
          return (
            <NavLink
              key={tab.to}
              to={`${tab.to}${qs ? `?${qs}` : ""}`}
              aria-current={active ? "page" : undefined}
              className={cn(
                "-mb-px inline-flex h-10 items-center gap-2 border-b-2 px-2.5 text-sm transition-colors sm:px-3",
                focusRing,
                active
                  ? "border-brand font-medium text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {Icon ? <Icon className="hidden h-4 w-4 shrink-0 sm:block" aria-hidden /> : null}
              {t(tab.label)}
            </NavLink>
          )
        })}
      </div>
    </nav>
  )
}

/**
 * The event log as a panel over whatever page is open, so following a run
 * never means leaving the page that raised the question. `?logs=1` opens it,
 * which is also what the old /logs address redirects to.
 */
function LogsDrawer({ onClose, collapsed }: { onClose: () => void; collapsed: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])
  return (
    <section
      aria-label={t("nav.logs")}
      className={cn(
        "fixed inset-x-0 bottom-16 z-40 h-[62vh] border-t bg-background shadow-[0_-12px_32px_-12px_rgb(0_0_0/0.35)] md:bottom-0",
        collapsed ? "md:left-16" : "md:left-60",
      )}
    >
      <button
        type="button"
        onClick={onClose}
        aria-label={t("nav.close_logs")}
        className={cn(
          "absolute right-3 top-3 z-10 flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground",
          focusRing,
        )}
      >
        <X className="h-4 w-4" aria-hidden />
      </button>
      {/* LogsPage sizes itself for a full page; inside the panel it fills the panel instead. */}
      <div className="h-full pb-4 pl-4 pr-14 pt-4 md:pl-6 [&>div]:h-full">
        <LogsPage />
      </div>
    </section>
  )
}

export function AppLayout({ children }: AppLayoutProps) {
  const { clearToken } = useAuth()
  const navigate = useNavigate()
  const [collapsed, , toggle] = useSidebarCollapsed()
  const { data: whoami } = useWhoami()
  const { pathname } = useLocation()
  const [params, setParams] = useSearchParams()
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [shortcutsOpen, setShortcutsOpen] = useState(false)

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

  const destinations = visibleDestinations({ isStakeholder, allowedRoutes, portfolioAvailable })
  const currentId = findTab(pathname)?.destination.id
  const current = destinations.find((d) => d.id === currentId)
  const canSeeLogs = !isStakeholder || (allowedRoutes ? allowedRoutes.includes("api_events") : false)
  const logsOpen = canSeeLogs && params.get("logs") === "1"

  const setLogs = (open: boolean) => {
    const next = new URLSearchParams(params)
    if (open) next.set("logs", "1")
    else next.delete("logs")
    setParams(next, { replace: true })
  }

  useKeyboardShortcuts({
    destinations,
    onPalette: () => setPaletteOpen((open) => !open),
    onShortcuts: () => setShortcutsOpen(true),
  })

  // A page opened by URL that the server would refuse sends the reader to
  // their first allowed page rather than to a 403.
  if (!isPathAllowed(pathname, { isStakeholder, allowedRoutes })) {
    return <Navigate to={homePath(destinations)} replace />
  }

  const logsButton = canSeeLogs ? (
    <button
      type="button"
      onClick={() => setLogs(!logsOpen)}
      aria-pressed={logsOpen}
      aria-label={collapsed ? t("nav.logs") : undefined}
      className={cn(sideButton, collapsed ? "justify-center" : "gap-2 px-3", logsOpen && "bg-sidebar-active text-sidebar-foreground")}
    >
      <ScrollText className="h-4 w-4 shrink-0" aria-hidden />
      {collapsed ? null : <span>{t("nav.logs")}</span>}
    </button>
  ) : null

  const searchKeys = isMac() ? "⌘K" : "Ctrl K"
  const searchButton = (
    <button
      type="button"
      onClick={() => setPaletteOpen(true)}
      aria-label={collapsed ? t("palette.open") : undefined}
      aria-keyshortcuts={isMac() ? "Meta+K" : "Control+K"}
      className={cn(sideButton, "border border-sidebar-active", collapsed ? "justify-center" : "gap-2 px-3")}
    >
      <Search className="h-4 w-4 shrink-0" aria-hidden />
      {collapsed ? null : (
        <>
          <span className="flex-1 text-left">{t("palette.open")}</span>
          <kbd className="font-mono text-[11px] text-sidebar-muted">{searchKeys}</kbd>
        </>
      )}
    </button>
  )

  return (
    <div className="min-h-screen bg-background md:flex">
      {/* Small screens: a slim top bar and, at the bottom, the same four destinations. */}
      <header className="sticky top-0 z-30 border-b bg-sidebar text-sidebar-foreground md:hidden">
        <div className="flex h-14 items-center justify-between gap-2 px-4">
          <div className="flex min-w-0 items-center gap-2.5">
            <img src="/favicon.svg" alt="" className="h-6 w-6 shrink-0" />
            <span className="truncate text-[15px] font-semibold">{current ? t(current.label) : "orch"}</span>
          </div>
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => setPaletteOpen(true)}
              aria-label={t("palette.open")}
              className={cn("flex h-10 w-10 items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground", focusRing)}
            >
              <Search className="h-4 w-4" aria-hidden />
            </button>
            {canSeeLogs ? (
              <button
                type="button"
                onClick={() => setLogs(!logsOpen)}
                aria-pressed={logsOpen}
                aria-label={t("nav.logs")}
                className={cn("flex h-10 w-10 items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground", focusRing)}
              >
                <ScrollText className="h-4 w-4" aria-hidden />
              </button>
            ) : null}
            <div className="w-10">
              <ThemeToggle compact />
            </div>
            <button
              type="button"
              aria-label={t("nav.logout")}
              className={cn("flex h-10 w-10 items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground", focusRing)}
              onClick={handleLogout}
            >
              <LogOut className="h-4 w-4" aria-hidden />
            </button>
          </div>
        </div>
      </header>

      <nav
        aria-label={t("nav.main") + " (small screens)"}
        className={cn(
          "fixed inset-x-0 bottom-0 z-40 grid h-16 border-t bg-sidebar pb-[env(safe-area-inset-bottom)] text-sidebar-foreground md:hidden",
          BOTTOM_COLS[destinations.length] ?? "grid-cols-4",
        )}
      >
        {destinations.map((d) => {
          const Icon = d.icon
          const active = d.id === currentId
          return (
            <NavLink
              key={d.id}
              to={d.tabs[0].to}
              aria-current={active ? "page" : undefined}
              className={cn(
                "flex flex-col items-center justify-center gap-1 text-[11px] font-medium",
                focusRing,
                active ? "text-sidebar-foreground" : "text-sidebar-muted",
              )}
            >
              <Icon className={cn("h-5 w-5", active && "text-brand")} aria-hidden />
              {t(d.label)}
            </NavLink>
          )
        })}
      </nav>

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
              className={cn("flex h-7 w-7 items-center justify-center rounded-md text-sidebar-muted hover:bg-sidebar-active hover:text-sidebar-foreground", focusRing)}
            >
              <ChevronLeft className="h-4 w-4" aria-hidden />
            </button>
          )}
        </div>

        <div className="px-3 pb-3">
          {collapsed ? (
            <Tooltip content={`${t("palette.open")} (${searchKeys})`}>{searchButton}</Tooltip>
          ) : (
            searchButton
          )}
        </div>

        <nav aria-label={t("nav.main")} className="flex-1 space-y-0.5 overflow-y-auto px-3">
          <DestinationLinks destinations={destinations} currentId={currentId} collapsed={collapsed} />
        </nav>

        <div className="space-y-0.5 border-t p-3">
          {collapsed ? (
            <Tooltip content={t("nav.expand")}>
              <button type="button" onClick={toggle} aria-label={t("nav.expand")} aria-expanded={false} className={cn(sideButton, "justify-center")}>
                <ChevronRight className="h-4 w-4" aria-hidden />
              </button>
            </Tooltip>
          ) : null}
          {collapsed && logsButton ? <Tooltip content={t("nav.logs")}>{logsButton}</Tooltip> : logsButton}
          <ThemeToggle compact={collapsed} />
          {collapsed ? (
            <Tooltip content={t("nav.logout")}>
              <button type="button" aria-label={t("nav.logout")} onClick={handleLogout} className={cn(sideButton, "justify-center")}>
                <LogOut className="h-4 w-4" aria-hidden />
              </button>
            </Tooltip>
          ) : (
            <button type="button" onClick={handleLogout} className={cn(sideButton, "gap-2 px-3")}>
              <LogOut className="h-4 w-4 shrink-0" aria-hidden />
              <span>{t("nav.logout")}</span>
            </button>
          )}
        </div>
      </aside>

      <main
        className={cn(
          "min-w-0 flex-1 pb-20 transition-[margin-left] duration-200 ease-out md:pb-0",
          collapsed ? "md:ml-16" : "md:ml-60",
          logsOpen && "pb-[calc(62vh+5rem)] md:pb-[62vh]",
        )}
      >
        <div className="mx-auto max-w-[1600px] px-4 py-6 md:px-6 md:py-8 lg:px-10">
          {current ? <SectionTabs destination={current} pathname={pathname} /> : null}
          {children}
        </div>
      </main>

      {logsOpen ? <LogsDrawer onClose={() => setLogs(false)} collapsed={collapsed} /> : null}

      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        destinations={destinations}
        isStakeholder={isStakeholder}
        canSeeLogs={canSeeLogs}
        onOpenLogs={() => setLogs(true)}
        onShowShortcuts={() => setShortcutsOpen(true)}
      />
      <ShortcutsDialog open={shortcutsOpen} onOpenChange={setShortcutsOpen} destinations={destinations} />
    </div>
  )
}
