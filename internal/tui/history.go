package tui

import "strings"

const inputHistoryLimit = 100

// recordHistory adds a submitted prompt to the in-memory history. Slash
// commands are intentionally excluded: they are discoverable through
// completion, while Up should recall things the user actually asked Iris.
func (m *model) recordHistory(input string) {
	input = strings.TrimSpace(input)
	if input == "" || strings.HasPrefix(input, "/") {
		m.historyIndex = -1
		return
	}
	if len(m.inputHistory) > 0 && m.inputHistory[len(m.inputHistory)-1] == input {
		m.historyIndex = -1
		return
	}
	m.inputHistory = append(m.inputHistory, input)
	if len(m.inputHistory) > inputHistoryLimit {
		m.inputHistory = m.inputHistory[len(m.inputHistory)-inputHistoryLimit:]
	}
	m.historyIndex = -1
}

func (m *model) historyPrev() bool {
	if len(m.inputHistory) == 0 {
		return false
	}
	if m.historyIndex == -1 {
		m.historyDraft = m.inputText.Value()
		m.historyIndex = len(m.inputHistory) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	} else {
		return false
	}
	m.setHistoryValue(m.inputHistory[m.historyIndex])
	return true
}

func (m *model) historyNext() bool {
	if m.historyIndex == -1 {
		return false
	}
	if m.historyIndex < len(m.inputHistory)-1 {
		m.historyIndex++
		m.setHistoryValue(m.inputHistory[m.historyIndex])
		return true
	}

	m.historyIndex = -1
	m.setHistoryValue(m.historyDraft)
	m.historyDraft = ""
	return true
}

func (m *model) setHistoryValue(value string) {
	m.inputText.SetValue(value)
	m.inputText.CursorEnd()
	m.completionIndex = 0
	m.resizeLayout(false)
}
