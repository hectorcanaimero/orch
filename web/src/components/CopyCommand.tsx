import { useState } from "react"
import { Check, Copy } from "lucide-react"
import { Button } from "@/components/ui/button"
import { t } from "@/i18n"
import { cn } from "@/lib/utils"

/** A command in a code box with a copy button; the text stays selectable when the clipboard is refused. */
export function CopyCommand({ command, className }: { command: string; className?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard refused (insecure origin): the command is still selectable.
    }
  }
  return (
    <div className={cn("mt-3 flex items-stretch gap-2", className)}>
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap rounded-md border bg-muted px-2.5 py-2 font-mono text-xs">{command}</code>
      <Button type="button" size="sm" variant="outline" onClick={copy} aria-label={copied ? t("common.copied") : t("common.copy")}>
        {copied ? <Check className="h-4 w-4" aria-hidden /> : <Copy className="h-4 w-4" aria-hidden />}
      </Button>
    </div>
  )
}
