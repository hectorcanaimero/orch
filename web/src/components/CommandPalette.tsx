import { useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react"
import { createPortal } from "react-dom"
import { useLocation, useNavigate } from "react-router-dom"
import {
  CornerDownLeft,
  Keyboard,
  ListFilter,
  Monitor,
  Moon,
  ScrollText,
  Search,
  Sun,
  Terminal,
  X,
  type LucideIcon,
} from "lucide-react"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { TaskDetailModal } from "@/components/TaskDetailModal"
import { useTasks } from "@/hooks/useTasks"
import { t, type MessageKey } from "@/i18n"
import { fuzzyScore } from "@/lib/fuzzy"
import { TAB_ICONS, type NavDestination } from "@/lib/nav"
import { STATUS, statusMeta } from "@/lib/status"
import { FILTERED_PAGES, GO_KEYS, isMac } from "@/lib/shortcuts"
import { nextTheme, useTheme } from "@/lib/theme"
import type { Task } from "@/lib/types"
import { cn } from "@/lib/utils"

// The dashboard only reads (rule 13): every entry here navigates, filters,
// opens a panel or copies a command for a terminal. Nothing changes state.

type Section = "goto" | "tasks" | "filters" | "panels" | "commands"
const SECTIONS: Section[] = ["goto", "tasks", "filters", "panels", "commands"]
const PER_SECTION = 8

const STATUS_FILTERS = [
  { value: "backlog", meta: STATUS.backlog },
  { value: "todo", meta: STATUS.todo },
  { value: "in-progress", meta: STATUS.in_progress },
  { value: "blocked", meta: STATUS.blocked },
  { value: "done", meta: STATUS.done },
]

const COMMANDS: { command: string; hint: MessageKey }[] = [
  { command: "orch status", hint: "palette.cmd.status" },
  { command: "orch run", hint: "palette.cmd.run" },
  { command: "orch doctor", hint: "palette.cmd.doctor" },
  { command: "orch dashboard --tunnel", hint: "palette.cmd.tunnel" },
]

const THEME_ICON = { dark: Moon, light: Sun, system: Monitor } as const

interface Item {
  key: string
  section: Section
  label: string
  search: string
  icon: LucideIcon
  iconClass?: string
  prefix?: string
  detail?: string
  mono?: boolean
  run: () => void
}

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded border bg-muted px-1.5 font-mono text-[11px] leading-none text-muted-foreground">
      {children}
    </kbd>
  )
}

export interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** What this session may see — the palette never offers a page the nav hides. */
  destinations: NavDestination[]
  isStakeholder: boolean
  canSeeLogs: boolean
  onOpenLogs: () => void
  onShowShortcuts: () => void
}

/** ⌘K / Ctrl+K: go to a page, find a task, filter the work, copy a command. */
export function CommandPalette(props: CommandPaletteProps) {
  const [taskId, setTaskId] = useState<string | null>(null)
  const canSeeTasks = !props.isStakeholder && props.destinations.some((d) => d.tabs.some((tab) => tab.to === "/work/list"))
  return (
    <>
      {props.open ? (
        canSeeTasks ? (
          <PaletteWithTasks {...props} onOpenTask={setTaskId} />
        ) : (
          <Palette {...props} tasks={[]} canFilter={false} onOpenTask={setTaskId} />
        )
      ) : null}
      {taskId ? <TaskDetailModal taskId={taskId} onClose={() => setTaskId(null)} /> : null}
    </>
  )
}

type PaletteProps = CommandPaletteProps & { onOpenTask: (id: string) => void }

// Mounted only while open, so the task list is requested only when someone searches.
function PaletteWithTasks(props: PaletteProps) {
  const { data } = useTasks()
  return <Palette {...props} tasks={data?.tasks ?? []} canFilter />
}

function uniq<T>(values: T[]): T[] {
  return [...new Set(values)]
}

