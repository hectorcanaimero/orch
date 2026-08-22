import { AlertTriangle, ExternalLink } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useProjectConfig } from "@/hooks/useProjectConfig"
import { cn } from "@/lib/utils"

/**
 * Board URL comes from the served project's `config.yaml` under the
 * `dashboard.board_url` key (Sprint E-3). The SPA is a per-project client,
 * so a global build-time `VITE_BOARD_URL` would be structurally wrong —
 * two projects served from the same SPA build would collide.
 *
 * The target is an operator-owned ExcaliDash canvas served over Cloudflare.
 * At the time of writing, the domain does NOT set `X-Frame-Options` or
 * `Content-Security-Policy: frame-ancestors`, so `<iframe>` embedding works
 * without a proxy.
 */
export function BoardPage() {
  const { data: config, isLoading, isError, error } = useProjectConfig()
  const boardUrl = config?.dashboard?.board_url ?? undefined
  const hasUrl = typeof boardUrl === "string" && boardUrl.trim().length > 0

  return (
    // h-[calc(100vh-4rem)] matches AppLayout's py-8 (2rem top + 2rem bottom)
    // so the iframe fills the remaining viewport height without page scroll.
    <div className="flex h-[calc(100vh-4rem)] flex-col gap-4">
      <header className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Board</h1>
          <p className="text-sm text-muted-foreground">
            Embedded ExcaliDash canvas
          </p>
        </div>
        {hasUrl ? (
          <a
            href={boardUrl}
            target="_blank"
            rel="noopener noreferrer"
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border border-input",
              "bg-background px-3 py-1.5 text-sm text-foreground",
              "transition-colors hover:bg-zinc-100",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            )}
            aria-label="Open board in a new tab"
          >
            <ExternalLink className="h-4 w-4" aria-hidden />
            Open in new tab
          </a>
        ) : null}
      </header>

      {/* Error banner sits ABOVE the fallback card so the operator can still
          see the empty-state guidance while knowing the config fetch failed.
          Loading state hides both to avoid flashing the empty-state Card. */}
      {isError ? (
        <Alert variant="destructive" className="flex-shrink-0">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load project configuration</AlertTitle>
          <AlertDescription>
            {error?.message ?? "Could not fetch /api/config from the backend."}
          </AlertDescription>
        </Alert>
      ) : null}

      {hasUrl ? (
        <div className="relative flex min-h-0 flex-1 overflow-hidden rounded-md border bg-zinc-50">
          <iframe
            src={boardUrl}
            title="ExcaliDash board"
            // ExcaliDash needs clipboard access for copy/paste of shapes and
            // fullscreen for its focus mode. We intentionally do NOT add a
            // `sandbox` attribute — the operator owns the target domain.
            allow="clipboard-read; clipboard-write; fullscreen"
            // `block` prevents the 4px bottom gap inline elements inherit,
            // otherwise the iframe leaves a visible strip inside the border.
            className="block h-full w-full"
          />
        </div>
      ) : isLoading ? (
        <div className="relative flex min-h-0 flex-1 overflow-hidden rounded-md border bg-zinc-50">
          <Skeleton className="h-full w-full" />
        </div>
      ) : (
        <Card className="flex-shrink-0">
          <CardHeader>
            <CardTitle>Board not configured</CardTitle>
            <CardDescription>
              Set{" "}
              <code className="font-mono">dashboard.board_url</code> in your
              project&apos;s <code className="font-mono">config.yaml</code> to
              embed an ExcaliDash canvas here.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <pre className="rounded-md bg-zinc-100 p-3 text-xs">
{`dashboard:
  board_url: "https://draw.z0n.co/editor/YOUR-CANVAS-ID"`}
            </pre>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
