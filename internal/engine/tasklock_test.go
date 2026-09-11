package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTaskLockExcludesASecondHolder(t *testing.T) {
	dir := t.TempDir()

	first, err := TryAcquireTaskLock(dir, "B-020")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first == nil {
		t.Fatal("the first acquire should succeed on a free task")
	}

	second, err := TryAcquireTaskLock(dir, "B-020")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second != nil {
		second.Release()
		t.Fatal("a second holder got the same task's lock")
	}

	first.Release()

	third, err := TryAcquireTaskLock(dir, "B-020")
	if err != nil {
		t.Fatalf("third acquire: %v", err)
	}
	if third == nil {
		t.Fatal("the lock was not released")
	}
	third.Release()
}

// TestTaskLockIsPerTask: the whole point of --task-locks is that two orch
// instances can share a project as long as they take different tasks.
func TestTaskLockIsPerTask(t *testing.T) {
	dir := t.TempDir()

	a, err := TryAcquireTaskLock(dir, "A-1")
	if err != nil || a == nil {
		t.Fatalf("A-1: lock=%v err=%v", a, err)
	}
	defer a.Release()

	b, err := TryAcquireTaskLock(dir, "B-1")
	if err != nil {
		t.Fatalf("B-1: %v", err)
	}
	if b == nil {
		t.Fatal("a different task's lock should be free")
	}
	b.Release()
}

// TestTaskLockRecordsThePID so a human can answer "who holds this?".
func TestTaskLockRecordsThePID(t *testing.T) {
	dir := t.TempDir()
	lock, err := TryAcquireTaskLock(dir, "B-020")
	if err != nil || lock == nil {
		t.Fatalf("acquire: lock=%v err=%v", lock, err)
	}
	defer lock.Release()

	b, err := os.ReadFile(filepath.Join(dir, TaskLocksDir, "B-020.lock")) // #nosec G304 -- a temp dir this test just created
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	want := "pid=" + strconv.Itoa(os.Getpid())
	if !strings.Contains(string(b), want) {
		t.Errorf("lock file holds %q, want it to contain %q", b, want)
	}
}

// TestTaskLockReleaseIsSafeOnNilAndTwice: callers do not guard the
// "task locks are off" case, and the reaper may reach Release on more than
// one path.
func TestTaskLockReleaseIsSafeOnNilAndTwice(t *testing.T) {
	var nilLock *TaskLock
	nilLock.Release() // must not panic
	if got := nilLock.Path(); got != "" {
		t.Errorf("Path on a nil lock = %q", got)
	}

	lock, err := TryAcquireTaskLock(t.TempDir(), "B-020")
	if err != nil || lock == nil {
		t.Fatalf("acquire: lock=%v err=%v", lock, err)
	}
	lock.Release()
	lock.Release() // must not panic, must not close someone else's fd
}

func TestTaskLockPath(t *testing.T) {
	dir := t.TempDir()
	lock, err := TryAcquireTaskLock(dir, "B-020")
	if err != nil || lock == nil {
		t.Fatalf("acquire: lock=%v err=%v", lock, err)
	}
	defer lock.Release()

	want := filepath.Join(dir, TaskLocksDir, "B-020.lock")
	if got := lock.Path(); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// TestTaskLockErrorsOnAnUnusableStateDir: contention is (nil, nil); a real
// problem is an error, and the two must not be confused.
func TestTaskLockErrorsOnAnUnusableStateDir(t *testing.T) {
	// A regular file where the lock directory should go.
	root := t.TempDir()
	blocker := filepath.Join(root, TaskLocksDir)
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := TryAcquireTaskLock(root, "B-020")
	if err == nil {
		if lock != nil {
			lock.Release()
		}
		t.Fatal("want an error when the lock directory cannot be created")
	}
	if lock != nil {
		lock.Release()
		t.Error("an error must not come with a lock")
	}
}
