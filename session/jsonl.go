package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

const (
	sessionFileExt   = ".jsonl"
	lockFileExt      = ".lock"
	recordSession    = "session"
	recordEntry      = "entry"
	recordCheckpoint = "checkpoint"
	maxLineBytes     = 16 * 1024 * 1024
)

type record struct {
	Type       string      `json:"type"`
	Version    int         `json:"version,omitempty"`
	Session    *Metadata   `json:"session,omitempty"`
	Entry      *Entry      `json:"entry,omitempty"`
	Checkpoint *checkpoint `json:"checkpoint,omitempty"`
}

// checkpoint persists the aggregate metadata after an append. The header is
// written once by Save; appends leave it untouched and add a checkpoint so the
// common path stays O(1) and crash-safe.
type checkpoint struct {
	Revision  uint64         `json:"revision"`
	UpdatedAt time.Time      `json:"updated_at"`
	Usage     provider.Usage `json:"usage"`
}

// JSONLManager stores one append-only JSONL journal per session. The first
// record is the versioned session header; entries and checkpoints follow.
type JSONLManager struct {
	dir string

	mu     sync.Mutex
	leases map[string]*fileLease
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

	return &JSONLManager{
		dir:    absolute,
		leases: make(map[string]*fileLease),
	}, nil
}

// Dir returns the session directory.
func (m *JSONLManager) Dir() string {
	return m.dir
}

// Create returns a new unsaved session.
func (m *JSONLManager) Create(options CreateOptions) (*Session, error) {
	return NewSession(options), nil
}

