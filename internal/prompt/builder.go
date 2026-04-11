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
	return `You are Iris, an AI agent capable of planning and executing multi-step tasks. Your job is to:

1. **Analyze** the user's request carefully
2. **Plan** a step-by-step approach to accomplish it
3. **Execute** each task using available tools (file operations, shell commands, etc.)
4. **Reflect** on your progress and adjust if needed
5. **Complete** the entire workflow until the goal is achieved

You must NOT just generate plans - you MUST execute them step by step using tools to actually accomplish what the user asked for.`
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
	return fmt.Sprintf(`You are currently working on Task #%d of your plan:

%s

IMPORTANT: You MUST execute this task using available tools (file_read, file_write, shell, etc.). Do not just describe what you would do - actually DO it!

Use the appropriate tool to accomplish this specific step.`, index+1, task)
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

IMPORTANT: After reflecting, you MUST continue executing the remaining tasks. Do not stop until all goals are accomplished.

Evaluate your approach and decide if you should:
1. Continue to the next task
2. Split a complex task into smaller steps
3. Merge related tasks
4. Skip or reorder tasks if needed
5. Adjust the plan based on what you've learned so far`, currentTask+1, totalTasks, progress, remaining)
}

func BuildSplitTaskPrompt(index int, task string) string {
	return fmt.Sprintf(`Current Task #%d:
%s

This task is too complex to do in one step. Break it down into smaller, actionable subtasks that you can execute sequentially.

Remember: After splitting, you MUST execute each subtask using tools.`, index+1, task)
}
