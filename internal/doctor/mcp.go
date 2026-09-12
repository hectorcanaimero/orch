package doctor

import (
	"os"
	"path/filepath"
)

// CheckMCPConfig reports whether root has a .mcp.json — the file that
// tells an MCP-capable agent CLI where orch's tools live. No Python
// equivalent to port: .mcp.json predates neither orch.py nor preflight.py's
// check list, so this is new coverage the G4.5 brief asked for directly.
//
// `orch init` writes the file since G6.4, but the remediation does not tell
// an existing project to re-run it: `--force` would be needed to get past the
// conflict check, and that overwrites tasks.json. Copying one block out of
// docs/MCP.md is the smaller operation.
func CheckMCPConfig(root string) Check {
	const name = "mcp.config"
	path := filepath.Join(root, ".mcp.json")
	if _, err := os.Stat(path); err != nil {
		return Check{
			Name: name, Status: StatusWarn,
			Detail:      path + " not found — MCP-capable agents won't see orch's tools",
			Remediation: "Create .mcp.json by hand — see docs/MCP.md for the block. New projects get one from `orch init`.",
		}
	}
	return Check{Name: name, Status: StatusOK, Detail: path + " present"}
}
