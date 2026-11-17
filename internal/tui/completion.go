package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
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
	matches := make([]commandSpec, 0, len(commandSpecs))
	for _, spec := range commandSpecs {
		if strings.HasPrefix(spec.name, value) {
			matches = append(matches, spec)
		}
	}
	return matches
}

func (m *model) completionLimit() int {
	if m.height <= 0 {
		return 6
	}
	return max(min(m.height/3, 6), 2)
}

// completionWindow returns the visible slice of matches and its offset in the
// full list. The window follows the selection, so every match is reachable even
// when the menu can only show a few rows.
func (m *model) completionWindow() (int, []commandSpec) {
	items := m.completionItems()
	if len(items) == 0 {
		return 0, nil
	}
	limit := m.completionLimit()
	if len(items) <= limit {
		return 0, items
	}

	selected := m.completionIndex
	if selected < 0 {
		selected = 0
	}
	if selected >= len(items) {
		selected = len(items) - 1
	}

	start := selected - limit + 1
	if start < 0 {
		start = 0
	}
	if start > len(items)-limit {
		start = len(items) - limit
	}
	return start, items[start : start+limit]
}

func (m *model) visibleCompletionItems() []commandSpec {
	_, items := m.completionWindow()
	return items
}

// completionPosition reports the 1-based selection and the match count, for the
// status hint that tells the reader the menu continues past the visible rows.
func (m *model) completionPosition() (int, int) {
	items := m.completionItems()
	if len(items) == 0 {
		return 0, 0
	}
	selected := m.completionIndex
	if selected < 0 || selected >= len(items) {
		selected = 0
	}
	return selected + 1, len(items)
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
	items := m.completionItems()
	if len(items) == 0 {
		return
	}
	m.completionIndex = (m.completionIndex + delta) % len(items)
	if m.completionIndex < 0 {
		m.completionIndex += len(items)
	}
}

// dismissCompletion leaves command entry: it removes the leading slash that
// opened the menu, so Escape returns the prompt to ordinary text instead of
// leaving a command prefix behind. Text typed after the slash is the command
// query and stays as plain text, editable like anything else.
func (m *model) dismissCompletion() {
	value := m.inputText.Value()
	if trimmed := strings.TrimPrefix(value, "/"); trimmed != value {
		m.inputText.SetValue(trimmed)
		m.inputText.CursorEnd()
	}
	m.completionIndex = 0
	m.resizeLayout(false)
}

// completionNeedsInput reports whether a command expects an argument before it
// can run. Required arguments are written as "<name>" in the usage string;
// optional ones use brackets.
func completionNeedsInput(spec commandSpec) bool {
	return strings.HasPrefix(strings.TrimSpace(spec.usage), "<")
}

// runCompletion applies the highlighted command to the prompt. Commands that
// take no required argument run immediately, the way Enter behaves everywhere
// else; a command that needs an argument is inserted so the argument can be
// typed without running it half-finished.
func (m *model) runCompletion() tea.Cmd {
	items := m.completionItems()
	if len(items) == 0 {
		return nil
	}
	index := m.completionIndex
	if index < 0 || index >= len(items) {
		index = 0
	}
	spec := items[index]
	if completionNeedsInput(spec) {
		m.acceptCompletion()
		return nil
	}

	m.inputText.SetValue(spec.name)
	m.inputText.CursorEnd()
	m.completionIndex = 0
	return m.submitInput()
}

func (m *model) acceptCompletion() {
	items := m.completionItems()
	if len(items) == 0 {
		return
	}
	if m.completionIndex < 0 || m.completionIndex >= len(items) {
		m.completionIndex = 0
	}
	m.inputText.SetValue(items[m.completionIndex].name + " ")
	m.inputText.CursorEnd()
	m.completionIndex = 0
	m.resizeLayout(false)
}

func (m *model) completionView() string {
	start, items := m.completionWindow()
	if len(items) == 0 {
		return ""
	}
	// The selection is an index into the full match list, so map it into the
	// visible window before highlighting a row.
	selected := m.completionIndex - start
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
