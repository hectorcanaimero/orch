const WORD_START = /[\s._/:>-]/

/**
 * How well `query` matches `text`, case-insensitively: 0 is no match, higher
 * is better. A plain substring beats any scattered match, earlier beats later;
 * otherwise the query's letters must appear in order, and runs of adjacent
 * letters and letters that start a word ("kb" → "Kanban board") score more.
 */
export function fuzzyScore(query: string, text: string): number {
  const q = query.trim().toLowerCase().replace(/\s+/g, " ")
  if (!q) return 1
  const s = text.toLowerCase()
  const at = s.indexOf(q)
  if (at !== -1) return 1000 - Math.min(at, 500) + (at === 0 || WORD_START.test(s[at - 1]) ? 100 : 0)

  let score = 0
  let last = -1
  let run = 0
  for (const ch of q.replace(/ /g, "")) {
    const i = s.indexOf(ch, last + 1)
    if (i === -1) return 0
    run = i === last + 1 ? run + 1 : 0
    score += 1 + run * 2 + (i === 0 || WORD_START.test(s[i - 1]) ? 3 : 0)
    last = i
  }
  return score
}
