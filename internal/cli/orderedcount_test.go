package cli

import "testing"

func TestOrderedCountPreservesFirstSeenOrder(t *testing.T) {
	m := newOrderedCount()
	m.Add("done", 1)
	m.Add("in-progress", 1)
	m.Add("done", 1) // repeat: bumps the value, doesn't move the key
	m.Add("blocked", 1)
	m.Add("_total", 4)

	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"done":2,"in-progress":1,"blocked":1,"_total":4}`
	if got := string(b); got != want {
		t.Errorf("MarshalJSON() = %s, want %s", got, want)
	}
}

func TestOrderedCountEmpty(t *testing.T) {
	m := newOrderedCount()
	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Errorf("MarshalJSON() on empty = %s, want {}", b)
	}
}
