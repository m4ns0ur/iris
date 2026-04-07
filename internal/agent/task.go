package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/prompt"
	"github.com/m4ns0ur/iris/internal/tool"
)

func (a *Agent) runTask() error {
	task := a.goals[a.currentTask]

	Logger.Debug("Running task", "taskIndex", a.currentTask, "task", task)

	opts := &llm.LLMOptions{
		Temperature:       0.1,
		TopP:              0.9,
		RepetitionPenalty: 1.05,
	}

	// Build prompt with available tools
	registry := tool.DefaultRegistry()
	toolContext := &tool.ToolContext{Registry: registry}
	systemPrompt := prompt.BuildSystemPrompt() + "\n\n" + toolContext.BuildPrompt()

	// Add task context to prompt
	taskPrompt := fmt.Sprintf(
		"Current Task (%d/%d): %s\n\n",
		a.currentTask+1, len(a.goals), task)

	// Include conversation history for context
	historyContext := a.buildHistoryContext()
	fullPrompt := systemPrompt + taskPrompt + historyContext

	Logger.Debug("Task prompt length", "length", len(fullPrompt))

	// Generate response from LLM
	resp, err := a.client.Generate(fullPrompt, "", opts)
	if err != nil {
		return fmt.Errorf("failed to generate task response: %w", err)
	}

	Logger.Debug("Task response received", "length", len(resp))

	// Parse tool call from response
	toolCall, args := a.parseToolCall(resp)
	if toolCall == "" {
		// No tool needed - task is complete or needs reflection
		Logger.Info("No tool required for task")
		a.state = "reflecting"
		return nil
	}

	Logger.Info("Executing tool", "toolName", toolCall, "args", args)

	// Execute the tool with context
	ctx := context.Background()
	result, err := tool.ExecuteWithTools(ctx, registry, toolCall, args)
	if err != nil {
		return fmt.Errorf("tool execution failed: %w", err)
	}

	Logger.Debug("Tool result", "toolName", toolCall, "result", result)

	// Add result to conversation history
	a.history = append(a.history, Message{
		Role:    "assistant",
		Content: fmt.Sprintf("Executed tool '%s': %s", toolCall, formatResult(result)),
	})

	return nil
}

func (a *Agent) buildHistoryContext() string {
	if len(a.history) < 2 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\nConversation History:\n")

	// Show last 5 messages or all if fewer than 5
	start := max(len(a.history)-5, 0)

	for i := start; i < len(a.history); i++ {
		fmt.Fprintf(&sb, "\n%s: %s", a.history[i].Role, truncateString(a.history[i].Content, 200))
	}

	return sb.String()
}

func (a *Agent) parseToolCall(response string) (string, map[string]any) {
	// Try to find JSON in response first
	jsonMatch := extractJSONArray(response)
	if jsonMatch != nil && len(*jsonMatch) > 0 {
		for _, item := range *jsonMatch {
			data, err := parseToolCallJSON(item)
			if err == nil && data.Name != "" {
				return data.Name, data.Parameters
			}
		}
		return "", nil
	}

	// Fallback: Parse natural language tool call
	lines := strings.SplitSeq(response, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)

		// Pattern 1: Tool name with JSON args ("file_read {"path": ...}")
		if strings.Contains(strings.ToLower(line), "tool:") ||
			strings.Contains(strings.ToLower(line), "using") {
			match := regexp.MustCompile(`(file_read|file_write|shell|search|grep):\s*(.+)?`)
			if match.MatchString(line) {
				groups := match.FindStringSubmatch(line)
				toolName := strings.TrimSpace(groups[1])
				argsStr := groups[2]

				// Try to parse as JSON if present
				if argsStr != "" && (strings.HasPrefix(argsStr, "{") || strings.Contains(argsStr, "\"path\"")) {
					data, err := parseToolCallJSON(argsStr)
					if err == nil {
						return toolName, data.Parameters
					}
				}
				// Simple case: just tool name
				if argsStr == "" || strings.TrimSpace(argsStr) == "" {
					return toolName, map[string]any{}
				}
			}
		}
	}

	// Last resort: return empty (no tool call detected)
	Logger.Debug("No tool call pattern found in response")
	return "", nil
}

func parseToolCallJSON(jsonStr string) (*tool.ToolDefinition, error) {
	var def tool.ToolDefinition
	err := json.Unmarshal([]byte(jsonStr), &def)
	if err != nil {
		// Try parsing as map[string]any first
		var m map[string]any
		err = json.Unmarshal([]byte(jsonStr), &m)
		if err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}

		// Extract name and args from map
		name, ok := m["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("missing 'name' field")
		}

		argsMap := make(map[string]any)
		if params, ok := m["arguments"].(map[string]any); ok {
			maps.Copy(argsMap, params)
		} else if args, ok := m["args"].(map[string]any); ok {
			maps.Copy(argsMap, args)
		}

		return &tool.ToolDefinition{
			Name:       name,
			Parameters: argsMap,
		}, nil
	}

	return &def, err
}

func formatResult(result map[string]any) string {
	if result == nil {
		return "(no output)"
	}

	content, ok := result["content"].(string)
	if ok && content != "" {
		return truncateString(content, 500)
	}

	// Try to marshal as JSON if not a simple string
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		return string(jsonBytes[:min(len(jsonBytes), 1000)]) + "..."
	}

	return fmt.Sprintf("%v", result)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
		return fmt.Errorf("need at least 2 tasks to merge")
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
		return fmt.Errorf("failed to generate subtasks: %w", err)
	}

	Logger.Debug("Subtask generation response", "length", len(resp))

	subtasks, err := a.extractSubtasks(resp)
	if err != nil {
		return fmt.Errorf("failed to parse subtasks: %w", err)
	}

	Logger.Info("Subtasks extracted", "count", len(subtasks))

	// Insert subtasks at the current task index, replacing the original task
	if len(subtasks) > 0 {
		// Remove the original task
		a.goals = append(a.goals[:taskIndex], a.goals[taskIndex+1:]...)

		// Insert subtasks in place of the original task
		a.goals = append(a.goals, subtasks...)

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
		if numberedListRegex.MatchString(line) {
			subtask := strings.TrimPrefix(line, " ")
			subtasks = append(subtasks, strings.TrimSpace(subtask))
			continue
		}

		// Pattern: Bullet points ("- Subtask description")
		if bulletListRegex.MatchString(line) {
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
