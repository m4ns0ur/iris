package session

import (
	"encoding/json"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

// recoverInterruptedToolCalls inserts a synthetic interrupted result for every
// assistant tool call that has no recorded result. It also normalizes missing
// tool-call IDs and types so the recovered model context remains valid.
//
// Synthetic results are placed before the next user/assistant entry, preserving
// the assistant → tool-result ordering expected by providers.
func recoverInterruptedToolCalls(session *Session) bool {
	if len(session.Entries) == 0 {
		return false
	}

	repaired := false
	result := make([]Entry, 0, len(session.Entries))
	pending := make([]provider.ToolCall, 0)

	appendEntry := func(entry Entry) {
		if entry.ID == "" {
			entry.ID = newID("e")
			repaired = true
		}
		if entry.SessionID == "" {
			entry.SessionID = session.ID
			repaired = true
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = time.Now().UTC()
			repaired = true
		}
		if entry.ParentID == "" && len(result) > 0 {
			entry.ParentID = result[len(result)-1].ID
			repaired = true
		}
		result = append(result, entry)
	}

	flush := func() {
		if len(pending) == 0 {
			return
		}
		now := time.Now().UTC()
		for index, call := range pending {
			entry := Entry{
				ID:         newID("e"),
				SessionID:  session.ID,
				Kind:       EntryTool,
				CreatedAt:  now.Add(time.Duration(index) * time.Nanosecond),
				Content:    interruptedToolContent(call),
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Status:     EntryStatusInterrupted,
				Recovered:  true,
			}
			if len(result) > 0 {
				entry.ParentID = result[len(result)-1].ID
			}
			result = append(result, entry)
		}
		pending = pending[:0]
		repaired = true
	}

	for _, original := range session.Entries {
		entry := original
		switch entry.Kind {
		case EntryAssistant:
			flush()
			for index := range entry.ToolCalls {
				if entry.ToolCalls[index].ID == "" {
					entry.ToolCalls[index].ID = newID("call")
					repaired = true
				}
				if entry.ToolCalls[index].Type == "" {
					entry.ToolCalls[index].Type = "function"
					repaired = true
				}
			}
			appendEntry(entry)
			pending = append(pending, entry.ToolCalls...)

		case EntryTool:
			matched := -1
			for index, call := range pending {
				if call.ID == entry.ToolCallID {
					matched = index
					break
				}
			}
			if matched >= 0 {
				pending = append(pending[:matched], pending[matched+1:]...)
			}
			// Orphan tool results stay in the transcript for auditability;
			// Session.History omits them from the model context.
			appendEntry(entry)

		default:
			flush()
			appendEntry(entry)
		}
	}
	flush()

	if !repaired {
		return false
	}

	session.Entries = result
	for _, entry := range result {
		if entry.CreatedAt.After(session.UpdatedAt) {
			session.UpdatedAt = entry.CreatedAt
		}
	}
	return true
}

func interruptedToolContent(call provider.ToolCall) string {
	payload := map[string]any{
		"ok": false,
		"error": map[string]any{
			"code": "tool_execution_interrupted",
			"message": "Iris stopped before recording this tool's result. The tool may have " +
				"executed; verify its effects before retrying.",
			"tool":         call.Function.Name,
			"tool_call_id": call.ID,
			"executed":     true,
			"retryable":    false,
			"interrupted":  true,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"error":{"code":"tool_execution_interrupted","message":"tool result was interrupted","executed":true,"retryable":false,"interrupted":true}}`
	}
	return string(encoded)
}

// needsToolIdentityRepair reports whether an assistant tool call is missing
// the ID or type required by providers.
func needsToolIdentityRepair(entries []Entry) bool {
	for _, entry := range entries {
		if entry.Kind != EntryAssistant {
			continue
		}
		for _, call := range entry.ToolCalls {
			if call.ID == "" || call.Type == "" {
				return true
			}
		}
	}
	return false
}

// hasInterruptedToolCalls reports whether any assistant tool call lacks a
// recorded result. It does not mutate the transcript.
func hasInterruptedToolCalls(entries []Entry) bool {
	pending := make([]provider.ToolCall, 0)
	for _, entry := range entries {
		switch entry.Kind {
		case EntryAssistant:
			pending = append(pending, entry.ToolCalls...)
		case EntryTool:
			for index, call := range pending {
				if call.ID == entry.ToolCallID {
					pending = append(pending[:index], pending[index+1:]...)
					break
				}
			}
		}
	}
	return len(pending) > 0
}