function Palette({
  onOpenChange,
  destinations,
  isStakeholder,
  canSeeLogs,
  onOpenLogs,
  onShowShortcuts,
  onOpenTask,
  tasks,
  canFilter,
}: PaletteProps & { tasks: Task[]; canFilter: boolean }) {
  const navigate = useNavigate()
  const { pathname, search } = useLocation()
  const [theme, setTheme] = useTheme()
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)
  const [notice, setNotice] = useState<{ command: string; copied: boolean } | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const baseId = useId()
  const listId = `${baseId}-list`
  const optionId = (i: number) => `${baseId}-option-${i}`
  const close = () => onOpenChange(false)

  // Focus the search field, hold the page still, and give focus back on close.
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const overflow = document.body.style.overflow
    document.body.style.overflow = "hidden"
    inputRef.current?.focus()
    return () => {
      document.body.style.overflow = overflow
      previous?.focus()
    }
  }, [])

  const copy = async (command: string) => {
    try {
      await navigator.clipboard.writeText(command)
      setNotice({ command, copied: true })
    } catch {
      // Clipboard refused (insecure origin, permissions): show the command to copy by hand.
      setNotice({ command, copied: false })
    }
  }

  const searching = query.trim() !== ""
  const all: Item[] = []
  const go = (to: string) => () => {
    close()
    navigate(to)
  }

  for (const d of destinations) {
    for (const tab of d.tabs) {
      const label = d.tabs.length > 1 ? `${t(d.label)} › ${t(tab.label)}` : t(d.label)
      all.push({ key: `go:${tab.to}`, section: "goto", label, search: label, icon: TAB_ICONS[tab.to] ?? d.icon, run: go(tab.to) })
    }
  }

  if (searching) {
    for (const task of tasks) {
      const meta = statusMeta(task.status)
      all.push({
        key: `task:${task.id}`,
        section: "tasks",
        label: task.title,
        prefix: task.id,
        search: `${task.id} ${task.title}`,
        icon: meta.icon,
        iconClass: meta.text,
        detail: t(meta.label),
        run: () => {
          close()
          onOpenTask(task.id)
        },
      })
    }
  }

  if (canFilter) {
    // On a filtered page the new filter joins the ones already set; elsewhere it starts the list fresh.
    const onFilteredPage = FILTERED_PAGES.includes(pathname)
    const filter = (param: string, value: string) => () => {
      const next = new URLSearchParams(onFilteredPage ? search : "")
      next.set(param, value)
      close()
      navigate(`${onFilteredPage ? pathname : "/work/list"}?${next}`)
    }
    for (const { value, meta } of STATUS_FILTERS) {
      const label = t("palette.filter_status", { status: t(meta.label) })
      all.push({ key: `filter:status:${value}`, section: "filters", label, search: label, icon: meta.icon, iconClass: meta.text, run: filter("status", value) })
    }
    if (searching) {
      for (const phase of uniq(tasks.map((task) => task.phase)).sort((a, b) => a - b)) {
        const label = t("palette.filter_phase", { phase })
        all.push({ key: `filter:phase:${phase}`, section: "filters", label, search: label, icon: ListFilter, run: filter("phase", String(phase)) })
      }
      for (const model of uniq(tasks.map((task) => task.model).filter(Boolean)).sort()) {
        const label = t("palette.filter_model", { model })
        all.push({ key: `filter:model:${model}`, section: "filters", label, search: label, icon: ListFilter, run: filter("model", model) })
      }
    }
  }

  if (canSeeLogs) {
    const label = t("palette.open_logs")
    all.push({
      key: "panel:logs",
      section: "panels",
      label,
      search: `${label} ${t("nav.logs")}`,
      icon: ScrollText,
      run: () => {
        close()
        onOpenLogs()
      },
    })
  }
  const next = nextTheme(theme)
  const themeLabel = t("theme.switch_to", { theme: t(`theme.${next}` as const) })
  all.push({ key: "panel:theme", section: "panels", label: themeLabel, search: `${themeLabel} theme`, icon: THEME_ICON[next], run: () => setTheme(next) })
  const shortcutsLabel = t("palette.show_shortcuts")
  all.push({
    key: "panel:shortcuts",
    section: "panels",
    label: shortcutsLabel,
    search: `${shortcutsLabel} keys help`,
    icon: Keyboard,
    run: () => {
      close()
      onShowShortcuts()
    },
  })

  if (!isStakeholder) {
    for (const { command, hint } of COMMANDS) {
      all.push({ key: `cmd:${command}`, section: "commands", label: command, search: `${command} ${t(hint)}`, detail: t(hint), mono: true, icon: Terminal, run: () => void copy(command) })
    }
    if (searching) {
      for (const task of tasks) {
        const key = statusMeta(task.status).key
        if (key !== "blocked" && key !== "failed") continue
        const unblock = { command: `orch task set --id ${task.id} --status todo` }
        all.push({
          key: `cmd:unblock:${task.id}`,
          section: "commands",
          label: unblock.command,
          search: `${unblock.command} ${task.title} unblock`,
          detail: t("palette.cmd.unblock", { id: task.id }),
          mono: true,
          icon: Terminal,
          run: () => void copy(unblock.command),
        })
      }
    }
  }

  const groups = SECTIONS.map((section) => ({
    section,
    items: all
      .map((item) => ({ item, score: fuzzyScore(query, item.search) }))
      .filter((x) => x.item.section === section && x.score > 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, PER_SECTION)
      .map((x) => x.item),
  })).filter((g) => g.items.length > 0)
  const flat = groups.flatMap((g) => g.items)
  const current = Math.min(active, Math.max(flat.length - 1, 0))

  useEffect(() => {
    document.getElementById(optionId(current))?.scrollIntoView?.({ block: "nearest" })
  })

  const onKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault()
      if (flat.length) setActive((current + (e.key === "ArrowDown" ? 1 : -1) + flat.length) % flat.length)
    } else if (e.key === "Enter") {
      e.preventDefault()
      flat[current]?.run()
    } else if (e.key === "Escape") {
      // Stop here, or the log panel's own Escape would close underneath.
      e.preventDefault()
      e.stopPropagation()
      close()
    } else if (e.key === "Tab") {
      // The search field is the only stop: arrows move through the results.
      e.preventDefault()
    }
  }

  let index = -1
  return createPortal(
    <div className="fixed inset-0 z-50 md:flex md:items-start md:justify-center md:px-4 md:pt-[12vh]">
      <div className="absolute inset-0 bg-black/60 animate-in fade-in-0" aria-hidden onClick={close} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("palette.title")}
        className={cn(
          "relative flex h-full w-full flex-col bg-popover text-popover-foreground",
          "md:h-auto md:max-h-[min(34rem,76vh)] md:max-w-[40rem] md:overflow-hidden md:rounded-xl md:border",
          "md:shadow-[0_24px_48px_-16px_rgb(0_0_0/0.5)] md:animate-in md:fade-in-0 md:zoom-in-95",
        )}
      >
        <div className="flex min-h-14 shrink-0 items-center gap-3 border-b px-4 pt-[env(safe-area-inset-top)]">
          <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded={flat.length > 0}
            aria-controls={listId}
            aria-activedescendant={flat.length ? optionId(current) : undefined}
            aria-autocomplete="list"
            aria-label={t("palette.title")}
            placeholder={t("palette.placeholder")}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setActive(0)
              setNotice(null)
            }}
            onKeyDown={onKeyDown}
            autoComplete="off"
            spellCheck={false}
            className="h-14 min-w-0 flex-1 bg-transparent text-[15px] outline-none placeholder:text-muted-foreground"
          />
          <span className="hidden md:inline-flex">
            <Kbd>Esc</Kbd>
          </span>
          <button
            type="button"
            onClick={close}
            aria-label={t("palette.close")}
            className="flex h-10 w-10 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring md:hidden"
          >
            <X className="h-4 w-4" aria-hidden />
          </button>
        </div>

        <div id={listId} role="listbox" aria-label={t("palette.title")} className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2">
          {groups.length === 0 ? (
            <p className="px-3 py-10 text-center text-sm text-muted-foreground">{t("palette.empty", { query: query.trim() })}</p>
          ) : (
            groups.map((g) => (
              <div key={g.section} role="group" aria-labelledby={`${baseId}-${g.section}`} className="pb-1">
                <div id={`${baseId}-${g.section}`} role="presentation" className="px-3 pb-1.5 pt-2.5 text-xs font-medium text-muted-foreground">
                  {t(`palette.section.${g.section}` as const)}
                </div>
                {g.items.map((item) => {
                  index += 1
                  const i = index
                  const selected = i === current
                  const Icon = item.icon
                  return (
                    <div
                      key={item.key}
                      id={optionId(i)}
                      role="option"
                      aria-selected={selected}
                      onMouseMove={() => {
                        if (!selected) setActive(i)
                      }}
                      onMouseDown={(e) => e.preventDefault()}
                      onClick={() => item.run()}
                      className={cn(
                        "flex min-h-10 cursor-pointer items-center gap-3 rounded-md px-3 py-2 text-sm",
                        selected ? "bg-muted text-foreground" : "text-foreground/90",
                      )}
                    >
                      <Icon className={cn("h-4 w-4 shrink-0", item.iconClass ?? "text-muted-foreground")} aria-hidden />
                      {item.prefix ? <span className="hidden shrink-0 font-mono text-xs text-muted-foreground sm:inline">{item.prefix}</span> : null}
                      <span className={cn("min-w-0 flex-1 truncate", item.mono && "font-mono text-[13px]")}>{item.label}</span>
                      {item.detail ? <span className="hidden max-w-[45%] shrink-0 truncate text-xs text-muted-foreground sm:block">{item.detail}</span> : null}
                      <CornerDownLeft className={cn("hidden h-3.5 w-3.5 shrink-0 text-muted-foreground", selected && "md:block")} aria-hidden />
                    </div>
                  )
                })}
              </div>
            ))
          )}
        </div>

        <div
          className={cn(
            "shrink-0 border-t px-4 pb-[calc(0.625rem+env(safe-area-inset-bottom))] pt-2.5 text-xs text-muted-foreground",
            // On a phone the key hint means nothing; the bar shows only a copy notice.
            !notice && "hidden md:block",
          )}
          aria-live="polite"
        >
          {notice ? (
            notice.copied ? (
              <span className="text-foreground">{t("palette.copied", { command: notice.command })}</span>
            ) : (
              <span className="flex flex-wrap items-center gap-2">
                {t("palette.copy_blocked")}
                <code className="select-all rounded bg-muted px-1.5 py-0.5 font-mono text-foreground">{notice.command}</code>
              </span>
            )
          ) : (
            <span>{t("palette.hint")}</span>
          )}
        </div>
      </div>
    </div>,
    document.body,
  )
}

