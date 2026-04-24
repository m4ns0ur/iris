package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/prompt"
)

type Plan struct {
	Goals []string `json:"goals"`
}

type Adjustment struct {
	Type        string `json:"type"`
	TaskIndex   int    `json:"task_index,omitempty"`
	TaskIndices []int  `json:"task_indices,omitempty"`
	Description string `json:"description"`
}

func (a *Agent) decide(onChunk func(chunkType string, content string), onError func(error)) error {
	a.history = append(a.history, llm.ChatMessage{
		Role:    "assistant",
		Content: prompt.BuildDecisionPrompt(),
	})

	Logger.Debug("Decision phase", "history", a.history)

	resp, err := a.client.Generate(a.history, nil, onChunk, onError)
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	decision := extractJSON(resp)

	Logger.Debug("Decision received", "decision", decision)

	if decision != nil {
		if state, ok := (*decision)["state"].(string); ok {
			Logger.Debug("State updated based on decision", "newState", a.state)
			a.state = state
		}
		if response, ok := (*decision)["response"].(string); ok {
			a.history = append(a.history, llm.ChatMessage{Role: "assistant", Content: response})
		}
		if toolName, ok := (*decision)["tool_name"].(string); ok {
			a.toolName = toolName
			if toolArgs, ok := (*decision)["tool_args"].(map[string]any); ok {
				a.toolArgs = toolArgs
			}
			Logger.Debug("Tool execution decision", "toolName", a.toolName, "toolArgs", a.toolArgs)
		}
	}

	return nil
}

func (a *Agent) plan(onChunk func(chunkType string, content string), onError func(error)) error {
	a.state = "planning"

	a.history = append(a.history, llm.ChatMessage{
		Role:    "assistant",
		Content: prompt.BuildPlanningPromptWithContext(a.goals, a.history),
	})

	Logger.Debug("Planning phase", "history", a.history)

	resp, err := a.client.Generate(a.history, nil, onChunk, onError)
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	Logger.Debug("Plan response received", "length", len(resp))

	plan, err := a.extractGoalsFromResponse(resp)
	if err != nil {
		return fmt.Errorf("failed to extract plan: %w", err)
	}

	Logger.Debug("Plan generated", "goalsCount", len(plan.Goals))

	for i, goal := range plan.Goals {
		Logger.Debug("Goal", "index", i, "goal", goal)
	}

	a.goals = plan.Goals
	a.currentTask = 0

	Logger.Debug("Planning complete", "goalsCount", len(a.goals))
	return nil
}

func (a *Agent) reflect(onChunk func(chunkType string, content string), onError func(error)) error {
	a.state = "reflecting"

	Logger.Debug("Reflection phase")

	if a.isTaskComplete() {
		a.currentTask++
		a.state = "executing"
		return nil
	}

	if err := a.adjustPlan(onChunk, onError); err != nil {
		return err
	}

	return nil
}

func (a *Agent) executePlan(onChunk func(chunkType string, content string), onError func(error)) error {
	if a.currentTask >= len(a.goals) {
		a.state = "done"
		return nil
	}

	a.state = "executing"
	task := a.goals[a.currentTask]

	Logger.Debug("Executing task", "taskIndex", a.currentTask, "task", task)

	if err := a.runTask(onChunk, onError); err != nil {
		return err
	}

	a.currentTask++
	return nil
}

func (a *Agent) extractGoalsFromResponse(response string) (*Plan, error) {
	plan := &Plan{Goals: []string{}}

	// Try to find JSON array in the response
	jsonMatch := extractJSONArray(response)
	if jsonMatch != nil {
		for _, item := range *jsonMatch {
			if strings.TrimSpace(item) != "" {
				plan.Goals = append(plan.Goals, item)
			}
		}
		return plan, nil
	}

	// Fallback: Extract goals using regex patterns
	lines := strings.SplitSeq(response, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)

		// Pattern 1: Numbered list ("1. Task description")
		if numberedListRegex.MatchString(line) {
			goal := strings.TrimPrefix(line, " ")
			plan.Goals = append(plan.Goals, strings.TrimSpace(goal))
			continue
		}

		// Pattern 2: Bullet points ("- Task description")
		if bulletListRegex.MatchString(line) {
			goal := strings.TrimPrefix(line, "- ")
			plan.Goals = append(plan.Goals, strings.TrimSpace(goal))
			continue
		}

		// Pattern 3: Task markers ("Task:", "Goal:", etc.)
		if taskMarkerRegex.MatchString(line) {
			goal := strings.TrimPrefix(line, "Task: ")
			plan.Goals = append(plan.Goals, strings.TrimSpace(goal))
			continue
		}
	}

	// If no goals extracted and response is non-empty, treat entire response as a single goal
	if len(plan.Goals) == 0 && strings.TrimSpace(response) != "" {
		plan.Goals = append(plan.Goals, response)
	}

	return plan, nil
}

