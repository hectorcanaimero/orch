import * as React from "react"
import { createPortal } from "react-dom"
import { X } from "lucide-react"
import { cn } from "@/lib/utils"

/**
 * Hand-written Dialog primitive (no Radix). Matches the existing shadcn "New
 * York" visual style. Renders into document.body via portal, closes on ESC,
 * and locks body scroll while open. Not a full a11y-compliant focus trap —
 * good enough for a read-only detail modal.
 */

interface DialogContextValue {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const DialogContext = React.createContext<DialogContextValue | null>(null)

function useDialogContext() {
  const ctx = React.useContext(DialogContext)
  if (!ctx) throw new Error("Dialog subcomponents must be used inside <Dialog>")
  return ctx
}

export interface DialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  children: React.ReactNode
}

export function Dialog({ open, onOpenChange, children }: DialogProps) {
  const value = React.useMemo(
    () => ({ open, onOpenChange }),
    [open, onOpenChange],
  )
  return <DialogContext.Provider value={value}>{children}</DialogContext.Provider>
}

export interface DialogContentProps
  extends React.HTMLAttributes<HTMLDivElement> {
  /**
   * When true (the default), a close (X) button is rendered in the top-right
   * corner and ESC closes the dialog.
   */
  showCloseButton?: boolean
}

export const DialogContent = React.forwardRef<HTMLDivElement, DialogContentProps>(
  ({ className, children, showCloseButton = true, ...props }, ref) => {
    const { open, onOpenChange } = useDialogContext()

    // Close on ESC
    React.useEffect(() => {
      if (!open) return
      const onKey = (e: KeyboardEvent) => {
        if (e.key === "Escape") onOpenChange(false)
      }
      window.addEventListener("keydown", onKey)
      return () => window.removeEventListener("keydown", onKey)
    }, [open, onOpenChange])

    // Lock body scroll while open
    React.useEffect(() => {
      if (!open) return
      const prev = document.body.style.overflow
      document.body.style.overflow = "hidden"
      return () => {
        document.body.style.overflow = prev
      }
    }, [open])

    if (!open) return null
    if (typeof document === "undefined") return null

    return createPortal(
      <div
        className="fixed inset-0 z-50 flex items-center justify-center p-4"
        role="dialog"
        aria-modal="true"
      >
        <div
          className="absolute inset-0 bg-black/50 backdrop-blur-sm animate-in fade-in-0"
          onClick={() => onOpenChange(false)}
        />
        <div
          ref={ref}
          className={cn(
            "relative z-10 grid w-full max-w-lg gap-4 rounded-lg border bg-background p-6 shadow-lg animate-in fade-in-0 zoom-in-95",
            "max-h-[calc(100vh-4rem)] overflow-y-auto overscroll-contain",
            className,
          )}
          {...props}
        >
          {children}
          {showCloseButton ? (
            <button
              type="button"
              onClick={() => onOpenChange(false)}
              className="absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
              aria-label="Close"
            >
              <X className="h-4 w-4" />
            </button>
          ) : null}
        </div>
      </div>,
      document.body,
    )
  },
)
DialogContent.displayName = "DialogContent"

export const DialogHeader = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn("flex flex-col space-y-1.5 text-left", className)}
    {...props}
  />
)
DialogHeader.displayName = "DialogHeader"

export const DialogFooter = ({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn(
      "flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2",
      className,
    )}
    {...props}
  />
)
DialogFooter.displayName = "DialogFooter"

export const DialogTitle = React.forwardRef<
  HTMLHeadingElement,
  React.HTMLAttributes<HTMLHeadingElement>
>(({ className, ...props }, ref) => (
  <h2
    ref={ref}
    className={cn(
      "text-lg font-semibold leading-none tracking-tight",
      className,
    )}
    {...props}
  />
))
DialogTitle.displayName = "DialogTitle"

export const DialogDescription = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(({ className, ...props }, ref) => (
  <p
    ref={ref}
    className={cn("text-sm text-muted-foreground", className)}
    {...props}
  />
))
DialogDescription.displayName = "DialogDescription"
