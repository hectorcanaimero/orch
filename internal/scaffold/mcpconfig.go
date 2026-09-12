package scaffold

import "os"

// mcpJSON is the `.mcp.json` a scaffolded project gets.
//
// It is what tells an MCP-capable agent CLI that this project serves orch's
// seven tools. The command is the bare binary name and the only argument is
// the subcommand: clients launch a stdio server with the directory holding
// `.mcp.json` as its working directory, and `orch mcp` resolves the project
// from there — so no absolute path is baked into a file the project commits.
// A client that does otherwise passes `--project-root` (see docs/MCP.md).
//
// Written here rather than in the embedded template tree for the same reason
// AGENTS.md's generic body is: it belongs to the scaffolder, not to any one
// project template, and `internal/templates`' tree is mirrored against the
// Python one file for file.
const mcpJSON = `{
  "mcpServers": {
    "orch": {
      "command": "orch",
      "args": ["mcp"]
    }
  }
}
`

// mcpConfig writes `.mcp.json`, never overwriting an existing one.
//
// Soft even under --force, unlike AGENTS.md. The file is shared: a project
// that already has one is very likely listing other servers in it, and
// replacing it would silently disconnect them. Re-adding orch to a file that
// already exists is a merge, which is a thing for a human to do — `orch
// doctor`'s `mcp.config` check is what tells them it is missing.
func (w *writer) mcpConfig() {
	if w.err != nil {
		return
	}
	if _, err := os.Stat(w.path(".mcp.json")); err == nil {
		return
	}
	w.write(".mcp.json", []byte(mcpJSON), 0o600)
}
