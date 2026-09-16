import {
  Activity,
  BarChart3,
  BriefcaseBusiness,
  CalendarClock,
  FileText,
  GitFork,
  KanbanSquare,
  LayoutGrid,
  ListIcon,
  Milestone,
  Radio,
  Send,
  Wallet,
  Workflow,
} from "lucide-react"
import type { MessageKey } from "@/i18n"

export interface NavTab {
  to: string
  label: MessageKey
  // The server route the page reads its data from, by the name the Go
  // dashboard registers it under. A stakeholder sees a page only when this
  // route is on the allow-list /api/whoami returns, so the navigation and the
  // gate share one rule instead of two copies that drift apart.
  route: string
  // Used only when a server sends no allow-list (an older binary).
  operatorOnly?: boolean
  // G8.5: hidden whenever the dashboard wasn't started with --portfolio.
  portfolioGated?: boolean
}

export interface NavDestination {
  id: "now" | "work" | "cost" | "delivery"
  label: MessageKey
  icon: typeof Activity
  tabs: NavTab[]
}

/**
 * Four places, each answering one question: what is happening now, where the
 * work stands, what it costs, and what has been delivered. Every page is a tab
 * of exactly one of them.
 */
export const DESTINATIONS: NavDestination[] = [
  {
    id: "now",
    label: "nav.now",
    icon: Activity,
    tabs: [
      { to: "/", label: "nav.now", route: "api_now", operatorOnly: true },
      { to: "/now/portfolio", label: "nav.portfolio", route: "api_portfolio", operatorOnly: true, portfolioGated: true },
    ],
  },
  {
    id: "work",
    label: "nav.work",
    icon: BriefcaseBusiness,
    tabs: [
      { to: "/work/list", label: "nav.list", route: "api_tasks" },
      { to: "/work/kanban", label: "nav.kanban", route: "api_tasks" },
      { to: "/work/graph", label: "nav.graph", route: "api_graph", operatorOnly: true },
      { to: "/work/phases", label: "nav.phases", route: "api_milestones" },
      { to: "/work/pace", label: "nav.pace", route: "api_sprint" },
    ],
  },
  {
    id: "cost",
    label: "nav.cost",
    icon: Wallet,
    tabs: [
      { to: "/cost/budget", label: "nav.budget", route: "api_budget_summary" },
      { to: "/cost/metrics", label: "nav.metrics", route: "api_metrics", operatorOnly: true },
    ],
  },
  {
    id: "delivery",
    label: "nav.delivery",
    icon: Send,
    tabs: [
      { to: "/delivery/summary", label: "nav.summary", route: "stakeholder_summary_json" },
      { to: "/delivery/ci", label: "nav.ci", route: "api_ci", operatorOnly: true },
      { to: "/delivery/share", label: "nav.share", route: "api_tunnel_status", operatorOnly: true },
    ],
  },
]

/** Icons for tabs, keyed by path; the destination icon stands for the group. */
export const TAB_ICONS: Record<string, typeof Activity> = {
  "/": Activity,
  "/now/portfolio": LayoutGrid,
  "/work/list": ListIcon,
  "/work/kanban": KanbanSquare,
  "/work/graph": GitFork,
  "/work/phases": Milestone,
  "/work/pace": CalendarClock,
  "/cost/budget": Wallet,
  "/cost/metrics": BarChart3,
  "/delivery/summary": FileText,
  "/delivery/ci": Workflow,
  "/delivery/share": Radio,
}

/**
 * Every address the dashboard answered before it had four destinations, and
 * where it lives now. Bookmarks and shared links keep working.
 */
export const LEGACY_REDIRECTS: Record<string, string> = {
  "/list": "/work/list",
  "/kanban": "/work/kanban",
  "/graph": "/work/graph",
  "/milestones": "/work/phases",
  "/pace": "/work/pace",
  "/sprint": "/work/pace",
  "/budget": "/cost/budget",
  "/metrics": "/cost/metrics",
  "/ci": "/delivery/ci",
  "/tunnel": "/delivery/share",
  "/portfolio": "/now/portfolio",
  "/summary": "/delivery/summary",
}

interface Access {
  isStakeholder: boolean
  // /api/whoami's `routes`: present only for a stakeholder session.
  allowedRoutes?: string[]
}

function maySee(tab: NavTab, { isStakeholder, allowedRoutes, portfolioAvailable }: Access & { portfolioAvailable: boolean }) {
  if (isStakeholder) return allowedRoutes ? allowedRoutes.includes(tab.route) : !tab.operatorOnly
  return !tab.portfolioGated || portfolioAvailable
}

/** The destinations this session may see, each holding only its visible tabs. */
export function visibleDestinations(access: Access & { portfolioAvailable: boolean }): NavDestination[] {
  return DESTINATIONS.map((d) => ({ ...d, tabs: d.tabs.filter((tab) => maySee(tab, access)) })).filter(
    (d) => d.tabs.length > 0,
  )
}

/** The tab a path belongs to: an exact match, or the tab whose path it extends. */
export function findTab(pathname: string): { destination: NavDestination; tab: NavTab } | undefined {
  for (const destination of DESTINATIONS) {
    for (const tab of destination.tabs) {
      if (tab.to === "/" ? pathname === "/" : pathname === tab.to || pathname.startsWith(`${tab.to}/`)) {
        return { destination, tab }
      }
    }
  }
  return undefined
}

/** Where a session lands: its first visible page. */
export function homePath(destinations: NavDestination[]): string {
  return destinations[0]?.tabs[0]?.to ?? "/login"
}

// isPathAllowed answers whether a page opened by URL should render, or send
// a stakeholder to their home page instead of a 403.
export function isPathAllowed(pathname: string, { isStakeholder, allowedRoutes }: Access): boolean {
  if (!isStakeholder) return true
  const found = findTab(pathname)
  if (!found) return true
  return allowedRoutes ? allowedRoutes.includes(found.tab.route) : !found.tab.operatorOnly
}
