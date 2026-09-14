package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// defaultTestCommand is the packaged config's `github.test_command`.
const defaultTestCommand = "pytest"

var reTestCommand = regexp.MustCompile(`(?m)^(\s*test_command:[ \t]*)([^#\n]*?)[ \t]*(#.*)?$`)

// testCommand is config.yaml's `github.test_command`, after replacing the
// packaged `pytest` with what the repo's own files imply: a template names
// its command on purpose, the packaged default only guesses.
func (w *writer) testCommand() string {
	rel := filepath.Join(".orchestrator", "config.yaml")
	raw, err := os.ReadFile(w.path(rel)) // #nosec G304 -- the file this scaffold just wrote
	if err != nil {
		return defaultTestCommand
	}
	m := reTestCommand.FindStringSubmatchIndex(string(raw))
	if m == nil {
		return defaultTestCommand
	}
	current := strings.TrimSpace(string(raw[m[4]:m[5]]))
	if current != defaultTestCommand {
		return current
	}
	detected := repoTestCommand(w.root)
	if detected == current {
		return current
	}
	w.write(rel, []byte(string(raw[:m[4]])+detected+string(raw[m[5]:])), 0o600)
	return detected
}

// repoTestCommand guesses the test command from the files at the root.
func repoTestCommand(root string) string {
	switch {
	case exists(root, "go.mod"):
		return "go test ./..."
	case exists(root, "package.json") && exists(root, "pnpm-lock.yaml"):
		return "pnpm test"
	case exists(root, "package.json") && exists(root, "yarn.lock"):
		return "yarn test"
	case exists(root, "package.json"):
		return "npm test"
	}
	return defaultTestCommand
}

// setupSteps are the workflow steps that install the toolchain and the
// dependencies the test command needs, picked by the command's first word.
func setupSteps(root, testCommand string) string {
	const node = "      - uses: actions/setup-node@v4\n" +
		"        with:\n" +
		"          node-version: 22\n"
	fields := strings.Fields(testCommand)
	first := ""
	if len(fields) > 0 {
		first = fields[0]
	}
	switch first {
	case "go":
		return "      - uses: actions/setup-go@v5\n" +
			"        with:\n" +
			"          go-version-file: go.mod"
	case "pnpm":
		return node +
			"      - name: Install dependencies\n" +
			"        run: npm install -g pnpm && pnpm install --frozen-lockfile"
	case "yarn":
		return node +
			"      - name: Install dependencies\n" +
			"        run: yarn install --frozen-lockfile"
	case "npm", "npx", "node":
		install := "npm install"
		if exists(root, "package-lock.json") {
			install = "npm ci"
		}
		return node +
			"      - name: Install dependencies\n" +
			"        run: " + install
	}
	return "      - uses: actions/setup-python@v5\n" +
		"        with:\n" +
		"          python-version: \"3.11\"\n" +
		"      - name: Install dependencies\n" +
		"        run: pip install -e .[dev]"
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
