package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	sessionFileExt = ".jsonl"
	recordSession  = "session"
	recordEntry    = "entry"
	maxLineBytes   = 16 * 1024 * 1024
)

type record struct {
	Type    string    `json:"type"`
	Session *Metadata `json:"session,omitempty"`
	Entry   *Entry    `json:"entry,omitempty"`
}

// JSONLManager stores one JSONL file per session. The first record is the
// session header; subsequent records are conversation entries.
type JSONLManager struct {
	dir string

	mu sync.Mutex
}

var _ Manager = (*JSONLManager)(nil)

// NewJSONL creates a JSONL session manager rooted at dir.
func NewJSONL(dir string) (*JSONLManager, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("session directory is required")
	}

	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve session directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	_ = os.Chmod(absolute, 0o700)

	return &JSONLManager{dir: absolute}, nil
}

// Dir returns the session directory.
func (m *JSONLManager) Dir() string {
	return m.dir
}

// Create returns a new unsaved session.
func (m *JSONLManager) Create(options CreateOptions) (*Session, error) {
	return NewSession(options), nil
}

// Save atomically writes the session header and all entries.
func (m *JSONLManager) Save(session *Session) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	if !validID(session.ID) {
		return fmt.Errorf("%w: %q", ErrInvalidSessionID, session.ID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session.UpdatedAt = time.Now().UTC()

	file, err := os.CreateTemp(m.dir, ".session-*")
	if err != nil {
		return fmt.Errorf("create session temp file: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod session temp file: %w", err)
	}

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(record{Type: recordSession, Session: &session.Metadata}); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode session header: %w", err)
	}
	for i := range session.Entries {
		entry := &session.Entries[i]
		if err := encoder.Encode(record{Type: recordEntry, Entry: entry}); err != nil {
			_ = file.Close()
			return fmt.Errorf("encode session entry %q: %w", entry.ID, err)
		}
	}

	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync session file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close session file: %w", err)
	}
	if err := os.Rename(tempPath, m.path(session.ID)); err != nil {
		return fmt.Errorf("publish session file: %w", err)
	}

	return nil
}

// Open loads a persisted session by ID.
func (m *JSONLManager) Open(id string) (*Session, error) {
	if !validID(id) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSessionID, id)
	}

	file, err := os.Open(m.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
		}
		return nil, err
	}
	defer file.Close()

	session := &Session{}
	scanner := newSessionScanner(file)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var record record
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decode session %q: %w", id, err)
		}

		switch record.Type {
		case recordSession:
			if record.Session == nil {
				return nil, fmt.Errorf("session %q has an empty header", id)
			}
			session.Metadata = *record.Session
		case recordEntry:
			if record.Entry == nil {
				return nil, fmt.Errorf("session %q has an empty entry", id)
			}
			session.Entries = append(session.Entries, *record.Entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session %q: %w", id, err)
	}

	if session.ID == "" {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if session.ID != id {
		return nil, fmt.Errorf("session file %q contains session %q", id, session.ID)
	}

	for i := range session.Entries {
		session.Entries[i].SessionID = session.ID
	}

	return session, nil
}

// Latest opens the most recently updated session.
func (m *JSONLManager) Latest() (*Session, error) {
	summaries, err := m.List()
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, ErrNoSessions
	}
	return m.Open(summaries[0].ID)
}

// List returns persisted sessions ordered by most recently updated.
func (m *JSONLManager) List() ([]Summary, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionFileExt) {
			continue
		}

		summary, err := m.readSummary(filepath.Join(m.dir, entry.Name()))
		if err != nil {
			continue
		}
		summaries = append(summaries, summary)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})

	return summaries, nil
}

// Fork creates a child session from a user message in an existing session.
func (m *JSONLManager) Fork(sessionID, fromEntryID string) (*Session, error) {
	parent, err := m.Open(sessionID)
	if err != nil {
		return nil, err
	}

	index := -1
	for i, entry := range parent.Entries {
		if entry.ID == fromEntryID {
			if entry.Kind != EntryUser {
				return nil, fmt.Errorf("%w: %s", ErrInvalidForkPoint, fromEntryID)
			}
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidForkPoint, fromEntryID)
	}

	child := NewSession(CreateOptions{
		Model:             parent.Model,
		ModelMetadata:     parent.ModelMetadata,
		Workspace:         parent.Workspace,
		ParentSessionID:   parent.ID,
		ForkedFromEntryID: fromEntryID,
	})
	child.Entries = append([]Entry(nil), parent.Entries[:index+1]...)
	for i := range child.Entries {
		child.Entries[i].SessionID = child.ID
		if child.Entries[i].Usage != nil {
			child.Usage.PromptTokens += child.Entries[i].Usage.PromptTokens
			child.Usage.CompletionTokens += child.Entries[i].Usage.CompletionTokens
			child.Usage.TotalTokens += child.Entries[i].Usage.TotalTokens
		}
	}

	if err := m.Save(child); err != nil {
		return nil, err
	}
	return child, nil
}

