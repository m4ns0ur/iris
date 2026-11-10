package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

func TestRunSummaryFormat(t *testing.T) {
	summary := &RunSummary{
		StartedAt:      time.Now().Add(-48 * time.Second),
		FinishedAt:     time.Now(),
		DurationMS:     48_000,
		LLMRequests:    5,
		ToolExecutions: 8,
		Usage:          provider.Usage{PromptTokens: 10_000, CompletionTokens: 2_450, TotalTokens: 12_450},
		GitRepository:  true,
		FilesChanged: []FileChange{
			{Status: "M", Path: "internal/agent/runner.go"},
			{Status: "A", Path: "internal/session/recovery_test.go"},
		},
	}

	text := summary.Format("Task completed")
	for _, want := range []string{
		"Task completed",
		"Files changed:",
		"  M internal/agent/runner.go",
		"  A internal/session/recovery_test.go",
		"Tool executions: 8",
		"LLM requests: 5",
		"Tokens consumed: 12,450",
		"Duration: 48s",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q:\n%s", want, text)
		}
	}
}

func TestRunSummaryFormatCleanAndNonGit(t *testing.T) {
	clean := &RunSummary{GitRepository: true, DurationMS: 1_000}
	if text := clean.Format("Task completed"); !strings.Contains(text, "Files changed:\n  none") {
		t.Fatalf("clean repository summary = %q", text)
	}

	nonGit := &RunSummary{DurationMS: 1_000}
	if text := nonGit.Format("Run canceled"); strings.Contains(text, "Files changed:") {
		t.Fatalf("non-git summary claimed files: %q", text)
	} else if !strings.Contains(text, "Run canceled") {
		t.Fatalf("outcome missing: %q", text)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                           "0ms",
		250 * time.Millisecond:      "250ms",
		48 * time.Second:            "48s",
		72 * time.Second:            "1m 12s",
		2*time.Hour + 3*time.Minute: "2h 3m",
	}
	for duration, want := range cases {
		if got := formatDuration(duration); got != want {
			t.Fatalf("formatDuration(%s) = %q, want %q", duration, got, want)
		}
	}
}
