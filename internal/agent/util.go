package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	numberedListRegex = regexp.MustCompile(`^\d+\.\s+(.+)$`)
	bulletListRegex   = regexp.MustCompile(`^-\s+(.+)$`)
	taskMarkerRegex   = regexp.MustCompile(`^(?:Task|Goal):\s+(.+)$`)
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

func extractJSON(text string) *map[string]any {
	text = strings.TrimSpace(text)

	start := strings.Index(text, "{")
	if start == -1 {
		return nil
	}

	depth := 0
	end := -1
	for i := start; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
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
	var result map[string]any
	err := json.Unmarshal([]byte(jsonStr), &result)
	if err != nil {
		return nil
	}

	return &result
}
