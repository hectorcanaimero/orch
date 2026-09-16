import { useRef, useState } from "react"
import { Check, ClipboardCopy } from "lucide-react"
import { Button } from "@/components/ui/button"
import { receiptMarkdown, type ReceiptPayload } from "@/hooks/useReceipt"
import { fmt, t } from "@/i18n"
import { formatDuration, formatRelative } from "@/lib/time"
import { cn } from "@/lib/utils"

const BUILT_WITH_KEY = "orch.receipt.builtWith"

function readBuiltWith(): boolean {
  try {
    return window.localStorage.getItem(BUILT_WITH_KEY) !== "off"
  } catch {
    return true
  }
}

/** The last finished run: what it did, how long, what it cost, and a copy as Markdown. */
export function RunReceipt({ payload, now }: { payload: ReceiptPayload; now: number }) {
  const r = payload.receipt
  const [builtWith, setBuiltWith] = useState(readBuiltWith)
  const [copied, setCopied] = useState(false)
  const [manual, setManual] = useState(false)
  const area = useRef<HTMLTextAreaElement>(null)
  if (!r) return null

  const text = receiptMarkdown(payload, builtWith)
  const toggle = (on: boolean) => {
    setBuiltWith(on)
    try {
      window.localStorage.setItem(BUILT_WITH_KEY, on ? "on" : "off")
    } catch {
      // Storage blocked: the choice lasts for this page only.
    }
  }
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // No clipboard (plain http, or refused): show the text selected instead.
      setManual(true)
      window.setTimeout(() => area.current?.select(), 0)
    }
  }
  const prsPassed = r.prs.filter((p) => p.ci_status === "success").length

  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 px-4 pt-4">
        <p className="min-w-0 truncate font-mono text-xs text-muted-foreground" title={r.run_id}>
          {r.run_id}
        </p>
        <p className="text-xs text-muted-foreground">
          {r.finished_at ? t("receipt.finished", { time: formatRelative(r.finished_at, now) }) : t("receipt.unfinished")}
        </p>
      </div>

      <p className="px-4 pt-2 text-sm">
        <span className="font-semibold tabular-nums">{t("receipt.done", { count: r.done.length })}</span>
        {r.blocked.length > 0 ? (
          <span className="text-status-blocked"> · {t("receipt.blocked", { count: r.blocked.length })}</span>
        ) : null}
        {r.failed_attempts > 0 ? (
          <span className="text-muted-foreground"> · {t("receipt.failed_attempts", { count: r.failed_attempts })}</span>
        ) : null}
      </p>
      <p className="px-4 pt-1 text-xs text-muted-foreground tabular-nums">
        {t("receipt.times", { wall: formatDuration(r.wall_seconds), agent: formatDuration(r.agent_seconds) })}
        {r.prs.length > 0 ? ` · ${t("receipt.prs", { count: r.prs.length, passed: prsPassed })}` : null}
      </p>

      <ul className="mt-3 divide-y border-t">
        {r.providers.map((p) => (
          <li key={p.provider} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-3 px-4 py-2 text-sm">
            <span className="min-w-0">
              <span className="font-medium">{p.provider}</span>
              <span className="ml-2 font-mono text-[11px] text-muted-foreground tabular-nums">
                {p.tokens_in + p.tokens_out > 0
                  ? p.weighted_tokens !== p.tokens_in + p.tokens_out
                    ? t("receipt.tokens_weighted", {
                        weighted: fmt.number(p.weighted_tokens),
                        raw: fmt.number(p.tokens_in + p.tokens_out),
                      })
                    : t("receipt.tokens", { raw: fmt.number(p.tokens_in + p.tokens_out) })
                  : t("receipt.dispatches", { count: p.dispatches })}
              </span>
            </span>
            <span className={cn("font-mono text-xs tabular-nums", p.cost_source === "no_data" && "text-muted-foreground")}>
              {p.cost_source === "no_data" ? t("now.cost.no_data") : `${p.cost_source === "estimated" ? "~" : ""}${fmt.usd(p.cost_usd)}`}
              {p.cost_source !== "no_data" ? <span className="ml-1 text-muted-foreground">{t(`now.cost.${p.cost_source}`)}</span> : null}
            </span>
          </li>
        ))}
      </ul>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3">
        <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
          <input
            id="receipt-built-with"
            type="checkbox"
            className="h-3.5 w-3.5 accent-brand"
            checked={builtWith}
            onChange={(e) => toggle(e.target.checked)}
          />
          {t("receipt.built_with")}
        </label>
        <Button type="button" size="sm" variant="outline" onClick={copy}>
          {copied ? <Check className="h-4 w-4" aria-hidden /> : <ClipboardCopy className="h-4 w-4" aria-hidden />}
          {copied ? t("common.copied") : t("receipt.copy")}
        </Button>
      </div>
      {manual ? (
        <div className="border-t px-4 py-3">
          <p className="mb-2 text-xs text-muted-foreground">{t("receipt.copy_manual")}</p>
          <textarea
            ref={area}
            readOnly
            aria-label={t("receipt.copy")}
            value={text}
            className="h-40 w-full resize-y rounded-md border bg-muted p-2 font-mono text-[11px]"
          />
        </div>
      ) : null}
    </div>
  )
}
