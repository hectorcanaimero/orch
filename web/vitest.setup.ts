import { cleanup } from "@testing-library/react"
import { afterEach } from "vitest"

// Vitest isn't Jest — @testing-library/react's auto-cleanup only
// registers itself against a real global `afterEach`, which vitest only
// exposes when `test.globals: true` is set. Explicit here instead of
// flipping that on globally: unmounting after every render test is what
// stops the second test in a file from finding the first test's DOM
// still attached (which reads as "duplicate elements" and has nothing to
// do with the component actually rendering something twice).
afterEach(() => {
  cleanup()
})
