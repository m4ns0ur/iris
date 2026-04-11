package prompt

import (
	"fmt"
	"strings"
)

const (
	Planning   Phase = "planning"
	Executing  Phase = "executing"
	Reflecting Phase = "reflecting"
)

type Phase string

func BuildSystemPrompt() string {
	return `You are Iris, an autonomous agent.

Check the user input and generate a list of tasks for you to work on.`
}

func BuildPlanningPrompt(goals []string) string {
	if len(goals) == 0 {
		return ""
	}

	goalList := make([]string, len(goals))
	for i, goal := range goals {
		goalList[i] = fmt.Sprintf("%d. %s", i+1, goal)
	}

	return fmt.Sprintf(`Current Goals:
%s

Review your progress and determine if all tasks are complete or if adjustments are needed.`, strings.Join(goalList, "\n"))
}

func BuildExecutionPrompt(index int, task string) string {
	return fmt.Sprintf(`Current Task #%d:
%s

Execute this task using available tools.`, index+1, task)
}

func BuildReflectionPrompt(currentTask int, totalTasks int) string {
	if currentTask >= totalTasks {
		return "All tasks completed successfully."
	}

	remaining := totalTasks - currentTask
	progress := float64(remaining) / float64(totalTasks) * 100

	return fmt.Sprintf(`Current Progress:
- Task #%d of %d (%.0f%% complete)
- Remaining tasks: %d

Evaluate your approach and decide if you should continue or adjust the plan.`, currentTask+1, totalTasks, progress, remaining)
}

func BuildSplitTaskPrompt(index int, task string) string {
	return fmt.Sprintf(`Current Task #%d:
%s

This task seems complex. Break it down into smaller subtasks.`, index+1, task)
}