func (a *Agent) adjustPlan(onChunk func(chunkType string, content string), onError func(error)) error {
	Logger.Debug("Adjusting plan", "currentTask", a.currentTask, "totalGoals", len(a.goals))

	a.history = append(a.history, llm.ChatMessage{
		Role:    "assistant",
		Content: prompt.BuildReflectionPrompt(a.currentTask, len(a.goals)),
	})

	resp, err := a.client.Generate(a.history, nil, onChunk, onError)
	if err != nil {
		return fmt.Errorf("LLM adjustment failed: %w", err)
	}

	Logger.Debug("Adjustment response received", "length", len(resp))

	adjustments, err := a.parseAdjustments(resp)
	if err != nil {
		return fmt.Errorf("failed to parse adjustments: %w", err)
	}

	Logger.Debug("Plan adjusted", "actions", len(adjustments))

	for _, action := range adjustments {
		switch action.Type {
		case "split":
			if err := a.splitTask(action.TaskIndex, action.Description, onChunk, onError); err != nil {
				return fmt.Errorf("failed to split task: %w", err)
			}

		case "merge":
			if err := a.mergeTasks(action.TaskIndices...); err != nil {
				return fmt.Errorf("failed to merge tasks: %w", err)
			}

		case "reorder":
			if err := a.reorderTasks(action.TaskIndex, action.Description); err != nil {
				return fmt.Errorf("failed to reorder tasks: %w", err)
			}

		case "skip":
			if err := a.skipTask(action.TaskIndex, action.Description); err != nil {
				return fmt.Errorf("failed to skip task: %w", err)
			}

		default:
			Logger.Warn("Unknown adjustment type", "type", action.Type)
		}
	}

	return nil
}

func (a *Agent) parseAdjustments(response string) ([]Adjustment, error) {
	var adjustments []Adjustment

	// Try to find JSON array in response
	jsonMatch := extractJSONArray(response)
	if jsonMatch != nil && len(*jsonMatch) > 0 {
		for _, item := range *jsonMatch {
			if str, ok := parseJSONItem(item); ok {
				adj, err := parseAdjustment(str)
				if err == nil {
					adjustments = append(adjustments, adj)
				}
			}
		}
		return adjustments, nil
	}

	// Fallback: Parse natural language instructions
	lines := strings.SplitSeq(response, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)

		if len(line) == 0 || strings.HasPrefix(strings.ToLower(line), "no changes needed") {
			continue
		}

		// Extract action type and description using regex
		match := regexp.MustCompile(`^(split|merge|reorder|skip|retry):(\d+)?\s*(.+)?$`)
		if match.MatchString(line) {
			groups := match.FindStringSubmatch(line)
			actionType := strings.TrimSpace(groups[1])
			taskIndexStr := groups[2]
			description := strings.TrimSpace(groups[3])

			var taskIndex int
			if taskIndexStr != "" {
				taskIndex, _ = strconv.Atoi(taskIndexStr)
			}

			adjustments = append(adjustments, Adjustment{
				Type:        actionType,
				TaskIndex:   taskIndex,
				Description: description,
			})
		}
	}

	return adjustments, nil
}

func parseJSONItem(item any) (string, bool) {
	switch v := item.(type) {
	case string:
		return strings.TrimSpace(v), true
	default:
		return "", false
	}
}

func parseAdjustment(jsonStr string) (Adjustment, error) {
	var adj Adjustment
	err := json.Unmarshal([]byte(jsonStr), &adj)
	if err != nil {
		return Adjustment{}, fmt.Errorf("failed to unmarshal: %w", err)
	}
	return adj, nil
}
