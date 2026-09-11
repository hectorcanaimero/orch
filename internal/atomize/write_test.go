package atomize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func TestApplyWritesJSONWithoutBackupWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")

	tf := model.TasksFile{Tasks: []model.Task{defaultTaskLiteral()}}
	backup, err := Apply(path, tf, true)
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Errorf("backup = %q, want none for a first write", backup)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tasks.json not written: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tasks.json.bak-*"))
	if len(matches) != 0 {
		t.Errorf("unexpected backup files: %v", matches)
	}
}

func TestApplyCreatesBackupWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	if err := os.WriteFile(path, []byte(`{"meta":{},"phases":[],"tasks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tf := model.TasksFile{Tasks: []model.Task{defaultTaskLiteral()}}
	backup, err := Apply(path, tf, true)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatalf("expected a backup path")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup file not written: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tasks.json.bak-*"))
	if len(matches) != 1 {
		t.Errorf("backups = %v, want exactly one", matches)
	}
}

func TestApplyNoBackupSkipsEvenWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	if err := os.WriteFile(path, []byte(`{"meta":{},"phases":[],"tasks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(path, model.TasksFile{}, false); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tasks.json.bak-*"))
	if len(matches) != 0 {
		t.Errorf("backups = %v, want none when backup=false", matches)
	}
}

func TestApplyThenLoadTasksFileRoundtrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")

	tf := model.TasksFile{Tasks: []model.Task{defaultTaskLiteral()}}
	if _, err := Apply(path, tf, true); err != nil {
		t.Fatal(err)
	}

	loaded, err := model.LoadTasksFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tasks) != 1 || loaded.Tasks[0].ID != "F1.1.T1" {
		t.Errorf("loaded = %+v", loaded.Tasks)
	}
}

func TestLoadExistingMissingFileIsEmptyTasksFile(t *testing.T) {
	dir := t.TempDir()
	tf, err := LoadExisting(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tf.Tasks) != 0 || len(tf.Phases) != 0 {
		t.Errorf("tf = %+v, want zero value", tf)
	}
}

func defaultTaskLiteral() model.Task {
	var tk model.Task
	_ = json.Unmarshal([]byte(`{
		"id": "F1.1.T1", "phase": 1, "title": "t", "model": "m",
		"status": "backlog", "dependencies": [], "estimateHours": 0,
		"files": [], "specRef": "", "comments": []
	}`), &tk)
	return tk
}
