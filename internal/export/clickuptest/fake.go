// Package clickuptest is an in-memory ClickUp REST API for tests: the subset
// of endpoints internal/export uses, with the same request and answer shapes.
// Both internal/export's and internal/cli's tests drive it, so it is not a
// _test file.
package clickuptest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Token is the only Authorization value the fake accepts.
const Token = "pk_test"

// Task is a task as the fake stores it.
type Task struct {
	ID, ListID, Name, Markdown, Status string
	TimeEstimate                       int64
	Fields                             map[string]any
	DependsOn                          []string
	Comments                           []string
}

// Field is a custom field on every List of the fake.
type Field struct {
	ID, Name, Type string
	// Options are drop-down option names; an option's id is "opt-<index>"
	// and its orderindex the index.
	Options []string
}

// Server is the fake. Lock mu around direct reads of its maps.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	FolderID string
	Lists    map[string]*List
	Tasks    map[string]*Task
	Fields   []Field
	// Writes counts every request that is not a GET.
	Writes int
	// RateLimitOnce answers the next request with a 429, once.
	RateLimitOnce bool
	// Remaining, when set, is sent as X-RateLimit-Remaining on every answer.
	Remaining string
	// FieldQuotaExceeded refuses every custom-field write, on its own or
	// inside a task creation, the way ClickUp's Free plan does once its
	// custom-field quota is used up.
	FieldQuotaExceeded bool
	nextID        int
}

// List is a List as the fake stores it.
type List struct {
	ID, Name, Content string
	Statuses          []string
	order             int
}

// DefaultStatuses are the statuses orch's defaults need.
var DefaultStatuses = []string{"backlog", "to do", "in progress", "blocked", "complete"}

// New starts a fake with one empty Folder.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{FolderID: "folder-1", Lists: map[string]*List{}, Tasks: map[string]*Task{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// URL is the base URL to point the client at.
func (s *Server) URL() string { return s.Server.URL }

// AddList creates a List directly, as if made in ClickUp's UI.
func (s *Server) AddList(name string, statuses ...string) *List {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(statuses) == 0 {
		statuses = DefaultStatuses
	}
	s.nextID++
	l := &List{ID: fmt.Sprintf("list-%d", s.nextID), Name: name, Statuses: statuses, order: s.nextID}
	s.Lists[l.ID] = l
	return l
}

// AddTask creates a task directly, as if made by hand or by an older tool.
func (s *Server) AddTask(listID, name, markdown, status string, fields map[string]any) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	t := &Task{ID: fmt.Sprintf("task-%d", s.nextID), ListID: listID, Name: name, Markdown: markdown, Status: status, Fields: fields}
	if t.Fields == nil {
		t.Fields = map[string]any{}
	}
	s.Tasks[t.ID] = t
	return t
}

// TaskByName finds a task by exact name.
func (s *Server) TaskByName(name string) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Tasks {
		if t.Name == name {
			return t
		}
	}
	return nil
}

