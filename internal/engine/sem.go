package engine

import "sync"

// Sem is a counting semaphore with an observable count. Port of orch.py's
// `_Sem`.
//
// Deliberately not `golang.org/x/sync/semaphore`, which the G3.1 brief
// suggested. That package has `TryAcquire`, but no way to read how much is
// currently held — and the current in-flight count is the whole point of
// AS-05, which is asserted between ticks and is what the concurrency test
// checks. Using it would mean keeping this counter alongside it anyway, so it
// would add a dependency and a second source of truth to save nothing.
//
// Acquisition is always non-blocking: a tick that cannot get a slot skips the
// task and the next tick retries. Blocking here would stall the reap loop
// that is the only thing able to free a slot.
type Sem struct {
	cap   int
	mu    sync.Mutex
	count int
}

// NewSem returns a semaphore with the given capacity. A capacity of zero or
// less admits nothing, which is what a config of `global_max: 0` asks for.
func NewSem(capacity int) *Sem { return &Sem{cap: capacity} }

// TryAcquire takes a slot if one is free, reporting whether it did.
func (s *Sem) TryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count >= s.cap {
		return false
	}
	s.count++
	return true
}

// Release returns a slot. Releasing more than was acquired floors at zero
// rather than going negative, as Python's `max(0, ...)` does: a double
// release is a bug, but one that must not hand out phantom capacity.
func (s *Sem) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count > 0 {
		s.count--
	}
}

// Current is how many slots are held right now.
func (s *Sem) Current() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Cap is the configured ceiling.
func (s *Sem) Cap() int { return s.cap }

// Sems is the pair of gates every dispatch passes: one global ceiling across
// every backend, and one per backend (FR-D-5).
type Sems struct {
	Global   *Sem
	Provider map[string]*Sem
}

// NewSems builds the semaphores from a config's concurrency block.
func NewSems(globalMax int, perProvider map[string]int) *Sems {
	s := &Sems{
		Global:   NewSem(globalMax),
		Provider: make(map[string]*Sem, len(perProvider)),
	}
	for backend, capacity := range perProvider {
		s.Provider[backend] = NewSem(capacity)
	}
	return s
}

// TryAcquire takes a slot for one backend, or reports that it could not.
//
// The provider gate is checked first and released if the global gate then
// refuses, which is Python's order. It matters: doing it the other way lets a
// full provider consume and return a global slot on every tick, so a backend
// that is at its cap would keep flapping the global count that AS-05 reads.
//
// An unknown backend is refused rather than admitted. A router entry naming a
// provider with no configured cap is a config error, and treating "no cap
// configured" as "no limit" is how a global_max quietly stops being a
// ceiling.
func (s *Sems) TryAcquire(backend string) bool {
	psem, ok := s.Provider[backend]
	if !ok {
		return false
	}
	if !psem.TryAcquire() {
		return false
	}
	if !s.Global.TryAcquire() {
		psem.Release()
		return false
	}
	return true
}

// Release returns both slots taken by a successful TryAcquire.
func (s *Sems) Release(backend string) {
	if psem, ok := s.Provider[backend]; ok {
		psem.Release()
	}
	s.Global.Release()
}

// Knows reports whether a backend has a configured cap.
func (s *Sems) Knows(backend string) bool {
	_, ok := s.Provider[backend]
	return ok
}
