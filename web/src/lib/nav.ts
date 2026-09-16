import {
  BarChart3,
  CalendarClock,
  GitFork,
  KanbanSquare,
  LayoutDashboard,
  LayoutGrid,
  ListIcon,
  Milestone,
  Radio,
  ScrollText,
  Wallet,
  Workflow,
} from "lucide-react"

export interface NavItem {
  to: string
  label: string
  icon: typeof LayoutDashboard
  end?: boolean
  // The server route the page reads its data from, by the name the Go
  // dashboard registers it under. A stakeholder sees a page only when this
  // route is on the allow-list /api/whoami returns, so the sidebar and the
  // gate share one rule instead of two copies that drift apart.
  route: string
  // Used only when a server sends no allow-list (an older binary).
  operatorOnly?: boolean
  // G8.5: hidden whenever the dashboard wasn't started with --portfolio.
  portfolioGated?: boolean
}

export const NAV_ITEMS: NavItem[] = [
  { to: "/", label: "Summary", icon: LayoutDashboard, end: true, route: "stakeholder_summary_json" },
  { to: "/list", label: "Tasks", icon: ListIcon, route: "api_tasks" },
  { to: "/kanban", label: "Kanban", icon: KanbanSquare, route: "api_tasks" },
  { to: "/milestones", label: "Milestones", icon: Milestone, route: "api_milestones" },
  { to: "/budget", label: "Budget", icon: Wallet, route: "api_budget_summary" },
  { to: "/pace", label: "Pace", icon: CalendarClock, route: "api_sprint" },
  { to: "/graph", label: "Graph", icon: GitFork, route: "api_graph", operatorOnly: true },
  { to: "/ci", label: "CI", icon: Workflow, route: "api_ci", operatorOnly: true },
  { to: "/tunnel", label: "Tunnel", icon: Radio, route: "api_tunnel_status", operatorOnly: true },
  { to: "/metrics", label: "Metrics", icon: BarChart3, route: "api_metrics", operatorOnly: true },
  { to: "/logs", label: "Logs", icon: ScrollText, route: "api_events", operatorOnly: true },
  {
    to: "/portfolio",
    label: "Portfolio",
    icon: LayoutGrid,
    route: "api_portfolio",
    operatorOnly: true,
    portfolioGated: true,
  },
]

interface Access {
  isStakeholder: boolean
  // /api/whoami's `routes`: present only for a stakeholder session.
  allowedRoutes?: string[]
}

function stakeholderMaySee(item: NavItem, allowedRoutes?: string[]): boolean {
  if (!allowedRoutes) return !item.operatorOnly
  return allowedRoutes.includes(item.route)
}

export function visibleNavItems({
  isStakeholder,
  allowedRoutes,
  portfolioAvailable,
}: Access & { portfolioAvailable: boolean }): NavItem[] {
  if (isStakeholder) return NAV_ITEMS.filter((item) => stakeholderMaySee(item, allowedRoutes))
  return NAV_ITEMS.filter((item) => !item.portfolioGated || portfolioAvailable)
}

// isPathAllowed answers whether a page opened by URL should render, or send
// a stakeholder back to the summary instead of a 403.
export function isPathAllowed(pathname: string, { isStakeholder, allowedRoutes }: Access): boolean {
  if (!isStakeholder || !allowedRoutes) return true
  const item = NAV_ITEMS.find((i) => i.to === pathname)
  return !item || allowedRoutes.includes(item.route)
}
