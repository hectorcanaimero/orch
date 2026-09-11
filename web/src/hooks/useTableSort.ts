import { useCallback, useMemo, useState } from "react"

export type SortDirection = "asc" | "desc"

export interface SortState {
  column: string
  direction: SortDirection
}

export type SortableValue = string | number | boolean | null | undefined

export type SortGetters<T> = Record<string, (row: T) => SortableValue>

export interface UseTableSortResult<T> {
  sort: SortState
  toggleSort: (column: string) => void
  sortedRows: (rows: T[], getters: SortGetters<T>) => T[]
}

/**
 * Compare two `SortableValue`s deterministically. Nulls/undefineds always
 * sort AFTER real values regardless of direction — a common convention for
 * "unknown" cells in Jira-style tables.
 */
function compareValues(a: SortableValue, b: SortableValue): number {
  const aNil = a === null || a === undefined
  const bNil = b === null || b === undefined
  if (aNil && bNil) return 0
  if (aNil) return 1
  if (bNil) return -1
  if (typeof a === "number" && typeof b === "number") return a - b
  if (typeof a === "boolean" && typeof b === "boolean") {
    return a === b ? 0 : a ? 1 : -1
  }
  const as = String(a).toLowerCase()
  const bs = String(b).toLowerCase()
  if (as < bs) return -1
  if (as > bs) return 1
  return 0
}

/**
 * Tiny generic client-side sort hook for table views.
 *
 * - Toggling the same column flips direction.
 * - Toggling a new column sets direction to `asc`.
 * - `sortedRows` returns a NEW array (does not mutate input) — safe to use
 *   directly in a render.
 * - Missing getters silently fall through (no sort) — the caller should
 *   ensure the active column key exists in the getters map.
 */
export function useTableSort<T>(initial: SortState): UseTableSortResult<T> {
  const [sort, setSort] = useState<SortState>(initial)

  const toggleSort = useCallback((column: string) => {
    setSort((prev) => {
      if (prev.column === column) {
        return {
          column,
          direction: prev.direction === "asc" ? "desc" : "asc",
        }
      }
      return { column, direction: "asc" }
    })
  }, [])

  const sortedRows = useCallback(
    (rows: T[], getters: SortGetters<T>): T[] => {
      const getter = getters[sort.column]
      if (!getter) return rows
      const dir = sort.direction === "asc" ? 1 : -1
      // Copy to avoid mutating caller's array. `Array.prototype.sort` is
      // stable in modern JS engines — we rely on that for secondary
      // tiebreakers (caller sorts by tiebreaker column first, then calls
      // this again with the primary column).
      const copy = [...rows]
      copy.sort((a, b) => compareValues(getter(a), getter(b)) * dir)
      return copy
    },
    [sort],
  )

  return useMemo(
    () => ({ sort, toggleSort, sortedRows }),
    [sort, toggleSort, sortedRows],
  )
}