var (
	reFolderLists = regexp.MustCompile(`^/folder/([^/]+)/list$`)
	reList        = regexp.MustCompile(`^/list/([^/]+)$`)
	reListFields  = regexp.MustCompile(`^/list/([^/]+)/field$`)
	reListTasks   = regexp.MustCompile(`^/list/([^/]+)/task$`)
	reTask        = regexp.MustCompile(`^/task/([^/]+)$`)
	reTaskField   = regexp.MustCompile(`^/task/([^/]+)/field/([^/]+)$`)
	reTaskDep     = regexp.MustCompile(`^/task/([^/]+)/dependency$`)
	reTaskComment = regexp.MustCompile(`^/task/([^/]+)/comment$`)
)

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Remaining != "" {
		w.Header().Set("X-RateLimit-Remaining", s.Remaining)
		w.Header().Set("X-RateLimit-Reset", "0")
	}
	if r.Header.Get("Authorization") != Token {
		http.Error(w, `{"err":"Token invalid","ECODE":"OAUTH_025"}`, http.StatusUnauthorized)
		return
	}
	if s.RateLimitOnce {
		s.RateLimitOnce = false
		w.Header().Set("X-RateLimit-Reset", "0")
		http.Error(w, `{"err":"Rate limit reached"}`, http.StatusTooManyRequests)
		return
	}
	if r.Method != http.MethodGet {
		s.Writes++
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	p := r.URL.Path
	switch {
	case reFolderLists.MatchString(p) && r.Method == http.MethodGet:
		lists := []map[string]any{}
		for i := 1; i <= s.nextID; i++ {
			for _, l := range s.Lists {
				if l.order == i {
					lists = append(lists, map[string]any{"id": l.ID, "name": l.Name})
				}
			}
		}
		writeJSON(w, map[string]any{"lists": lists})
	case reFolderLists.MatchString(p) && r.Method == http.MethodPost:
		s.nextID++
		l := &List{ID: fmt.Sprintf("list-%d", s.nextID), Name: str(body["name"]), Content: str(body["content"]),
			Statuses: DefaultStatuses, order: s.nextID}
		s.Lists[l.ID] = l
		writeJSON(w, map[string]any{"id": l.ID, "name": l.Name})
	case reListFields.MatchString(p):
		fields := []map[string]any{}
		for _, f := range s.Fields {
			opts := []map[string]any{}
			for i, o := range f.Options {
				opts = append(opts, map[string]any{"id": "opt-" + strconv.Itoa(i), "name": o, "orderindex": i})
			}
			fields = append(fields, map[string]any{"id": f.ID, "name": f.Name, "type": f.Type,
				"type_config": map[string]any{"options": opts}})
		}
		writeJSON(w, map[string]any{"fields": fields})
	case reList.MatchString(p):
		l := s.Lists[reList.FindStringSubmatch(p)[1]]
		if l == nil {
			http.Error(w, `{"err":"List not found"}`, http.StatusNotFound)
			return
		}
		sts := []map[string]any{}
		for _, st := range l.Statuses {
			sts = append(sts, map[string]any{"status": st})
		}
		writeJSON(w, map[string]any{"id": l.ID, "name": l.Name, "statuses": sts})
	case reListTasks.MatchString(p) && r.Method == http.MethodGet:
		listID := reListTasks.FindStringSubmatch(p)[1]
		tasks := []map[string]any{}
		for _, t := range s.Tasks {
			if t.ListID == listID && r.URL.Query().Get("page") == "0" {
				tasks = append(tasks, s.taskJSON(t))
			}
		}
		writeJSON(w, map[string]any{"tasks": tasks, "last_page": true})
	case reListTasks.MatchString(p) && r.Method == http.MethodPost:
		listID := reListTasks.FindStringSubmatch(p)[1]
		l := s.Lists[listID]
		if !hasStatus(l, str(body["status"])) {
			http.Error(w, `{"err":"Status does not exist","ECODE":"CRTSK_001"}`, http.StatusBadRequest)
			return
		}
		if _, withFields := body["custom_fields"]; withFields && s.FieldQuotaExceeded {
			http.Error(w, fieldQuotaBody, http.StatusBadRequest)
			return
		}
		s.nextID++
		t := &Task{ID: fmt.Sprintf("task-%d", s.nextID), ListID: listID, Name: str(body["name"]),
			Markdown: str(body["markdown_content"]), Status: str(body["status"]), Fields: map[string]any{}}
		if est, ok := body["time_estimate"].(float64); ok {
			t.TimeEstimate = int64(est)
		}
		if cfs, ok := body["custom_fields"].([]any); ok {
			for _, cf := range cfs {
				m, _ := cf.(map[string]any)
				t.Fields[str(m["id"])] = s.stored(str(m["id"]), m["value"])
			}
		}
		s.Tasks[t.ID] = t
		writeJSON(w, s.taskJSON(t))
	case reTaskField.MatchString(p):
		if s.FieldQuotaExceeded {
			http.Error(w, fieldQuotaBody, http.StatusBadRequest)
			return
		}
		m := reTaskField.FindStringSubmatch(p)
		if t := s.Tasks[m[1]]; t != nil {
			t.Fields[m[2]] = s.stored(m[2], body["value"])
		}
		writeJSON(w, map[string]any{})
	case reTaskDep.MatchString(p):
		if t := s.Tasks[reTaskDep.FindStringSubmatch(p)[1]]; t != nil {
			t.DependsOn = append(t.DependsOn, str(body["depends_on"]))
		}
		writeJSON(w, map[string]any{})
	case reTaskComment.MatchString(p):
		if t := s.Tasks[reTaskComment.FindStringSubmatch(p)[1]]; t != nil {
			t.Comments = append(t.Comments, str(body["comment_text"]))
		}
		writeJSON(w, map[string]any{"id": "c"})
	case reTask.MatchString(p) && r.Method == http.MethodPut:
		t := s.Tasks[reTask.FindStringSubmatch(p)[1]]
		if t == nil || !hasStatus(s.Lists[t.ListID], str(body["status"])) {
			http.Error(w, `{"err":"Status does not exist"}`, http.StatusBadRequest)
			return
		}
		t.Status = str(body["status"])
		writeJSON(w, s.taskJSON(t))
	default:
		http.Error(w, `{"err":"Route not found"}`, http.StatusNotFound)
	}
}

// fieldQuotaBody is ClickUp's answer once a plan's custom-field quota is used.
const fieldQuotaBody = `{"err":"Custom field usages exceeded for your plan","ECODE":"FIELD_033"}`

// stored is how ClickUp reads a value back: a drop-down's orderindex.
func (s *Server) stored(fieldID string, v any) any {
	for _, f := range s.Fields {
		if f.ID == fieldID && f.Type == "drop_down" {
			if id, ok := v.(string); ok && strings.HasPrefix(id, "opt-") {
				n, _ := strconv.Atoi(strings.TrimPrefix(id, "opt-"))
				return n
			}
		}
	}
	return v
}

func (s *Server) taskJSON(t *Task) map[string]any {
	cfs := []map[string]any{}
	for id, v := range t.Fields {
		cfs = append(cfs, map[string]any{"id": id, "value": v})
	}
	deps := []map[string]any{}
	for _, d := range t.DependsOn {
		deps = append(deps, map[string]any{"task_id": t.ID, "depends_on": d, "type": 1})
	}
	return map[string]any{
		"id": t.ID, "name": t.Name, "url": "https://app.clickup.com/t/" + t.ID,
		"description": t.Markdown, "markdown_description": t.Markdown,
		"status": map[string]any{"status": t.Status}, "custom_fields": cfs, "dependencies": deps,
	}
}

func hasStatus(l *List, status string) bool {
	if l == nil {
		return false
	}
	for _, st := range l.Statuses {
		if strings.EqualFold(st, status) {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
