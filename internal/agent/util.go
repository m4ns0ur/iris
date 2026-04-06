package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func extractJSONArray(text string) *[]string {
	text = strings.TrimSpace(text)

	start := strings.Index(text, "[")
	if start == -1 {
		return nil
	}

	depth := 0
	end := -1
	for i := start; i < len(text); i++ {
		if text[i] == '[' {
			depth++
		} else if text[i] == ']' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}

	if end == -1 {
		return nil
	}

	jsonStr := text[start : end+1]
	var result []string
	err := json.Unmarshal([]byte(jsonStr), &result)
	if err != nil {
		return nil
	}

	return &result
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
		return Adjustment{}, fmt.Errorf("Failed to unmarshal: %w", err)
	}
	return adj, nil
}
