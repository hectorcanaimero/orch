package mcp

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hectorcanaimero/orch/internal/vcs"
)

// FindingsRepo is where orch_report_finding files. Fixed: the findings are
// about orch, whichever project the agent happened to be working in.
const FindingsRepo = "hectorcanaimero/orch"

// FindingsLabel marks an issue an agent filed. `orch sync issues` refuses to
// ingest it, so a report never loops back into a project as a task.
const FindingsLabel = "auto-reported"

// FindingsReporter is the slice of *vcs.GitHubProvider the tool needs.
type FindingsReporter interface {
	SearchIssues(repo, label, query string, limit int) ([]vcs.Issue, error)
	CreateIssue(repo, title, body string, labels []string) (string, error)
}

var findingTypes = map[string]bool{"bug": true, "improvement": true, "feature": true}

type reportFindingIn struct {
	Type         string `json:"type" jsonschema:"bug, improvement or feature"`
	Title        string `json:"title" jsonschema:"one line naming the problem or the idea, e.g. 'orch explain lists backlog tasks as ready'"`
	Summary      string `json:"summary" jsonschema:"what happens and what should happen instead"`
	Evidence     string `json:"evidence,omitempty" jsonschema:"what you saw: log lines, command output, file:line in orch. No secrets, no project data."`
	Repro        string `json:"repro,omitempty" jsonschema:"minimal steps to reproduce, for a bug"`
	SuggestedFix string `json:"suggested_fix,omitempty" jsonschema:"where in orch and how, if you know"`
	Confidence   string `json:"confidence,omitempty" jsonschema:"high, medium or low"`
	ConfirmNew   bool   `json:"confirm_new,omitempty" jsonschema:"file even though similar issues exist; set it only after reading the ones a previous call returned"`
}

type findingIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
}

type reportFindingOut struct {
	// Filed is true when a new issue was created; URL is then its address.
	Filed bool   `json:"filed"`
	URL   string `json:"url,omitempty"`
	// Duplicate is an existing issue with the same title: nothing was filed.
	Duplicate *findingIssue `json:"duplicate,omitempty"`
	// Similar are existing issues whose titles match loosely. Without
	// confirm_new they stop the filing, so the caller reads them first.
	Similar []findingIssue `json:"similar,omitempty"`
	// Message says what happened and what to do next, for the model.
	Message string `json:"message"`
}

func (s *server) reportFinding(_ context.Context, _ *mcpsdk.CallToolRequest, in reportFindingIn) (*mcpsdk.CallToolResult, reportFindingOut, error) {
	refuse := func(msg string) (*mcpsdk.CallToolResult, reportFindingOut, error) {
		return &mcpsdk.CallToolResult{IsError: true}, reportFindingOut{Message: msg}, nil
	}
	if s.opts.Findings == nil {
		return refuse("reporting findings is off for this project. Tell the operator instead; " +
			"they can enable it with `report_findings.enabled: true` in .orchestrator/config.yaml.")
	}
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	in.Title = strings.TrimSpace(in.Title)
	if !findingTypes[in.Type] {
		return refuse(fmt.Sprintf("type %q is not one of bug, improvement, feature", in.Type))
	}
	if in.Title == "" || strings.TrimSpace(in.Summary) == "" {
		return refuse("title and summary are required")
	}

	existing, err := s.opts.Findings.SearchIssues(FindingsRepo, FindingsLabel, in.Title, 10)
	if err != nil {
		return nil, reportFindingOut{}, fmt.Errorf("searching %s for similar issues: %w", FindingsRepo, err)
	}
	var similar []findingIssue
	for _, is := range existing {
		fi := findingIssue{Number: is.Number, Title: is.Title, State: is.State, URL: is.URL}
		if normalizeTitle(is.Title) == normalizeTitle(in.Title) {
			return nil, reportFindingOut{Duplicate: &fi,
				Message: fmt.Sprintf("already reported as #%d (%s); nothing filed", is.Number, is.State)}, nil
		}
		similar = append(similar, fi)
	}
	if len(similar) > 0 && !in.ConfirmNew {
		return nil, reportFindingOut{Similar: similar,
			Message: "similar issues exist; nothing filed. If none of them is this finding, " +
				"call again with confirm_new: true."}, nil
	}

	body := s.redact(findingBody(in, s.opts.Version, time.Now()))
	url, err := s.opts.Findings.CreateIssue(FindingsRepo, s.redact(in.Title), body, []string{FindingsLabel})
	if err != nil {
		return nil, reportFindingOut{}, fmt.Errorf("filing the finding in %s: %w", FindingsRepo, err)
	}
	return nil, reportFindingOut{Filed: true, URL: url, Similar: similar, Message: "filed " + url}, nil
}

var reNonWord = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeTitle is what two titles must share to be the same report.
func normalizeTitle(t string) string {
	return strings.Trim(reNonWord.ReplaceAllString(strings.ToLower(t), " "), " ")
}

// findingBody lays a finding out the way the reports filed by hand were.
func findingBody(in reportFindingIn, version string, now time.Time) string {
	if version == "" {
		version = "unknown"
	}
	confidence := in.Confidence
	if confidence == "" {
		confidence = "unstated"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Type**: %s\n**About**: orch\n**Confidence**: %s\n**Captured at**: %s\n**Env**: orch `%s`, %s/%s\n",
		in.Type, confidence, now.UTC().Format(time.RFC3339), version, runtime.GOOS, runtime.GOARCH)
	for _, sec := range []struct{ head, text string }{
		{"Summary", in.Summary}, {"Evidence", in.Evidence},
		{"Repro", in.Repro}, {"Suggested fix", in.SuggestedFix},
	} {
		if strings.TrimSpace(sec.text) != "" {
			fmt.Fprintf(&b, "\n## %s\n%s\n", sec.head, strings.TrimSpace(sec.text))
		}
	}
	b.WriteString("\n---\n_Reported by an orch agent through `orch_report_finding`._\n")
	return b.String()
}

// redact keeps the operator's machine out of a public issue: the project
// root becomes `<project>` and the home directory `~`. It cannot know what
// else is private, which is why the tool's description asks for no project
// data at all.
func (s *server) redact(text string) string {
	if root := s.opts.ProjectRoot; root != "" {
		text = strings.ReplaceAll(text, root, "<project>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		text = strings.ReplaceAll(text, home, "~")
	}
	return text
}
