package agent

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	publicagent "github.com/m4ns0ur/iris/agent"
)

const gitSummaryTimeout = 2 * time.Second

// collectGitChanges observes the working tree at the end of a run. It returns
// false when workdir is not inside a Git work tree or Git is unavailable.
func collectGitChanges(workdir string) (bool, []publicagent.FileChange) {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitSummaryTimeout)
	defer cancel()

	command := exec.CommandContext(
		ctx,
		"git",
		"-C",
		workdir,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		return false, nil
	}
	return true, parseGitStatus(output)
}

func parseGitStatus(output []byte) []publicagent.FileChange {
	entries := strings.Split(string(output), "\x00")
	changes := make([]publicagent.FileChange, 0, len(entries))
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if len(entry) < 4 {
			continue
		}

		code := entry[:2]
		path := entry[3:]
		if code[0] == 'R' || code[0] == 'C' || code[1] == 'R' || code[1] == 'C' {
			if index+1 < len(entries) {
				previous := entries[index+1]
				index++
				if previous != "" {
					path = previous + " -> " + path
				}
			}
		}

		status := gitStatusLabel(code)
		if status == "" {
			continue
		}
		changes = append(changes, publicagent.FileChange{
			Status: status,
			Path:   path,
		})
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func gitStatusLabel(code string) string {
	if len(code) < 2 {
		return ""
	}
	if code == "??" {
		return "A"
	}
	if code == "!!" {
		return ""
	}

	label := code[0]
	if label == ' ' {
		label = code[1]
	}
	switch label {
	case 'A', 'M', 'D', 'R', 'C', 'T', 'U':
		return string(label)
	default:
		return "M"
	}
}

// collectRunSummary builds the terminal summary from runtime counters. It must
// run on the run goroutine before finishRun releases the active-run slot.
func (a *Agent) collectRunSummary() *publicagent.RunSummary {
	finishedAt := time.Now()
	startedAt := a.runStartedAt
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	summary := &publicagent.RunSummary{
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		DurationMS:     finishedAt.Sub(startedAt).Milliseconds(),
		LLMRequests:    a.llmRequests,
		ToolExecutions: a.toolExecutions,
		Usage:          a.runUsage,
	}

	gitRepository, files := collectGitChanges(a.workdir)
	summary.GitRepository = gitRepository
	summary.FilesChanged = files
	return summary
}
