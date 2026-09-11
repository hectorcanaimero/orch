package cli

import (
	"encoding/json"
	"strconv"
	"strings"
)

// orderedCount is an insertion-ordered string→int map. It exists because
// Python's `totals` dict in `build_status_snapshot` accumulates in the
// order statuses are FIRST SEEN while walking tasks.json, and `json.dumps`
// preserves that order — but Go's encoding/json always sorts a plain map's
// keys alphabetically, which would silently reorder the wire output and
// break scripts/parity.sh's diff.
type orderedCount struct {
	keys   []string
	values map[string]int
}

func newOrderedCount() *orderedCount {
	return &orderedCount{values: map[string]int{}}
}

// Add increments key by delta, remembering the order keys were first added.
func (m *orderedCount) Add(key string, delta int) {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] += delta
}

// MarshalJSON writes {"k1":v1,"k2":v2,...} in insertion order.
func (m *orderedCount) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(m.values[k]))
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}
