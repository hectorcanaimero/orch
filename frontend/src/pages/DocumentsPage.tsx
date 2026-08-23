import { useMemo, useState } from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import { AlertTriangle, FileText, Loader2 } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { useDocContent, useDocs, type DocEntry } from "@/hooks/useDocs"

type GroupedDocs = Record<string, Record<string, DocEntry[]>>

function groupDocs(docs: DocEntry[]): GroupedDocs {
  const out: GroupedDocs = {}
  for (const doc of docs) {
    if (!out[doc.category]) out[doc.category] = {}
    if (!out[doc.category][doc.sub_category])
      out[doc.category][doc.sub_category] = []
    out[doc.category][doc.sub_category].push(doc)
  }
  return out
}

function DocSidebar({
  grouped,
  selected,
  onSelect,
}: {
  grouped: GroupedDocs
  selected: string | null
  onSelect: (path: string) => void
}) {
  return (
    <ScrollArea className="h-full">
      <nav className="space-y-4 p-3">
        {Object.entries(grouped).map(([category, subs]) => (
          <div key={category}>
            <p className="mb-1 px-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {category}
            </p>
            {Object.entries(subs).map(([sub, docs]) => (
              <div key={sub} className="mb-2">
                {sub !== category && (
                  <p className="px-2 py-1 text-xs text-muted-foreground/70">
                    {sub}
                  </p>
                )}
                {docs.map((doc) => (
                  <button
                    key={doc.path}
                    type="button"
                    onClick={() => onSelect(doc.path)}
                    className={cn(
                      "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
                      selected === doc.path
                        ? "bg-zinc-100 font-medium text-zinc-900"
                        : "text-zinc-700 hover:bg-zinc-50 hover:text-zinc-900",
                    )}
                  >
                    <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                    <span className="truncate">{doc.title}</span>
                  </button>
                ))}
              </div>
            ))}
          </div>
        ))}
      </nav>
    </ScrollArea>
  )
}

function MarkdownViewer({ path }: { path: string }) {
  const { data, isLoading, isError, error } = useDocContent(path)

  if (isLoading) {
    return (
      <div className="space-y-3 p-6">
        <Skeleton className="h-8 w-1/2" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-5/6" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-3/4" />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="p-6">
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load document</AlertTitle>
          <AlertDescription>{(error as Error)?.message}</AlertDescription>
        </Alert>
      </div>
    )
  }

  return (
    <ScrollArea className="h-full">
      <article className="prose prose-zinc max-w-none p-6 prose-headings:font-semibold prose-headings:tracking-tight prose-code:rounded prose-code:bg-zinc-100 prose-code:px-1 prose-code:py-0.5 prose-code:text-sm prose-pre:bg-zinc-100 prose-pre:text-sm">
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{data ?? ""}</ReactMarkdown>
      </article>
    </ScrollArea>
  )
}

function EmptyState({ total }: { total: number }) {
  if (total === 0) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-center">
          <FileText className="mx-auto h-10 w-10 text-muted-foreground/40" />
          <h2 className="mt-3 text-base font-medium">No documents found</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Add markdown files to <code className="rounded bg-zinc-100 px-1 text-xs">docs/</code>,{" "}
            <code className="rounded bg-zinc-100 px-1 text-xs">specs/</code>, or{" "}
            <code className="rounded bg-zinc-100 px-1 text-xs">openspec/</code>.
          </p>
        </div>
      </div>
    )
  }
  return (
    <div className="flex h-full items-center justify-center">
      <div className="text-center text-muted-foreground">
        <FileText className="mx-auto h-8 w-8 opacity-30" />
        <p className="mt-2 text-sm">Select a document to view it</p>
      </div>
    </div>
  )
}

export function DocumentsPage() {
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const { data, isLoading, isError, error } = useDocs()

  const grouped = useMemo(
    () => (data ? groupDocs(data.docs) : {}),
    [data],
  )

  if (isLoading) {
    return (
      <div className="flex h-[calc(100vh-4rem)] gap-4">
        <div className="w-56 shrink-0 space-y-2 pt-2">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-7 w-full" />
          <Skeleton className="h-7 w-full" />
          <Skeleton className="h-7 w-5/6" />
        </div>
        <div className="flex-1 space-y-3 pt-2">
          <Skeleton className="h-8 w-1/3" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-4/5" />
        </div>
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load documents</AlertTitle>
        <AlertDescription>{(error as Error)?.message}</AlertDescription>
      </Alert>
    )
  }

  const total = data?.docs.length ?? 0

  return (
    <div className="flex h-[calc(100vh-4rem)] flex-col gap-0">
      <header className="mb-4 flex-shrink-0">
        <h1 className="text-2xl font-semibold tracking-tight">Documents</h1>
        <p className="text-sm text-muted-foreground">
          {total === 0 ? "No documents" : `${total} document${total !== 1 ? "s" : ""}`}
        </p>
      </header>

      <div className="flex min-h-0 flex-1 overflow-hidden rounded-lg border bg-white">
        {/* Sidebar */}
        <div className="w-56 shrink-0 border-r">
          {total > 0 ? (
            <DocSidebar
              grouped={grouped}
              selected={selectedPath}
              onSelect={setSelectedPath}
            />
          ) : null}
        </div>

        {/* Viewer */}
        <div className="min-w-0 flex-1">
          {selectedPath ? (
            <MarkdownViewer path={selectedPath} />
          ) : (
            <EmptyState total={total} />
          )}
        </div>
      </div>
    </div>
  )
}