export function ShortcutsDialog({ open, onOpenChange, destinations }: { open: boolean; onOpenChange: (open: boolean) => void; destinations: NavDestination[] }) {
  const letters = Object.fromEntries(Object.entries(GO_KEYS).map(([key, id]) => [id, key]))
  const rows: { keys: string[][]; label: string }[] = [
    { keys: [[isMac() ? "⌘" : "Ctrl", "K"]], label: t("shortcuts.palette") },
    ...destinations.map((d) => ({ keys: [["g"], [letters[d.id]]], label: t("shortcuts.go", { destination: t(d.label) }) })),
    ...(destinations.some((d) => d.tabs.some((tab) => tab.to === "/work/list")) ? [{ keys: [["/"]], label: t("shortcuts.search") }] : []),
    { keys: [["?"]], label: t("shortcuts.help") },
    { keys: [["Esc"]], label: t("shortcuts.close") },
  ]
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("shortcuts.title")}</DialogTitle>
        </DialogHeader>
        <ul className="divide-y text-sm">
          {rows.map((row) => (
            <li key={row.label} className="flex items-center justify-between gap-4 py-2.5">
              <span>{row.label}</span>
              <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
                {row.keys.map((chord, i) => (
                  <span key={chord.join("+")} className="flex items-center gap-1.5">
                    {i > 0 ? <span>{t("shortcuts.then")}</span> : null}
                    {chord.map((key) => (
                      <Kbd key={key}>{key}</Kbd>
                    ))}
                  </span>
                ))}
              </span>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  )
}
