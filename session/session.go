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

	// ErrSessionLocked is returned when another writer holds the session lease.
	ErrSessionLocked = errors.New("session is locked by another writer")

	// ErrSessionConflict is returned when a save would overwrite changes made
	// by another writer.
	ErrSessionConflict = errors.New("session was modified by another writer")

	// ErrUnsupportedSessionVersion is returned when a session file uses a
	// newer format than this build understands.
	ErrUnsupportedSessionVersion = errors.New("unsupported session format version")
)

// FormatVersion is the current on-disk session schema version.
const FormatVersion = 2

// EntryStatusInterrupted marks a recovered tool entry whose result was never
// recorded, typically because Iris crashed while the tool was executing.
const EntryStatusInterrupted = "interrupted"

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
	// Status is empty for normal entries. EntryStatusInterrupted identifies a
	// tool result synthesized during crash recovery.
	Status string `json:"status,omitempty"`
	// Recovered reports that the entry was synthesized by session recovery.
	Recovered bool `json:"recovered,omitempty"`
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
	// Revision increments on every successful save or append. It is used to
	// detect lost updates between writers.
	Revision uint64 `json:"revision,omitempty"`
}

// Session is an in-memory conversation that can be saved by a Manager.
// Entries are the durable transcript; History is the provider-facing model
// context. Context compaction never removes entries from the transcript.
type Session struct {
	Metadata
	Entries []Entry `json:"entries,omitempty"`

	// Recovered reports that Open found a partial final record, an interrupted
	// tool call, or an incomplete checkpoint. A legacy file that only needs a
	// format upgrade is tracked by needsRepair instead.
	Recovered bool `json:"-"`

	// needsRepair is set by the loader and consumed by Acquire, which persists
	// the repaired transcript before returning the writer lease.
	needsRepair bool
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

	// Save atomically rewrites the complete session and updates its
	// timestamp. If the file exists, the session revision must match the
	// current file revision or Save returns ErrSessionConflict.
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

	// Acquire takes an exclusive writer lease for the session and repairs any
	// recoverable damage. It returns ErrSessionLocked if another writer owns
	// the session. Release the lease when the session is no longer active.
	Acquire(session *Session) (Lease, error)

	// Append durably appends one entry to the session. The session must be
	// under an Acquire lease. Append returns only after the entry is synced.
	Append(session *Session, entry Entry) (Entry, error)
}

// SessionManager is an alias for Manager.
type SessionManager = Manager

// ToolOutputProvider optionally supplies the persistent directory used for
// full tool output. Managers that do not implement it use the agent's
// ephemeral tool-output store.
type ToolOutputProvider interface {
	ToolOutputDir(sessionID string) (string, error)
}

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

// Append adds an entry in memory, assigning its ID, timestamp, session ID, and
// parent. It does not write to durable storage; use Manager.Append while
// holding a writer lease for that.
func (s *Session) Append(entry Entry) Entry {
	entry = s.prepareEntry(entry)
	s.commitEntry(entry)
	return entry
}

// prepareEntry assigns identity and ordering fields without mutating the
// session. The manager uses it to write an entry before committing it to
// memory, so a failed write leaves the in-memory transcript unchanged.
func (s *Session) prepareEntry(entry Entry) Entry {
	now := time.Now().UTC()
	entry.ID = newID("e")
	entry.SessionID = s.ID
	entry.CreatedAt = now
	if entry.ParentID == "" {
		entry.ParentID = s.lastEntryID()
	}
	return entry
}

// commitEntry adds a prepared entry to the in-memory session.
func (s *Session) commitEntry(entry Entry) {
	s.Entries = append(s.Entries, entry)
	if entry.CreatedAt.After(s.UpdatedAt) {
		s.UpdatedAt = entry.CreatedAt
	}

	if entry.Usage != nil {
		s.Usage.PromptTokens += entry.Usage.PromptTokens
		s.Usage.CompletionTokens += entry.Usage.CompletionTokens
		s.Usage.TotalTokens += entry.Usage.TotalTokens
	}
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
// persisted and are re-added by the agent when the history is loaded. Orphan
// tool results are omitted, and tool calls without a recorded result get a
// synthetic interrupted result, so the model context stays valid. The
// transcript (Entries) is not modified; Acquire persists the repair.
func (s *Session) History() []provider.Message {
	messages := make([]provider.Message, 0, len(s.Entries))
	pending := make([]provider.ToolCall, 0)

	// A transcript that was opened but not yet repaired can have dangling tool
	// calls. Synthesize interrupted results in the model context so loading it
	// is always valid; Acquire persists the same repair in the transcript.
	flushPending := func() {
		for _, call := range pending {
			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    interruptedToolContent(call),
				ToolCallID: call.ID,
			})
		}
		pending = pending[:0]
	}

	for _, entry := range s.Entries {
		switch entry.Kind {
		case EntryUser:
			flushPending()
			messages = append(messages, provider.Message{
				Role:    "user",
				Content: []provider.Content{{Type: "text", Text: entry.Content}},
			})
		case EntryAssistant:
			flushPending()
			messages = append(messages, provider.Message{
				Role:             "assistant",
				Content:          entry.Content,
				ReasoningContent: entry.ReasoningContent,
				ToolCalls:        append([]provider.ToolCall(nil), entry.ToolCalls...),
			})
			pending = append(pending, entry.ToolCalls...)
		case EntryTool:
			matched := -1
			for index, call := range pending {
				if call.ID == entry.ToolCallID {
					matched = index
					break
				}
			}
			if matched < 0 {
				continue
			}
			pending = append(pending[:matched], pending[matched+1:]...)
			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    entry.Content,
				ToolCallID: entry.ToolCallID,
			})
		}
	}
	flushPending()

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
