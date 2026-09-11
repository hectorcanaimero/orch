package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// printCompactJSON writes v the way Python's `print(json.dumps(v,
// separators=(",", ":")))` does: no extra whitespace between tokens (Go's
// encoding/json is already compact without an Indent call), HTML-unescaped
// (Python never escapes `<`/`>`/`&`), one trailing newline. Matching this
// exactly is what makes scripts/parity.sh's diff meaningful.
func printCompactJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	return nil
}
