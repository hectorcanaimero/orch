package doctor

import (
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

func TestCheckRoutesSkipsWithNoTasks(t *testing.T) {
	c := CheckRoutes(nil, router.Router{"x": {}})
	if c.Status != StatusSkip {
		t.Errorf("c = %+v, want skip", c)
	}
}

func TestCheckRoutesSkipsWithNoRouter(t *testing.T) {
	c := CheckRoutes([]model.Task{{ID: "T1", Model: "x"}}, nil)
	if c.Status != StatusSkip {
		t.Errorf("c = %+v, want skip", c)
	}
}

func TestCheckRoutesOKWhenAllResolve(t *testing.T) {
	tasks := []model.Task{{ID: "T1", Model: "claude/opus"}}
	r := router.Router{"claude/opus": model.RouteEntry{Backend: model.BackendClaude}}
	c := CheckRoutes(tasks, r)
	if c.Status != StatusOK {
		t.Errorf("c = %+v, want ok", c)
	}
}

func TestCheckRoutesErrorsOnUnresolved(t *testing.T) {
	tasks := []model.Task{{ID: "T1", Model: "nope/nope"}}
	r := router.Router{"claude/opus": model.RouteEntry{Backend: model.BackendClaude}}
	c := CheckRoutes(tasks, r)
	if c.Status != StatusError {
		t.Errorf("c = %+v, want error", c)
	}
	if c.Remediation == "" {
		t.Errorf("expected a remediation hint")
	}
}
