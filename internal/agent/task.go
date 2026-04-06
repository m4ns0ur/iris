package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/prompt"
)

func (a *Agent) runTask() error {
	// Stub: Execute the current task
	// This should call appropriate tools based on the goal

	task := a.goals[a.currentTask]

	Logger.Debug("Running task", "task", task)

	// Stub: Implement tool selection and execution
	// - Analyze the task description
	// - Select appropriate tools (file read, write, etc.)
	// - Execute tools and capture results

	return nil
}

func (a *Agent) isTaskComplete() bool {
	if a.currentTask >= len(a.goals) {
		return true
	}

	if a.state == "reflecting" && a.currentTask > 0 {
		for i := len(a.history) - 1; i >= 0 && i >= len(a.history)-5; i-- {
			msg := a.history[i]
			if msg.Role == "assistant" && (strings.Contains(msg.Content, "completed") ||
				strings.Contains(msg.Content, "finished") ||
				strings.Contains(msg.Content, "done")) {
				return true
			}
		}
	}

	if a.step > len(a.goals)*2 && a.state == "reflecting" {
		Logger.Warn("Task stuck in reflection, marking as complete")
		return true
	}

	return false
}

func (a *Agent) skipTask(taskIndex int, reason string) error {
	if taskIndex < 0 || taskIndex >= len(a.goals) {
		return fmt.Errorf("invalid task index: %d", taskIndex)
	}

	Logger.Info("Skipping task", "index", taskIndex, "reason", reason)
	Logger.Debug("Task being skipped", "task", a.goals[taskIndex])

	// Remove the task from the list (shift remaining tasks down by one index)
	if len(a.goals) > taskIndex {
		a.goals = append(a.goals[:taskIndex], a.goals[taskIndex+1:]...)
	}

	// Adjust currentTask if we skipped ahead of it
	if a.currentTask >= taskIndex && a.currentTask < len(a.goals) {
		a.currentTask--
	}

	Logger.Info("Task removed", "newCount", len(a.goals))

	return nil
}

func (a *Agent) reorderTasks(taskIndex int, reason string) error {
	if taskIndex < 0 || taskIndex >= len(a.goals) {
		return fmt.Errorf("invalid task index: %d", taskIndex)
	}

	Logger.Info("Reordering tasks", "index", taskIndex, "reason", reason)

	currentTask := a.goals[taskIndex]

	// Remove from current position
	a.goals = append(a.goals[:taskIndex], a.goals[taskIndex+1:]...)

	// Insert at the beginning
	a.goals = append([]string{currentTask}, a.goals...)

	Logger.Info("Moved task to front", "index", taskIndex, "task", currentTask)

	return nil
}

func (a *Agent) mergeTasks(taskIndices ...int) error {
	if len(taskIndices) < 2 {
		return fmt.Errorf("Need at least 2 tasks to merge")
	}

	Logger.Info("Merging tasks", "indices", taskIndices)

	// Sort indices in descending order to remove from end first
	sortedIndices := make([]int, len(taskIndices))
	copy(sortedIndices, taskIndices)
	for i := 0; i < len(sortedIndices)-1; i++ {
		if sortedIndices[i] > sortedIndices[i+1] {
			sortedIndices[i], sortedIndices[i+1] = sortedIndices[i+1], sortedIndices[i]
		}
	}

	// Collect tasks to merge and build merged description
	var mergedTask string
	for _, idx := range taskIndices {
		if idx >= 0 && idx < len(a.goals) {
			mergedTask += a.goals[idx] + "\n"
		}
	}

	// Remove tasks from sorted indices (descending order to avoid index shifts)
	for _, idx := range sortedIndices {
		if idx >= 0 && idx < len(a.goals) {
			a.goals = append(a.goals[:idx], a.goals[idx+1:]...)
		}
	}

	// Add merged task back to goals list
	mergedTask = strings.TrimSpace(mergedTask)
	if mergedTask != "" {
		a.goals = append(a.goals, mergedTask)
		Logger.Info("Tasks merged successfully", "count", len(taskIndices), "newTotalGoals", len(a.goals))
	}

	// Adjust currentTask if we're on or past the merged tasks
	if a.currentTask >= 0 {
		for _, idx := range sortedIndices {
			if a.currentTask == idx || (a.currentTask > idx && a.currentTask < len(a.goals)) {
				a.currentTask--
			}
		}
	}

	return nil
}

func (a *Agent) splitTask(taskIndex int, reason string) error {
	if taskIndex < 0 || taskIndex >= len(a.goals) {
		return fmt.Errorf("invalid task index: %d", taskIndex)
	}

	Logger.Info("Splitting task", "index", taskIndex, "reason", reason)

	subtaskPrompt := prompt.BuildSplitTaskPrompt(taskIndex, a.goals[taskIndex])

	opts := &llm.LLMOptions{
		Temperature:       0.3,
		TopP:              0.95,
		RepetitionPenalty: 1.05,
	}

	resp, err := a.client.Generate(prompt.BuildSystemPrompt(), subtaskPrompt, opts)
	if err != nil {
		return fmt.Errorf("Failed to generate subtasks: %w", err)
	}

	Logger.Debug("Subtask generation response", "length", len(resp))

	subtasks, err := a.extractSubtasks(resp)
	if err != nil {
		return fmt.Errorf("Failed to parse subtasks: %w", err)
	}

	Logger.Info("Subtasks extracted", "count", len(subtasks))

	// Insert subtasks at the current task index, replacing the original task
	if len(subtasks) > 0 {
		// Remove the original task
		a.goals = append(a.goals[:taskIndex], a.goals[taskIndex+1:]...)

		// Insert subtasks in place of the original task
		for _, subtask := range subtasks {
			a.goals = append(a.goals, subtask)
		}

		Logger.Info("Task split successfully", "originalIndex", taskIndex, "subtaskCount", len(subtasks))

		// Adjust currentTask if we're currently on this task or past it
		if a.currentTask >= taskIndex {
			a.currentTask += (len(subtasks) - 1)
		}
	} else {
		Logger.Warn("No subtasks generated, keeping original task")
	}

	return nil
}

func (a *Agent) extractSubtasks(response string) ([]string, error) {
	var subtasks []string

	// Try to find JSON array in response first
	jsonMatch := extractJSONArray(response)
	if jsonMatch != nil && len(*jsonMatch) > 0 {
		for _, item := range *jsonMatch {
			if strings.TrimSpace(item) != "" {
				subtasks = append(subtasks, item)
			}
		}
		return subtasks, nil
	}

	// Fallback: Parse numbered list or bullet points
	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if len(line) == 0 || strings.HasPrefix(strings.ToLower(line), "no subtasks needed") {
			continue
		}

		// Pattern: Numbered list ("1. Subtask description")
		if matched, _ := regexp.MatchString(`^\d+\.\s+(.+)$`, line); matched {
			subtask := strings.TrimPrefix(line, " ")
			subtasks = append(subtasks, strings.TrimSpace(subtask))
			continue
		}

		// Pattern: Bullet points ("- Subtask description")
		if matched, _ := regexp.MatchString(`^-\s+(.+)$`, line); matched {
			subtask := strings.TrimPrefix(line, "- ")
			subtasks = append(subtasks, strings.TrimSpace(subtask))
			continue
		}
	}

	// If no subtasks extracted and response is non-empty, treat entire response as one subtask
	if len(subtasks) == 0 && strings.TrimSpace(response) != "" {
		subtasks = append(subtasks, response)
	}

	return subtasks, nil
}
