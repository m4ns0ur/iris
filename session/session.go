// Package session provides persistent conversation sessions. The default
// implementation stores one JSONL file per session.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

var (
	// ErrSessionNotFound is returned when a session ID cannot be resolved.
	ErrSessionNotFound = errors.New("session not found")

	// ErrNoSessions is returned when no persisted sessions exist.
	ErrNoSessions = errors.New("no sessions found")

	// ErrInvalidSessionID is returned for unsafe or malformed session IDs.
	ErrInvalidSessionID = errors.New("invalid session id")

	// ErrInvalidForkPoint is returned when a fork target is missing or is not
	// a user message.
	ErrInvalidForkPoint = errors.New("fork point must be a user message")
)

// EntryKind identifies the type of a conversation entry.
type EntryKind string

const (
	EntryUser      EntryKind = "user"
	EntryAssistant EntryKind = "assistant"
	EntryTool      EntryKind = "tool"
)

// Entry is one persisted conversation item. ParentID links it to the previous
// entry in the conversation tree.
type Entry struct {
	ID               string              `json:"id"`
	SessionID        string              `json:"session_id"`
	ParentID         string              `json:"parent_id,omitempty"`
	Kind             EntryKind           `json:"kind"`
	CreatedAt        time.Time           `json:"created_at"`
	Content          string              `json:"content,omitempty"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []provider.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	ToolName         string              `json:"tool_name,omitempty"`
	Model            string              `json:"model,omitempty"`
	Usage            *provider.Usage     `json:"usage,omitempty"`
}

// Metadata is the session header persisted as the first JSONL record.
type Metadata struct {
	ID                string            `json:"id"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	Model             string            `json:"model,omitempty"`
	ModelMetadata     map[string]string `json:"model_metadata,omitempty"`
	Workspace         string            `json:"workspace,omitempty"`
	ParentSessionID   string            `json:"parent_session_id,omitempty"`
	ForkedFromEntryID string            `json:"forked_from_entry_id,omitempty"`
	Usage             provider.Usage    `json:"usage"`
}

// Session is an in-memory conversation that can be saved by a Manager.
type Session struct {
	Metadata
	Entries []Entry `json:"entries,omitempty"`
}

// CreateOptions configures a new session.
type CreateOptions struct {
	Model             string
	ModelMetadata     map[string]string
	Workspace         string
	ParentSessionID   string
	ForkedFromEntryID string
}

// Summary is a lightweight session listing entry.
type Summary struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Model           string    `json:"model,omitempty"`
	Workspace       string    `json:"workspace,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	EntryCount      int       `json:"entry_count"`
}

// Manager is the persistent session abstraction.
type Manager interface {
	// Create builds a new unsaved session.
	Create(options CreateOptions) (*Session, error)

	// Save atomically persists the session and updates its timestamp.
	Save(session *Session) error

	// Open loads a session by ID.
	Open(id string) (*Session, error)

	// Latest opens the most recently updated session.
	Latest() (*Session, error)

	// List returns sessions ordered by most recently updated.
	List() ([]Summary, error)

	// Fork creates a child session containing entries through the selected
	// user message.
	Fork(sessionID, fromEntryID string) (*Session, error)

	// ExportMarkdown writes the session as Markdown and returns the path.
	ExportMarkdown(session *Session) (string, error)
}

// SessionManager is an alias for Manager.
type SessionManager = Manager

// NewSession creates an unsaved session.
func NewSession(options CreateOptions) *Session {
	now := time.Now().UTC()
	return &Session{
		Metadata: Metadata{
			ID:                newID("s"),
			CreatedAt:         now,
			UpdatedAt:         now,
			Model:             options.Model,
			ModelMetadata:     cloneMetadata(options.ModelMetadata),
			Workspace:         options.Workspace,
			ParentSessionID:   options.ParentSessionID,
			ForkedFromEntryID: options.ForkedFromEntryID,
		},
	}
}

// AddUser appends a user message.
func (s *Session) AddUser(content string) Entry {
	return s.Append(Entry{Kind: EntryUser, Content: content})
}

// AddAssistant appends an assistant message, including any tool calls and
// token usage for that turn.
func (s *Session) AddAssistant(
	content string,
	reasoning string,
	toolCalls []provider.ToolCall,
	usage *provider.Usage,
	model string,
) Entry {
	return s.Append(Entry{
		Kind:             EntryAssistant,
		Content:          content,
		ReasoningContent: reasoning,
		ToolCalls:        append([]provider.ToolCall(nil), toolCalls...),
		Usage:            cloneUsage(usage),
		Model:            model,
	})
}

// AddTool appends a tool result.
func (s *Session) AddTool(toolCallID, toolName, content string) Entry {
	return s.Append(Entry{
		Kind:       EntryTool,
		Content:    content,
		ToolCallID: toolCallID,
		ToolName:   toolName,
	})
}

// Append adds an entry, assigning its ID, timestamp, session ID, and parent.
func (s *Session) Append(entry Entry) Entry {
	now := time.Now().UTC()
	entry.ID = newID("e")
	entry.SessionID = s.ID
	entry.CreatedAt = now
	if entry.ParentID == "" {
		entry.ParentID = s.lastEntryID()
	}

	s.Entries = append(s.Entries, entry)
	s.UpdatedAt = now

	if entry.Usage != nil {
		s.Usage.PromptTokens += entry.Usage.PromptTokens
		s.Usage.CompletionTokens += entry.Usage.CompletionTokens
		s.Usage.TotalTokens += entry.Usage.TotalTokens
	}

	return entry
}

// EntryByID returns an entry by ID.
func (s *Session) EntryByID(id string) (Entry, bool) {
	for _, entry := range s.Entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return Entry{}, false
}

// UserEntries returns user messages in conversation order.
func (s *Session) UserEntries() []Entry {
	users := make([]Entry, 0)
	for _, entry := range s.Entries {
		if entry.Kind == EntryUser {
			users = append(users, entry)
		}
	}
	return users
}

// History reconstructs provider messages for the agent. System prompts are not
// persisted and are re-added by the agent when the history is loaded.
func (s *Session) History() []provider.Message {
	messages := make([]provider.Message, 0, len(s.Entries))

	for _, entry := range s.Entries {
		switch entry.Kind {
		case EntryUser:
			messages = append(messages, provider.Message{
				Role:    "user",
				Content: []provider.Content{{Type: "text", Text: entry.Content}},
			})
		case EntryAssistant:
			messages = append(messages, provider.Message{
				Role:             "assistant",
				Content:          entry.Content,
				ReasoningContent: entry.ReasoningContent,
				ToolCalls:        append([]provider.ToolCall(nil), entry.ToolCalls...),
			})
		case EntryTool:
			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    entry.Content,
				ToolCallID: entry.ToolCallID,
			})
		}
	}

	return messages
}

func (s *Session) lastEntryID() string {
	if len(s.Entries) == 0 {
		return ""
	}
	return s.Entries[len(s.Entries)-1].ID
}

func cloneMetadata(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneUsage(usage *provider.Usage) *provider.Usage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

func newID(prefix string) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(random[:])
}
