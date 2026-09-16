import { en } from "./en"
import { es } from "./es"
import { pt } from "./pt"
import type { Dictionary, Lang, Plural } from "."

const DICTS: Record<Exclude<Lang, "en">, Dictionary> = { es, pt }

const forms = (v: string | Plural) => (typeof v === "string" ? [v] : [v.one, v.other])

/**
 * English fragments still showing in `text` although `lang` translates them:
 * the literal pieces between {placeholders}, 8+ characters, that the
 * translation does not also contain. Used by page tests in es/pt.
 */
export function englishLeaks(text: string, lang: Exclude<Lang, "en">): string[] {
  const leaks = new Set<string>()
  for (const [key, value] of Object.entries(en) as [keyof typeof en, string | Plural][]) {
    const translated = forms(DICTS[lang][key]).join("\n")
    for (const form of forms(value)) {
      for (const piece of form.split(/\{[a-z_]+\}/)) {
        const frag = piece.trim()
        if (frag.length < 8 || !/[a-z]{3}/.test(frag) || translated.includes(frag)) continue
        // Whole words only: "dispatch" inside per_dispatch_usd is a config key, not a leak.
        const escaped = frag.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
        if (new RegExp(`(?<![\\w])${escaped}(?![\\w])`).test(text)) leaks.add(frag)
      }
    }
  }
  return [...leaks]
}

/** What a reader sees or hears on `root`: each text node, aria-label, title and placeholder on its own line. */
export function visibleText(root: Element): string {
  const parts: string[] = []
  const walker = root.ownerDocument.createTreeWalker(root, 4 /* NodeFilter.SHOW_TEXT */)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) parts.push(n.nodeValue ?? "")
  root.querySelectorAll("[aria-label],[title],[placeholder]").forEach((el) => {
    for (const attr of ["aria-label", "title", "placeholder"]) {
      const v = el.getAttribute(attr)
      if (v) parts.push(v)
    }
  })
  return parts.join("\n")
}
