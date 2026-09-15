package providers

import "testing"

// TestResultTextFromRealCaptures pins Result.Text against what each CLI really
// printed. Every capture here answered the prompt `Reply with exactly: ok`, so
// the answer is known; a failed run's Text is empty or the CLI's own words,
// never mistaken for an answer.
func TestResultTextFromRealCaptures(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		exit     int
		fixture  []string
		want     string
	}{
		{"claude result", ClaudeProvider{}, 0, []string{"claude", "2.1.269", "success.json"}, "ok"},
		{"codex last agent_message", CodexProvider{}, 0, []string{"codex", "0.154.0", "success.jsonl"}, "ok"},
		{"codex auth error has no answer", CodexProvider{}, 1, []string{"codex", "0.154.0", "auth-error.jsonl"}, ""},
		{"opencode text part", OpencodeProvider{}, 0, []string{"opencode", "1.18.30", "success.json"}, "ok"},
		{"opencode error has no answer", OpencodeProvider{}, 1, []string{"opencode", "1.18.30", "unknown-model.json"}, ""},
		// gemini prints plain text: its notice about ripgrep comes with the
		// answer, and the verdict parser is what looks past it.
		{"gemini stdout", GeminiProvider{}, 0, []string{"gemini", "0.59.0", "success.log"},
			"Ripgrep is not available. Falling back to GrepTool.\nok"},
		{"gemini failure is not an answer", GeminiProvider{}, 41, []string{"gemini", "0.59.0", "auth-error.log"}, ""},
		{"agy response", AgyProvider{}, 0, []string{"agy", "1.2.1", "success.json"}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.provider.Parse(tt.exit, readFixture(t, tt.fixture...)).Text
			if got != tt.want {
				t.Errorf("Text = %q, want %q", got, tt.want)
			}
		})
	}
}
