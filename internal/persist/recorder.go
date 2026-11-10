// Package persist adapts the public agent recorder contract to session
// storage. It is shared by the TUI and the headless client so both interfaces
// persist at exactly the same durability points.
package persist

import (
	"fmt"
	"sync"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/session"
)

// Recorder durably appends transcript entries to a leased session.
type Recorder struct {
	mu      sync.Mutex
	manager session.Manager
	session *session.Session
	lease   session.Lease
}

// New creates a recorder for a session under an active lease.
func New(manager session.Manager, current *session.Session, lease session.Lease) *Recorder {
	return &Recorder{manager: manager, session: current, lease: lease}
}

// Attach installs a recorder on agents that support durable recording. It
// reports whether the agent accepted it.
func Attach(instance agent.Agent, recorder agent.Recorder) bool {
	target, ok := instance.(agent.RecorderAgent)
	if !ok {
		return false
	}
	target.SetRecorder(recorder)
	return true
}

// AttachToolOutput points an agent's full tool output at the manager's
// session-scoped directory. Agents or managers that do not support this keep
// their ephemeral tool-output store.
func AttachToolOutput(instance agent.Agent, manager session.Manager, sessionID string) error {
	target, ok := instance.(agent.ToolOutputAgent)
	if !ok {
		return nil
	}
	provider, ok := manager.(session.ToolOutputProvider)
	if !ok {
		return nil
	}
	dir, err := provider.ToolOutputDir(sessionID)
	if err != nil {
		return err
	}
	return target.SetToolOutputDir(dir)
}

// Session returns the session being recorded.
func (r *Recorder) Session() *session.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

// Release relinquishes the writer lease. The recorder must not be used after
// Release.
func (r *Recorder) Release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lease := r.lease
	r.lease = nil
	if lease == nil {
		return nil
	}
	return lease.Release()
}

// RecordUser persists a user message before the model call that follows it.
func (r *Recorder) RecordUser(text string) error {
	return r.append(session.Entry{Kind: session.EntryUser, Content: text})
}

// RecordAssistant persists an assistant message before any of its tool calls
// execute.
func (r *Recorder) RecordAssistant(record agent.AssistantRecord) error {
	var usage *provider.Usage
	if record.Usage != (provider.Usage{}) {
		copied := record.Usage
		usage = &copied
	}
	model := record.Model
	if model == "" && r.session != nil {
		model = r.session.Model
	}
	return r.append(session.Entry{
		Kind:             session.EntryAssistant,
		Content:          record.Content,
		ReasoningContent: record.Reasoning,
		ToolCalls:        append([]provider.ToolCall(nil), record.ToolCalls...),
		Usage:            usage,
		Model:            model,
	})
}

// RecordToolResult persists a tool result immediately after execution.
func (r *Recorder) RecordToolResult(record agent.ToolResultRecord) error {
	return r.append(session.Entry{
		Kind:       session.EntryTool,
		Content:    record.Content,
		ToolCallID: record.ToolCallID,
		ToolName:   record.ToolName,
	})
}

func (r *Recorder) append(entry session.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager == nil || r.session == nil {
		return fmt.Errorf("transcript recorder has no session")
	}
	if r.lease == nil {
		return fmt.Errorf("transcript recorder lease is released")
	}
	_, err := r.manager.Append(r.session, entry)
	return err
}
