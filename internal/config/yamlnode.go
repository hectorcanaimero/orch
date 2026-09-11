package config

import (
	"fmt"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// decodeYAML parses YAML into a generic tree, tolerating a duplicate mapping
// key the way Python does — last one wins — and reporting each one.
//
// This exists because the config orch itself ships is affected. Since the H-2
// sprint, `orchestrator/config.yaml` defines `dashboard:` twice: once for
// `show_spend_to_stakeholder` and `summary_language`, and again 38 lines later
// for `kanban` and `tunnel`. PyYAML silently keeps the second, so the first
// block — including the documented `summary_language` knob — has never had
// any effect. Editing it does nothing, silently.
//
// Go's yaml.v3 rejects duplicate keys outright, which is the better default
// and would have caught this years ago. But `orch init` copied that file into
// every project scaffolded without a template, so refusing to load it would
// break exactly the promise ADR-G3 makes: swap the binary, keep your project.
//
// So: match Python's behaviour, and say so out loud. The warning is how the
// silent part stops being silent.
func decodeYAML(data []byte, source string) (map[string]any, []string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return map[string]any{}, nil, nil // empty file
	}

	dups := map[string]bool{}
	v, err := nodeToAny(root.Content[0], "", dups)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", source, err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return map[string]any{}, nil, nil
		}
		return nil, nil, fmt.Errorf("parse %s: the top level is %T, want a mapping", source, v)
	}

	warnings := make([]string, 0, len(dups))
	for path := range dups {
		warnings = append(warnings, fmt.Sprintf(
			"`%s` is defined more than once in %s — the last one wins, "+
				"the earlier ones are dead", path, source))
	}
	sort.Strings(warnings)
	return m, warnings, nil
}

// nodeToAny converts a YAML node to Go values, recording the dotted path of
// any duplicated mapping key in dups.
func nodeToAny(n *yaml.Node, prefix string, dups map[string]bool) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return nodeToAny(n.Content[0], prefix, dups)

	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if _, seen := out[k]; seen {
				dups[path] = true
			}
			v, err := nodeToAny(n.Content[i+1], path, dups)
			if err != nil {
				return nil, err
			}
			out[k] = v // last wins, as PyYAML does
		}
		return out, nil

	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeToAny(c, prefix, dups)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil

	case yaml.AliasNode:
		return nodeToAny(n.Alias, prefix, dups)

	case yaml.ScalarNode:
		return scalarToAny(n)
	}
	return nil, fmt.Errorf("unsupported YAML node kind %v at %q", n.Kind, prefix)
}

// scalarToAny resolves a scalar to the Go type its YAML tag implies. Going
// through the tag rather than re-guessing from the text keeps `"true"` a
// string and `true` a bool, which matters for `github.test_command: "yes"`.
func scalarToAny(n *yaml.Node) (any, error) {
	switch n.Tag {
	case "!!null":
		return nil, nil
	case "!!bool":
		b, err := strconv.ParseBool(n.Value)
		if err != nil {
			return nil, fmt.Errorf("parse bool %q: %w", n.Value, err)
		}
		return b, nil
	case "!!int":
		i, err := strconv.Atoi(n.Value)
		if err != nil {
			// Out of int range, or 0o/0x notation yaml.v3 accepts and Atoi
			// does not. Fall back to the library's own decoding.
			var v any
			if derr := n.Decode(&v); derr == nil {
				return v, nil
			}
			return nil, fmt.Errorf("parse int %q: %w", n.Value, err)
		}
		return i, nil
	case "!!float":
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("parse float %q: %w", n.Value, err)
		}
		return f, nil
	default:
		return n.Value, nil
	}
}
