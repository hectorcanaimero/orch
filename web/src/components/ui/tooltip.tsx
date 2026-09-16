import type { ReactNode } from "react"
import * as TooltipPrimitive from "@radix-ui/react-tooltip"

/**
 * Hover/focus label for icon-only controls. Carries its own provider so a
 * control renders the same inside the app shell and in an isolated test.
 */
export function Tooltip({
  content,
  side = "right",
  children,
}: {
  content: ReactNode
  side?: "top" | "right" | "bottom" | "left"
  children: ReactNode
}) {
  return (
    <TooltipPrimitive.Provider delayDuration={250}>
      <TooltipPrimitive.Root>
        <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            side={side}
            sideOffset={8}
            className="z-50 rounded-md border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-[0_4px_12px_-4px_rgb(0_0_0/0.3)] data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in-0"
          >
            {content}
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  )
}
