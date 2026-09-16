//go:build js && wasm

// Command orch-wasm is the site playground's engine: it exposes
// orchBuild(spec) to the page, which runs the spec through the same atomize
// and graph code as `orch atomize` and `orch validate` and returns the result
// as JSON. Build it with `make site-wasm`.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/hectorcanaimero/orch/internal/sitegen"
)

func main() {
	js.Global().Set("orchBuild", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 {
			return `{"error":"orchBuild takes one argument: the spec text"}`
		}
		out, err := json.Marshal(sitegen.Build(args[0].String()))
		if err != nil {
			return `{"error":"could not encode the result"}`
		}
		return string(out)
	}))
	js.Global().Set("orchSampleSpec", sitegen.SampleSpec)
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("orch-ready"))
	select {}
}
