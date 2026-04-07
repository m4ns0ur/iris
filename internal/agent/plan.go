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

func (a *Agent) plan() error {
	a.state = "planning"

	systemPrompt := prompt.BuildSystemPrompt()
	userPrompt := prompt.BuildPlanningPrompt(a.goals)

	Logger.Debug("Planning phase", "systemPrompt", systemPrompt, "userPrompt", userPrompt)

	plan, err := a.generatePlan(systemPrompt, userPrompt)
	if err != nil {
		return err
	}

	a.goals = plan.Goals
	a.currentTask = 0

	Logger.Info("Planning complete", "goalsCount", len(a.goals))
	return nil
}

func (a *Agent) generatePlan(systemPrompt, userPrompt string) (*Plan, error) {
	if a.client == nil {
		return nil, fmt.Errorf("LLM client is not initialized")
	}

	Logger.Debug("Generating plan", "systemPrompt", systemPrompt, "userPrompt", userPrompt)

	opts := &llm.LLMOptions{
		Temperature:       0.1,
		TopP:              0.9,
		RepetitionPenalty: 1.05,
	}

	resp, err := a.client.Generate(systemPrompt, userPrompt, opts)
	if err != nil {
		return nil, fmt.Errorf("LLM generation failed: %w", err)
	}

	Logger.Debug("Plan response received", "length", len(resp))

	plan, err := a.extractGoalsFromResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to extract plan: %w", err)
	}

	Logger.Info("Plan generated", "goalsCount", len(plan.Goals))

	for i, goal := range plan.Goals {
		Logger.Debug("Goal", "index", i, "goal", goal)
	}

	return plan, nil
}

func (a *Agent) reflect() error {
	a.state = "reflecting"

	Logger.Debug("Reflection phase")

	if a.isTaskComplete() {
		a.currentTask++
		a.state = "executing"
		return nil
	}

	if err := a.adjustPlan(); err != nil {
		return err
	}

	return nil
}

func (a *Agent) executePlan() error {
	if a.currentTask >= len(a.goals) {
		a.state = "done"
		return nil
	}

	a.state = "executing"
	task := a.goals[a.currentTask]

	Logger.Info("Executing task", "taskIndex", a.currentTask, "task", task)

	if err := a.runTask(); err != nil {
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
	lines := strings.Split(response, "\n")
	for _, line := range lines {
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

func (a *Agent) adjustPlan() error {
	if a.client == nil {
		return fmt.Errorf("LLM client is not initialized")
	}

	Logger.Info("Adjusting plan", "currentTask", a.currentTask, "totalGoals", len(a.goals))

	reflectionPrompt := prompt.BuildReflectionPrompt(a.currentTask, len(a.goals))

	var historyContext string
	if len(a.history) >= 3 {
		historyContext = "\n\nRecent conversation:\n"
		for i := len(a.history) - 1; i >= len(a.history)-3 && i >= 0; i-- {
			historyContext += fmt.Sprintf("%s: %s\n", a.history[i].Role, a.history[i].Content)
		}
	}

	opts := &llm.LLMOptions{
		Temperature:       0.2,
		TopP:              0.95,
		RepetitionPenalty: 1.05,
	}

	resp, err := a.client.Generate(prompt.BuildSystemPrompt(), reflectionPrompt+historyContext, opts)
	if err != nil {
		return fmt.Errorf("LLM adjustment failed: %w", err)
	}

	Logger.Debug("Adjustment response received", "length", len(resp))

	adjustments, err := a.parseAdjustments(resp)
	if err != nil {
		return fmt.Errorf("failed to parse adjustments: %w", err)
	}

	Logger.Info("Plan adjusted", "actions", len(adjustments))

	for _, action := range adjustments {
		switch action.Type {
		case "split":
			if err := a.splitTask(action.TaskIndex, action.Description); err != nil {
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
	lines := strings.Split(response, "\n")
	for _, line := range lines {
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
