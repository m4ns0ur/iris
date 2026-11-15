package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

type commandSpec struct {
	name        string
	usage       string
	description string
}

var commandSpecs = []commandSpec{
	{name: "/new", description: "Start a new session"},
	{name: "/resume", usage: "[id|n]", description: "Resume the latest, a prefix, or a listed session"},
	{name: "/sessions", description: "List recent sessions"},
	{name: "/fork", usage: "[n]", description: "Fork from the nth user message"},
	{name: "/export", description: "Export the session as Markdown"},
	{name: "/status", description: "Show session, model, context, and policy details"},
	{name: "/clear-queue", description: "Discard queued instructions and restore the input"},
	{name: "/skills", description: "List discovered skills"},
	{name: "/skill", usage: "<name>", description: "Insert a skill into the input"},
	{name: "/prompts", description: "List prompt templates"},
	{name: "/prompt", usage: "<name> [input]", description: "Render a prompt template"},
	{name: "/help", description: "Show commands and keys"},
	{name: "/quit", description: "Quit Iris"},
}

// completionItems returns the command menu entries for the current input.
func (m *model) completionItems() []commandSpec {
	if m.state != StateInput || m.pendingApproval != nil {
		return nil
	}

	raw := m.inputText.Value()
	value := strings.TrimSpace(raw)
	if value == "" || !strings.HasPrefix(value, "/") {
		return nil
	}
	if strings.ContainsAny(raw, " \t\n") {
		return nil
	}
	if m.completionDismissed == raw {
		return nil
	}

	matches := make([]commandSpec, 0, len(commandSpecs))
	for _, spec := range commandSpecs {
		if strings.HasPrefix(spec.name, value) {
			matches = append(matches, spec)
		}
	}
	if len(matches) > 6 {
		matches = matches[:6]
	}
	return matches
}

func (m *model) completionLimit() int {
	if m.height <= 0 {
		return 6
	}
	return max(min(m.height/3, 6), 2)
}

func (m *model) visibleCompletionItems() []commandSpec {
	items := m.completionItems()
	if limit := m.completionLimit(); len(items) > limit {
		items = items[:limit]
	}
	return items
}

func (m *model) completionHeight() int {
	items := m.visibleCompletionItems()
	if len(items) == 0 {
		return 0
	}
	return len(items) + RuleHeight
}

func (m *model) completionRule() string {
	if m.width <= 0 {
		return ""
	}
	return styleBorder.Render(strings.Repeat("─", m.width))
}

func (m *model) moveCompletion(delta int) {
	items := m.visibleCompletionItems()
	if len(items) == 0 {
		return
	}
	m.completionIndex = (m.completionIndex + delta) % len(items)
	if m.completionIndex < 0 {
		m.completionIndex += len(items)
	}
}

func (m *model) acceptCompletion() {
	items := m.visibleCompletionItems()
	if len(items) == 0 {
		return
	}
	if m.completionIndex < 0 || m.completionIndex >= len(items) {
		m.completionIndex = 0
	}
	m.inputText.SetValue(items[m.completionIndex].name + " ")
	m.inputText.CursorEnd()
	m.completionIndex = 0
	m.completionDismissed = ""
	m.resizeLayout(false)
}

func (m *model) completionView() string {
	items := m.visibleCompletionItems()
	if len(items) == 0 {
		return ""
	}
	selected := m.completionIndex
	if selected < 0 || selected >= len(items) {
		selected = 0
	}

	width := m.contentWidth()
	nameWidth := 0
	for _, item := range items {
		label := item.name
		if item.usage != "" {
			label += " " + item.usage
		}
		nameWidth = max(nameWidth, lipgloss.Width(label))
	}
	nameWidth = min(nameWidth, max(width/2, 12))

	lines := make([]string, 0, len(items))
	for index, item := range items {
		label := item.name
		if item.usage != "" {
			label += " " + item.usage
		}
		label = padRight(truncateMiddleDisplay(label, nameWidth), nameWidth)

		prefix := "  "
		nameStyle := styleMuted
		descriptionStyle := styleDim
		if index == selected {
			prefix = styleOrange.Render("›") + " "
			nameStyle = styleOrange
			descriptionStyle = styleMuted
		}

		line := prefix + nameStyle.Render(label)
		if item.description != "" {
			remaining := width - lipgloss.Width(line) - 2
			if remaining > 6 {
				line += "  " + descriptionStyle.Render(truncateMiddleDisplay(item.description, remaining))
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func padRight(text string, width int) string {
	if lipgloss.Width(text) >= width {
		return text
	}
	return text + strings.Repeat(" ", width-lipgloss.Width(text))
}
