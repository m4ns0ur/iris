package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

func userHomeDir() (string, error) {
	return os.UserHomeDir()
}

func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func workspaceLabel(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if cleaned == "/" {
		return "/"
	}
	base := filepath.Base(cleaned)
	if base == "." {
		return ""
	}
	return base
}

func shortAge(updated time.Time) string {
	if updated.IsZero() {
		return "—"
	}
	age := time.Since(updated)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return strconv.Itoa(int(age.Minutes())) + "m"
	case age < 24*time.Hour:
		return strconv.Itoa(int(age.Hours())) + "h"
	default:
		return strconv.Itoa(int(age.Hours()/24)) + "d"
	}
}

func formatInt(value int) string {
	text := strconv.Itoa(value)
	if len(text) <= 3 {
		return text
	}
	var b strings.Builder
	offset := len(text) % 3
	if offset > 0 {
		b.WriteString(text[:offset])
	}
	for index := offset; index < len(text); index += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(text[index : index+3])
	}
	return b.String()
}

func usageTotal(usage provider.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.PromptTokens + usage.CompletionTokens
}
