package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// CheckClaudeDoneChannel warns when the project's own claude settings deny a
// channel the dispatch prompt names for reporting a task done (#328).
//
// Either denial alone is enough to hurt: an agent with orch's MCP tools that
// cannot see orch_set_status is left with orch_block, and one without them
// falls back to scripts/task-finish.sh. Claude hides a denied MCP tool rather
// than refusing the call, which is how #336 read "the tool does not exist".
// orch writes neither file; this only says which rule to remove.
func CheckClaudeDoneChannel(root string) Check {
	const name = "claude.done_channel"
	var denied []string
	for _, file := range []string{"settings.json", "settings.local.json"} {
		raw, err := os.ReadFile(filepath.Join(root, ".claude", file)) // #nosec G304 -- the project's own settings file
		if err != nil {
			continue
		}
		var doc struct {
			Permissions struct {
				Deny []string `json:"deny"`
			} `json:"permissions"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for _, rule := range doc.Permissions.Deny {
			if deniesDoneChannel(rule) {
				denied = append(denied, ".claude/"+file+": "+rule)
			}
		}
	}
	if len(denied) == 0 {
		return Check{Name: name, Status: StatusOK, Detail: "no claude settings deny task-finish.sh or orch_set_status"}
	}
	return Check{
		Name: name, Status: StatusWarn,
		Detail: "claude settings deny a done channel the dispatch prompt names — " + strings.Join(denied, "; "),
		Remediation: "Remove the rule from permissions.deny (allow `Bash(scripts/task-finish.sh:*)` and `mcp__orch__orch_set_status`). " +
			"Without them an agent can only exit and let orch record the done, and its note never reaches the tasks that depend on it.",
	}
}

// deniesDoneChannel reports whether a claude deny rule covers
// scripts/task-finish.sh or mcp__orch__orch_set_status.
func deniesDoneChannel(rule string) bool {
	switch rule {
	case "Bash", "Bash(*)", "mcp__orch", "mcp__orch__*", "mcp__orch__orch_set_status":
		return true
	}
	return strings.HasPrefix(rule, "Bash(") && strings.Contains(rule, "task-finish.sh")
}