// ExportMarkdown writes a human-readable export under <dir>/exports.
func (m *JSONLManager) ExportMarkdown(session *Session) (string, error) {
	if session == nil {
		return "", fmt.Errorf("session is nil")
	}
	if !validID(session.ID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSessionID, session.ID)
	}

	exportDir := filepath.Join(m.dir, "exports")
	if err := os.MkdirAll(exportDir, 0o700); err != nil {
		return "", fmt.Errorf("create export directory: %w", err)
	}
	_ = os.Chmod(exportDir, 0o700)

	var builder strings.Builder
	fmt.Fprintf(&builder, "# Iris Session %s\n\n", session.ID)
	fmt.Fprintf(&builder, "- Created: %s\n", session.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&builder, "- Updated: %s\n", session.UpdatedAt.Format(time.RFC3339))
	if session.Model != "" {
		fmt.Fprintf(&builder, "- Model: %s\n", session.Model)
	}
	if session.Workspace != "" {
		fmt.Fprintf(&builder, "- Workspace: %s\n", session.Workspace)
	}
	if session.ParentSessionID != "" {
		fmt.Fprintf(&builder, "- Parent session: %s\n", session.ParentSessionID)
	}
	if session.ForkedFromEntryID != "" {
		fmt.Fprintf(&builder, "- Forked from entry: %s\n", session.ForkedFromEntryID)
	}
	fmt.Fprintf(
		&builder,
		"- Tokens: prompt=%d completion=%d total=%d\n",
		session.Usage.PromptTokens,
		session.Usage.CompletionTokens,
		session.Usage.TotalTokens,
	)

	keys := make([]string, 0, len(session.ModelMetadata))
	for key := range session.ModelMetadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&builder, "- Model metadata %s: %s\n", key, session.ModelMetadata[key])
	}

	builder.WriteString("\n")

	for _, entry := range session.Entries {
		fmt.Fprintf(&builder, "<!-- entry %s parent %s -->\n\n", entry.ID, entry.ParentID)
		switch entry.Kind {
		case EntryUser:
			builder.WriteString("## User\n\n")
			builder.WriteString(entry.Content)
			builder.WriteString("\n\n")
		case EntryAssistant:
			builder.WriteString("## Assistant\n\n")
			if entry.Content != "" {
				builder.WriteString(entry.Content)
				builder.WriteString("\n\n")
			}
			if entry.ReasoningContent != "" {
				builder.WriteString("<details><summary>Reasoning</summary>\n\n")
				builder.WriteString(entry.ReasoningContent)
				builder.WriteString("\n\n</details>\n\n")
			}
			if len(entry.ToolCalls) > 0 {
				calls, _ := json.MarshalIndent(entry.ToolCalls, "", "  ")
				builder.WriteString("Tool calls:\n\n```json\n")
				builder.Write(calls)
				builder.WriteString("\n```\n\n")
			}
		case EntryTool:
			fmt.Fprintf(&builder, "### Tool result: %s", entry.ToolName)
			if entry.ToolCallID != "" {
				fmt.Fprintf(&builder, " (%s)", entry.ToolCallID)
			}
			builder.WriteString("\n\n```text\n")
			builder.WriteString(entry.Content)
			builder.WriteString("\n```\n\n")
		}
	}

	path := filepath.Join(exportDir, session.ID+".md")
	if err := writeFileAtomic(path, []byte(builder.String())); err != nil {
		return "", err
	}
	return path, nil
}

func (m *JSONLManager) readSummary(path string) (Summary, error) {
	file, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer file.Close()

	summary := Summary{}
	scanner := newSessionScanner(file)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record record
		if err := json.Unmarshal(line, &record); err != nil {
			return Summary{}, err
		}
		switch record.Type {
		case recordSession:
			if record.Session == nil {
				return Summary{}, fmt.Errorf("empty session header")
			}
			summary.ID = record.Session.ID
			summary.CreatedAt = record.Session.CreatedAt
			summary.UpdatedAt = record.Session.UpdatedAt
			summary.Model = record.Session.Model
			summary.Workspace = record.Session.Workspace
			summary.ParentSessionID = record.Session.ParentSessionID
		case recordEntry:
			summary.EntryCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, err
	}
	if summary.ID == "" {
		return Summary{}, ErrSessionNotFound
	}
	return summary, nil
}

func (m *JSONLManager) path(id string) string {
	return filepath.Join(m.dir, id+sessionFileExt)
}

func newSessionScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	return scanner
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, char := range id {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_':
		default:
			return false
		}
	}
	return true
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".export-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
