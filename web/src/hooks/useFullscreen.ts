import { useCallback, useEffect, useState } from "react"
import type { RefObject } from "react"

/**
 * Sprint E-6 UX: minimal wrapper around the Fullscreen API for the
 * Architecture and Kanban regions. Returns `[isFullscreen, toggle]`.
 *
 * Listens on the singleton `document.fullscreenchange` event (fires for
 * exits triggered by the browser's Esc key too), so the UI stays in sync
 * even when the user leaves fullscreen without clicking our button.
 *
 * Safari and older browsers exposed a `webkitFullscreenElement` variant.
 * We touch both surfaces via casts to keep this dependency-free without
 * broadening the app-wide type surface.
 */
export function useFullscreen(
  ref: RefObject<HTMLElement | null>,
): [boolean, () => void] {
  const [isFullscreen, setIsFullscreen] = useState(false)

  useEffect(() => {
    const handler = () => {
      const doc = document as Document & { webkitFullscreenElement?: Element }
      setIsFullscreen(
        !!(document.fullscreenElement || doc.webkitFullscreenElement),
      )
    }
    document.addEventListener("fullscreenchange", handler)
    document.addEventListener("webkitfullscreenchange", handler)
    return () => {
      document.removeEventListener("fullscreenchange", handler)
      document.removeEventListener("webkitfullscreenchange", handler)
    }
  }, [])

  const toggle = useCallback(() => {
    const el = ref.current
    if (!el) return
    const doc = document as Document & {
      webkitFullscreenElement?: Element
      webkitExitFullscreen?: () => Promise<void>
    }
    const inFs = !!(document.fullscreenElement || doc.webkitFullscreenElement)
    if (inFs) {
      if (document.exitFullscreen) {
        void document.exitFullscreen()
      } else if (doc.webkitExitFullscreen) {
        void doc.webkitExitFullscreen()
      }
      return
    }
    const target = el as HTMLElement & {
      webkitRequestFullscreen?: () => Promise<void>
    }
    if (target.requestFullscreen) {
      void target.requestFullscreen()
    } else if (target.webkitRequestFullscreen) {
      void target.webkitRequestFullscreen()
    }
  }, [ref])

  return [isFullscreen, toggle]
}