// Acquire takes an exclusive writer lease and reloads the session from disk
// under that lease. Recoverable damage (a partial final record, a legacy
// header, or a tool call without a result) is repaired and persisted before
// Acquire returns.
func (m *JSONLManager) Acquire(session *Session) (Lease, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if !validID(session.ID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSessionID, session.ID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.leases == nil {
		m.leases = make(map[string]*fileLease)
	}
	if current := m.leases[session.ID]; current != nil {
		return nil, fmt.Errorf("%w: %s", ErrSessionLocked, session.ID)
	}

	lockPath := m.lockPath(session.ID)
	lockFile, err := acquireLeaseFile(lockPath)
	if err != nil {
		return nil, err
	}

	loaded, err := m.openLocked(session.ID)
	switch {
	case err == nil:
		*session = *loaded
	case errors.Is(err, ErrSessionNotFound):
		// A new unsaved session has no file yet. Keep its metadata, but a
		// missing file means there is no persisted revision to match.
		session.Revision = 0
	default:
		_ = releaseLeaseFile(lockPath, lockFile)
		return nil, err
	}

	if session.needsRepair {
		// Tool-call repair only happens under the exclusive writer lease, so a
		// live run cannot race a read-only Open into synthesizing a result.
		recoverInterruptedToolCalls(session)
		if err := m.saveLocked(session); err != nil {
			_ = releaseLeaseFile(lockPath, lockFile)
			return nil, err
		}
		session.needsRepair = false
	}

	lease := &fileLease{
		manager: m,
		id:      session.ID,
		path:    lockPath,
		file:    lockFile,
	}
	m.leases[session.ID] = lease
	return lease, nil
}

// Save atomically rewrites the complete session. If the file exists, the
// session's Revision must match the current file revision; otherwise Save
// returns ErrSessionConflict. Save uses an existing lease when this manager
// holds one, or a transient lease otherwise.
func (m *JSONLManager) Save(session *Session) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	if !validID(session.ID) {
		return fmt.Errorf("%w: %q", ErrInvalidSessionID, session.ID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.leases[session.ID] != nil {
		return m.saveLocked(session)
	}

	lockPath := m.lockPath(session.ID)
	lockFile, err := acquireLeaseFile(lockPath)
	if err != nil {
		return err
	}
	defer func() { _ = releaseLeaseFile(lockPath, lockFile) }()
	return m.saveLocked(session)
}

func (m *JSONLManager) saveLocked(session *Session) error {
	current, err := m.currentRevisionLocked(session.ID)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		return err
	}
	if session.Revision != current {
		return fmt.Errorf(
			"%w: session %s is at revision %d, writer has %d",
			ErrSessionConflict,
			session.ID,
			current,
			session.Revision,
		)
	}

	next := current + 1
	updated := time.Now().UTC()
	if session.UpdatedAt.After(updated) {
		updated = session.UpdatedAt
	}
	header := session.Metadata
	header.Revision = next
	header.UpdatedAt = updated

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
	if err := encoder.Encode(record{
		Type:    recordSession,
		Version: FormatVersion,
		Session: &header,
	}); err != nil {
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
	_ = syncDirectory(m.dir)

	session.Revision = next
	session.UpdatedAt = updated
	session.needsRepair = false
	return nil
}

// Append durably journals one entry and fsyncs it. The session must be under
// an Acquire lease. A failed append leaves the in-memory session unchanged.
func (m *JSONLManager) Append(session *Session, entry Entry) (Entry, error) {
	if session == nil {
		return Entry{}, fmt.Errorf("session is nil")
	}
	if !validID(session.ID) {
		return Entry{}, fmt.Errorf("%w: %q", ErrInvalidSessionID, session.ID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.leases[session.ID] == nil {
		return Entry{}, fmt.Errorf("%w: acquire session %s before appending", ErrSessionLocked, session.ID)
	}

	prepared := session.prepareEntry(entry)

	// A brand-new session has no header yet. Write it together with the first
	// entry, using the full atomic save path.
	if session.Revision == 0 {
		beforeLen := len(session.Entries)
		beforeUsage := session.Usage
		beforeUpdated := session.UpdatedAt
		session.commitEntry(prepared)
		if err := m.saveLocked(session); err != nil {
			session.Entries = session.Entries[:beforeLen]
			session.Usage = beforeUsage
			session.UpdatedAt = beforeUpdated
			return Entry{}, err
		}
		return prepared, nil
	}

	if err := m.ensureCleanTailLocked(session.ID); err != nil {
		return Entry{}, err
	}

	nextRevision := session.Revision + 1
	updated := prepared.CreatedAt
	if session.UpdatedAt.After(updated) {
		updated = session.UpdatedAt
	}
	newUsage := session.Usage
	if prepared.Usage != nil {
		newUsage.PromptTokens += prepared.Usage.PromptTokens
		newUsage.CompletionTokens += prepared.Usage.CompletionTokens
		newUsage.TotalTokens += prepared.Usage.TotalTokens
	}

	payload, err := encodeRecords(
		record{Type: recordEntry, Entry: &prepared},
		record{
			Type: recordCheckpoint,
			Checkpoint: &checkpoint{
				Revision:  nextRevision,
				UpdatedAt: updated,
				Usage:     newUsage,
			},
		},
	)
	if err != nil {
		return Entry{}, err
	}
	if err := m.appendLocked(session.ID, payload); err != nil {
		return Entry{}, err
	}

	session.commitEntry(prepared)
	session.Revision = nextRevision
	if updated.After(session.UpdatedAt) {
		session.UpdatedAt = updated
	}
	session.needsRepair = false
	return prepared, nil
}

// ensureCleanTailLocked removes a partial final line left by a failed append.
// Appends must never follow a partial record, or a later loader would reject
// the journal as corrupt in the middle.
func (m *JSONLManager) ensureCleanTailLocked(id string) error {
	path := m.path(id)
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open session journal tail: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat session journal tail: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("session journal %q is empty", id)
	}

	last := []byte{0}
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		return fmt.Errorf("read session journal tail: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}

	const chunkSize = 4096
	offset := info.Size()
	for offset > 0 {
		start := offset - chunkSize
		if start < 0 {
			start = 0
		}
		buffer := make([]byte, offset-start)
		if _, err := file.ReadAt(buffer, start); err != nil {
			return fmt.Errorf("read session journal tail: %w", err)
		}
		if index := bytes.LastIndexByte(buffer, '\n'); index >= 0 {
			cut := start + int64(index) + 1
			if err := file.Truncate(cut); err != nil {
				return fmt.Errorf("repair session journal tail: %w", err)
			}
			if err := file.Sync(); err != nil {
				return fmt.Errorf("sync session journal tail: %w", err)
			}
			return nil
		}
		offset = start
	}

	// No newline at all means the header itself is unusable; do not append to
	// a journal that Acquire should have rejected.
	return fmt.Errorf("session journal %q has no complete record", id)
}

func (m *JSONLManager) appendLocked(id string, payload []byte) error {
	path := m.path(id)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open session journal: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat session journal: %w", err)
	}
	start := info.Size()

	written := 0
	for written < len(payload) {
		count, writeErr := file.Write(payload[written:])
		if writeErr != nil {
			_ = file.Truncate(start)
			_ = file.Sync()
			_ = file.Close()
			return fmt.Errorf("append session journal: %w", writeErr)
		}
		if count == 0 {
			_ = file.Truncate(start)
			_ = file.Sync()
			_ = file.Close()
			return fmt.Errorf("append session journal: %w", io.ErrShortWrite)
		}
		written += count
	}

	if err := file.Sync(); err != nil {
		_ = file.Truncate(start)
		_ = file.Sync()
		_ = file.Close()
		return fmt.Errorf("sync session journal: %w", err)
	}
	// The fsync above is the durability barrier. A close error after a
	// successful sync should not make the caller roll back an entry that is
	// already durable in the journal.
	_ = file.Close()
	return nil
}

// Open loads a persisted session without acquiring a writer lease. A partial
// final record is ignored (and repaired by Acquire); damage in the middle of
// the journal is an error.
func (m *JSONLManager) Open(id string) (*Session, error) {
	if !validID(id) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSessionID, id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openLocked(id)
}

func (m *JSONLManager) openLocked(id string) (*Session, error) {
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

	headerSeen := false
	checkpointSeen := false
	entriesAfterCheckpoint := false
	legacy := false
	recovered := false
	malformed := false
	var computedUsage provider.Usage
	var maxEntryTime time.Time

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			if malformed {
				continue
			}
			malformed = true
			recovered = true
			continue
		}
		if malformed {
			return nil, fmt.Errorf("decode session %q: corrupt record before the end of the journal", id)
		}

		switch rec.Type {
		case recordSession:
			if headerSeen {
				return nil, fmt.Errorf("session %q contains multiple headers", id)
			}
			if rec.Session == nil {
				return nil, fmt.Errorf("session %q has an empty header", id)
			}
			if rec.Version > FormatVersion {
				return nil, fmt.Errorf("%w: session %q uses version %d", ErrUnsupportedSessionVersion, id, rec.Version)
			}
			if rec.Version < FormatVersion {
				legacy = true
			}
			session.Metadata = *rec.Session
			headerSeen = true

		case recordEntry:
			if rec.Entry == nil {
				if malformed {
					continue
				}
				malformed = true
				recovered = true
				continue
			}
			entry := *rec.Entry
			session.Entries = append(session.Entries, entry)
			entriesAfterCheckpoint = true
			if entry.CreatedAt.After(maxEntryTime) {
				maxEntryTime = entry.CreatedAt
			}
			if entry.Usage != nil {
				computedUsage.PromptTokens += entry.Usage.PromptTokens
				computedUsage.CompletionTokens += entry.Usage.CompletionTokens
				computedUsage.TotalTokens += entry.Usage.TotalTokens
			}

		case recordCheckpoint:
			if rec.Checkpoint == nil {
				if malformed {
					continue
				}
				malformed = true
				recovered = true
				continue
			}
			if rec.Checkpoint.Revision >= session.Revision {
				session.Revision = rec.Checkpoint.Revision
				session.UpdatedAt = rec.Checkpoint.UpdatedAt
				session.Usage = rec.Checkpoint.Usage
				checkpointSeen = true
				entriesAfterCheckpoint = false
			}

		default:
			// Unknown record types are ignored for forward compatibility within
			// a supported schema version.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session %q: %w", id, err)
	}

	if !headerSeen {
		return nil, fmt.Errorf("session %q has no valid header", id)
	}
	if session.ID != id {
		return nil, fmt.Errorf("session file %q contains session %q", id, session.ID)
	}

	if checkpointSeen && entriesAfterCheckpoint {
		// An entry was journaled after the last complete checkpoint (for
		// example, the checkpoint write was lost). Rebuild the aggregate.
		session.Usage = computedUsage
		if !maxEntryTime.IsZero() && maxEntryTime.After(session.UpdatedAt) {
			session.UpdatedAt = maxEntryTime
		}
	} else if !checkpointSeen && !maxEntryTime.IsZero() && maxEntryTime.After(session.UpdatedAt) {
		// The header may be older than an entry written without a checkpoint
		// (legacy files or a hand-edited journal).
		session.UpdatedAt = maxEntryTime
	}

	for i := range session.Entries {
		session.Entries[i].SessionID = session.ID
	}

	if hasInterruptedToolCalls(session.Entries) || needsToolIdentityRepair(session.Entries) {
		recovered = true
	}
	if checkpointSeen && entriesAfterCheckpoint {
		// The journal contains an entry after the last complete checkpoint,
		// which means an append was interrupted before its checkpoint.
		recovered = true
	}

	session.Recovered = recovered
	session.needsRepair = recovered || legacy
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
	m.mu.Lock()
	defer m.mu.Unlock()

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
		id := strings.TrimSuffix(entry.Name(), sessionFileExt)
		if !validID(id) {
			continue
		}

		summary, err := m.readSummary(filepath.Join(m.dir, entry.Name()))
		if err != nil || summary.ID != id {
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
	fmt.Fprintf(&builder, "- Format version: %d\n", FormatVersion)
	fmt.Fprintf(&builder, "- Revision: %d\n", session.Revision)
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
			if entry.Status != "" {
				fmt.Fprintf(&builder, " [%s]", entry.Status)
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
	headerSeen := false
	checkpointSeen := false
	var revision uint64
	entriesAfterCheckpoint := false
	malformed := false
	var maxEntryTime time.Time

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			if malformed {
				continue
			}
			malformed = true
			continue
		}
		if malformed {
			return Summary{}, fmt.Errorf("corrupt record before the end of the journal")
		}

		switch rec.Type {
		case recordSession:
			if rec.Session == nil {
				return Summary{}, fmt.Errorf("empty session header")
			}
			if rec.Version > FormatVersion {
				return Summary{}, fmt.Errorf("%w: version %d", ErrUnsupportedSessionVersion, rec.Version)
			}
			headerSeen = true
			revision = rec.Session.Revision
			summary.ID = rec.Session.ID
			summary.CreatedAt = rec.Session.CreatedAt
			summary.UpdatedAt = rec.Session.UpdatedAt
			summary.Model = rec.Session.Model
			summary.Workspace = rec.Session.Workspace
			summary.ParentSessionID = rec.Session.ParentSessionID
		case recordEntry:
			if rec.Entry == nil {
				if malformed {
					continue
				}
				malformed = true
				continue
			}
			summary.EntryCount++
			entriesAfterCheckpoint = true
			if rec.Entry.CreatedAt.After(maxEntryTime) {
				maxEntryTime = rec.Entry.CreatedAt
			}
		case recordCheckpoint:
			if rec.Checkpoint == nil {
				if malformed {
					continue
				}
				malformed = true
				continue
			}
			if rec.Checkpoint.Revision >= revision {
				revision = rec.Checkpoint.Revision
				summary.UpdatedAt = rec.Checkpoint.UpdatedAt
				checkpointSeen = true
				entriesAfterCheckpoint = false
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, err
	}
	if !headerSeen || summary.ID == "" {
		return Summary{}, ErrSessionNotFound
	}
	if checkpointSeen && entriesAfterCheckpoint {
		if !maxEntryTime.IsZero() && maxEntryTime.After(summary.UpdatedAt) {
			summary.UpdatedAt = maxEntryTime
		}
	} else if !checkpointSeen && !maxEntryTime.IsZero() && maxEntryTime.After(summary.UpdatedAt) {
		summary.UpdatedAt = maxEntryTime
	}
	return summary, nil
}

func (m *JSONLManager) currentRevisionLocked(id string) (uint64, error) {
	file, err := os.Open(m.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrSessionNotFound
		}
		return 0, err
	}
	defer file.Close()

	var revision uint64
	scanner := newSessionScanner(file)
	malformed := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			if malformed {
				continue
			}
			malformed = true
			continue
		}
		if malformed {
			return 0, fmt.Errorf("decode session %q: corrupt record before the end of the journal", id)
		}
		switch rec.Type {
		case recordSession:
			if rec.Version > FormatVersion {
				return 0, fmt.Errorf("%w: session %q uses version %d", ErrUnsupportedSessionVersion, id, rec.Version)
			}
			if rec.Session != nil && rec.Session.Revision > revision {
				revision = rec.Session.Revision
			}
		case recordCheckpoint:
			if rec.Checkpoint != nil && rec.Checkpoint.Revision > revision {
				revision = rec.Checkpoint.Revision
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return revision, nil
}

func (m *JSONLManager) path(id string) string {
	return filepath.Join(m.dir, id+sessionFileExt)
}

func newSessionScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	return scanner
}

func encodeRecords(records ...record) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for i := range records {
		if err := encoder.Encode(&records[i]); err != nil {
			return nil, fmt.Errorf("encode session record: %w", err)
		}
	}
	return buffer.Bytes(), nil
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
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
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	_ = syncDirectory(dir)
	return nil
}
