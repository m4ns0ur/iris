// Package client provides a session-aware headless API for Iris. It is the
// shared foundation for the print, JSONL, and RPC execution modes and can be
// embedded directly by Go applications.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/persist"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/session"
)

// ErrBusy is returned when a session operation is attempted during a run.
var ErrBusy = errors.New("client is running")

// Options configures a Client.
type Options struct {
	// Agent executes prompts. Required.
	Agent agent.Agent

	// Sessions persists conversations. Nil disables persistence.
	Sessions session.Manager

	// SessionDefaults configures new sessions.
	SessionDefaults session.CreateOptions

	// InitialSession optionally resumes an existing conversation. When nil and
	// Sessions is configured, a new unsaved session is created.
	InitialSession *session.Session
}

// Result is the outcome of one headless run.
type Result struct {
	Content             string          `json:"content"`
	Reasoning           string          `json:"reasoning,omitempty"`
	SessionID           string          `json:"session_id,omitempty"`
	Usage               provider.Usage  `json:"usage"`
	ContextSummaryUsage *provider.Usage `json:"context_summary_usage,omitempty"`
	Canceled            bool            `json:"canceled,omitempty"`
	// Truncated reports that the last model turn hit the output token limit.
	Truncated bool `json:"truncated,omitempty"`
	// Summary contains runtime-observed execution metadata: counters, usage,
	// duration, and working-tree changes. It is nil if the agent did not report
	// a summary.
	Summary *agent.RunSummary `json:"summary,omitempty"`
}

// EventRecord is the stable JSON-friendly envelope used by the JSONL and RPC
// modes.
type EventRecord struct {
	Type  agent.EventType `json:"type"`
	Event json.RawMessage `json:"event"`
}

// Record wraps an agent event for structured output.
func Record(event agent.Event) EventRecord {
	encoded, err := json.Marshal(event)
	if err != nil {
		encoded = []byte("null")
	}
	return EventRecord{Type: event.Type(), Event: encoded}
}

// Client runs prompts and manages the current persisted session.
type Client struct {
	agent    agent.Agent
	sessions session.Manager
	defaults session.CreateOptions

	mu            sync.Mutex
	session       *session.Session
	lease         session.Lease
	recorder      *persist.Recorder
	recording     bool
	persisted     int
	dirty         bool
	running       bool
	handler       func(agent.Event)
	runUsage      []provider.Usage
	summaryUse    provider.Usage
	runContent    strings.Builder
	runReasoning  strings.Builder
	canceled      bool
	truncated     bool
	lastTurnBytes int
	summary       *agent.RunSummary
}

// New creates a headless client.
func New(options Options) (*Client, error) {
	if options.Agent == nil {
		return nil, fmt.Errorf("client requires an agent")
	}

	c := &Client{
		agent:    options.Agent,
		sessions: options.Sessions,
		defaults: options.SessionDefaults,
	}
	c.agent.Subscribe(c.handleEvent)

	if options.Sessions == nil {
		if options.InitialSession != nil {
			if err := c.agent.LoadHistory(options.InitialSession.History()); err != nil {
				return nil, err
			}
			c.session = options.InitialSession
			c.persisted = len(c.agent.History())
			logger.SetSession(c.session.ID)
		}
		return c, nil
	}

	if options.InitialSession != nil {
		if err := c.attachSessionLocked(options.InitialSession); err != nil {
			return nil, err
		}
		return c, nil
	}

	created, err := c.sessions.Create(c.defaults)
	if err != nil {
		return nil, err
	}
	if err := c.attachSessionLocked(created); err != nil {
		return nil, err
	}
	return c, nil
}

// attachSessionLocked acquires the writer lease, recovers the session, installs
// the recorder, and loads the model context. The caller must hold c.mu.
func (c *Client) attachSessionLocked(next *session.Session) error {
	if c.sessions == nil {
		return fmt.Errorf("session persistence is not configured")
	}
	if next == nil {
		return fmt.Errorf("session is nil")
	}

	lease, err := c.sessions.Acquire(next)
	if err != nil {
		return err
	}
	if err := persist.AttachToolOutput(c.agent, c.sessions, next.ID); err != nil {
		_ = lease.Release()
		return err
	}
	if err := c.agent.LoadHistory(next.History()); err != nil {
		_ = lease.Release()
		return err
	}

	recorder := persist.New(c.sessions, next, lease)
	recording := persist.Attach(c.agent, recorder)
	if !recording {
		// The agent does not support live recording; keep the lease and use
		// the end-of-run fallback, but ensure no previous recorder remains
		// attached if this is a switch.
		persist.Attach(c.agent, nil)
	}

	previousLease := c.lease
	c.session = next
	c.lease = lease
	c.recorder = recorder
	c.recording = recording
	c.persisted = len(c.agent.History())
	c.dirty = false
	if previousLease != nil {
		_ = previousLease.Release()
	}
	logger.SetSession(next.ID)
	return nil
}

func (c *Client) handleEvent(event agent.Event) {
	c.mu.Lock()
	switch e := event.(type) {
	case agent.TurnStarted:
		c.runContent.Reset()
		c.runReasoning.Reset()
		c.lastTurnBytes = 0
	case agent.AssistantDelta:
		c.runContent.WriteString(e.Text)
	case agent.ReasoningDelta:
		c.runReasoning.WriteString(e.Text)
	case agent.Usage:
		c.runUsage = append(c.runUsage, provider.Usage{
			PromptTokens:     e.PromptTokens,
			CompletionTokens: e.CompletionTokens,
			TotalTokens:      e.TotalTokens,
		})
	case agent.ContextCompactionFinished:
		c.summaryUse.PromptTokens += e.SummaryPromptTokens
		c.summaryUse.CompletionTokens += e.SummaryCompletionTokens
		c.summaryUse.TotalTokens += e.SummaryPromptTokens + e.SummaryCompletionTokens
	case agent.TurnRetried:
		// The failed attempt's partial output is not part of the result.
		c.runContent.Reset()
		c.runReasoning.Reset()
		c.lastTurnBytes = 0
	case agent.TurnFinished:
		c.truncated = e.Err == "" && !e.Canceled && provider.IsTruncatedFinish(e.FinishReason)
		c.lastTurnBytes = e.ContentBytes + e.ReasoningBytes
	case agent.AgentFinished:
		c.canceled = e.Canceled
		c.summary = e.Summary
	}
	handler := c.handler
	c.mu.Unlock()

	if handler != nil {
		handler(event)
	}
}

// Run executes a prompt and returns the final assistant response.
func (c *Client) Run(ctx context.Context, prompt string) (Result, error) {
	return c.RunStream(ctx, prompt, nil)
}

// RunStream executes a prompt and forwards typed events to handler. The
// handler runs synchronously in the agent run goroutine.
func (c *Client) RunStream(
	ctx context.Context,
	prompt string,
	handler func(agent.Event),
) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(prompt) == "" {
		return Result{}, fmt.Errorf("prompt is required")
	}

	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return Result{}, ErrBusy
	}
	c.running = true
	c.handler = handler
	c.runUsage = nil
	c.summaryUse = provider.Usage{}
	c.runContent.Reset()
	c.runReasoning.Reset()
	c.canceled = false
	c.truncated = false
	c.lastTurnBytes = 0
	c.summary = nil
	c.mu.Unlock()

	runErr := c.agent.Run(ctx, prompt)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.running = false
	c.handler = nil

	history := c.agent.History()
	content := c.runContent.String()
	reasoning := c.runReasoning.String()
	if content == "" && reasoning == "" && c.lastTurnBytes > 0 {
		content, reasoning = lastAssistant(history)
	}
	var summaryUsage *provider.Usage
	if c.summaryUse.PromptTokens > 0 || c.summaryUse.CompletionTokens > 0 {
		copied := c.summaryUse
		summaryUsage = &copied
	}
	result := Result{
		Content:             content,
		Reasoning:           reasoning,
		Usage:               sumUsage(c.runUsage),
		ContextSummaryUsage: summaryUsage,
		Canceled:            c.canceled || errors.Is(runErr, context.Canceled),
		Truncated:           c.truncated,
		Summary:             c.summary,
	}
	if c.session != nil {
		result.SessionID = c.session.ID
	}

	saveErr := c.persistLocked(history, c.runUsage, c.summaryUse)
	c.runUsage = nil
	c.summaryUse = provider.Usage{}
	c.runContent.Reset()
	c.runReasoning.Reset()
	c.canceled = false
	c.truncated = false
	c.lastTurnBytes = 0
	c.summary = nil

	return result, errors.Join(runErr, saveErr)
}

// Cancel stops the active run, if any.
func (c *Client) Cancel() {
	c.agent.Cancel()
}

// Abort stops the active run and returns instructions that were queued but not
// delivered.
func (c *Client) Abort() agent.Queue {
	return c.agent.Abort()
}

// Steer queues an instruction for the active run. It is delivered at the next
// turn boundary without interrupting the current request or tool.
func (c *Client) Steer(prompt string) error {
	return c.agent.Steer(prompt)
}

// FollowUp queues a request for when the active run would otherwise stop.
func (c *Client) FollowUp(prompt string) error {
	return c.agent.FollowUp(prompt)
}

// ClearQueue removes queued instructions and returns their text. It is safe
// while a run is active.
func (c *Client) ClearQueue() agent.Queue {
	return c.agent.ClearQueue()
}

// Pending returns the queued instructions without clearing them.
func (c *Client) Pending() agent.Queue {
	return c.agent.Pending()
}

// SessionID returns the active session ID, if any.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return ""
	}
	return c.session.ID
}

// IsRunning reports whether a run is active.
func (c *Client) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// NewSession persists the current session and starts an empty one.
func (c *Client) NewSession() (*session.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions == nil {
		return nil, fmt.Errorf("session persistence is not configured")
	}
	if c.running {
		return nil, ErrBusy
	}
	if err := c.persistCurrentLocked(); err != nil {
		return nil, err
	}

	created, err := c.sessions.Create(c.defaults)
	if err != nil {
		return nil, err
	}
	if err := c.attachSessionLocked(created); err != nil {
		return nil, err
	}
	return created, nil
}

// ResumeSession loads a persisted session by ID.
func (c *Client) ResumeSession(id string) (*session.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions == nil {
		return nil, fmt.Errorf("session persistence is not configured")
	}
	if c.running {
		return nil, ErrBusy
	}
	if err := c.persistCurrentLocked(); err != nil {
		return nil, err
	}

	loaded, err := c.sessions.Open(id)
	if err != nil {
		return nil, err
	}
	if c.session != nil && c.session.ID == loaded.ID {
		return c.session, nil
	}
	if err := c.attachSessionLocked(loaded); err != nil {
		return nil, err
	}
	return loaded, nil
}

// ResumeLatest loads the most recently updated session.
func (c *Client) ResumeLatest() (*session.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions == nil {
		return nil, fmt.Errorf("session persistence is not configured")
	}
	if c.running {
		return nil, ErrBusy
	}
	if err := c.persistCurrentLocked(); err != nil {
		return nil, err
	}

	loaded, err := c.sessions.Latest()
	if err != nil {
		return nil, err
	}
	if c.session != nil && c.session.ID == loaded.ID {
		return c.session, nil
	}
	if err := c.attachSessionLocked(loaded); err != nil {
		return nil, err
	}
	return loaded, nil
}

// ForkSession creates a child session from a user entry in the current
// session.
func (c *Client) ForkSession(entryID string) (*session.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions == nil || c.session == nil {
		return nil, fmt.Errorf("no active session")
	}
	if c.running {
		return nil, ErrBusy
	}
	if err := c.persistCurrentLocked(); err != nil {
		return nil, err
	}

	child, err := c.sessions.Fork(c.session.ID, entryID)
	if err != nil {
		return nil, err
	}
	if err := c.attachSessionLocked(child); err != nil {
		return nil, err
	}
	return child, nil
}

// ForkSessionAt forks from a 1-based user-message index in the current
// session.
func (c *Client) ForkSessionAt(index int) (*session.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions == nil || c.session == nil {
		return nil, fmt.Errorf("no active session")
	}
	if c.running {
		return nil, ErrBusy
	}
	if err := c.persistCurrentLocked(); err != nil {
		return nil, err
	}

	users := c.session.UserEntries()
	if index < 1 || index > len(users) {
		return nil, fmt.Errorf("user message index must be between 1 and %d", len(users))
	}

	child, err := c.sessions.Fork(c.session.ID, users[index-1].ID)
	if err != nil {
		return nil, err
	}
	if err := c.attachSessionLocked(child); err != nil {
		return nil, err
	}
	return child, nil
}

// ListSessions returns persisted sessions ordered by most recently updated.
func (c *Client) ListSessions() ([]session.Summary, error) {
	if c.sessions == nil {
		return nil, fmt.Errorf("session persistence is not configured")
	}
	return c.sessions.List()
}

// ExportSession persists the current session and exports it as Markdown.
func (c *Client) ExportSession() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions == nil || c.session == nil {
		return "", fmt.Errorf("no active session")
	}
	if c.running {
		return "", ErrBusy
	}
	if err := c.persistCurrentLocked(); err != nil {
		return "", err
	}
	return c.sessions.ExportMarkdown(c.session)
}

func (c *Client) persistCurrentLocked() error {
	if c.sessions == nil || c.session == nil {
		return nil
	}
	return c.persistLocked(c.agent.History(), nil, provider.Usage{})
}

func (c *Client) persistLocked(
	history []provider.Message,
	usage []provider.Usage,
	summaryUsage provider.Usage,
) error {
	if c.sessions == nil || c.session == nil {
		return nil
	}

	if c.recording {
		changed := false
		if summaryUsage.PromptTokens > 0 || summaryUsage.CompletionTokens > 0 {
			c.session.Usage.PromptTokens += summaryUsage.PromptTokens
			c.session.Usage.CompletionTokens += summaryUsage.CompletionTokens
			c.session.Usage.TotalTokens += summaryUsage.TotalTokens
			changed = true
		}
		if changed {
			c.dirty = true
		}
		c.persisted = len(history)
		if !c.dirty {
			return nil
		}
		if err := c.sessions.Save(c.session); err != nil {
			return err
		}
		c.dirty = false
		return nil
	}

	if c.persisted > len(history) {
		c.persisted = 0
	}
	newMessages := history[c.persisted:]

	toolNames := make(map[string]string)
	for _, entry := range c.session.Entries {
		for _, call := range entry.ToolCalls {
			if call.ID != "" {
				toolNames[call.ID] = call.Function.Name
			}
		}
	}

	usageIndex := 0
	changed := false

	for _, message := range newMessages {
		switch message.Role {
		case "system":
			continue
		case "user":
			c.session.AddUser(messageText(message.Content))
			changed = true
		case "assistant":
			var messageUsage *provider.Usage
			if usageIndex < len(usage) {
				copied := usage[usageIndex]
				messageUsage = &copied
				usageIndex++
			}
			c.session.AddAssistant(
				messageText(message.Content),
				message.ReasoningContent,
				message.ToolCalls,
				messageUsage,
				c.session.Model,
			)
			for _, call := range message.ToolCalls {
				if call.ID != "" {
					toolNames[call.ID] = call.Function.Name
				}
			}
			changed = true
		case "tool":
			c.session.AddTool(
				message.ToolCallID,
				toolNames[message.ToolCallID],
				messageText(message.Content),
			)
			changed = true
		}
	}

	for ; usageIndex < len(usage); usageIndex++ {
		c.session.Usage.PromptTokens += usage[usageIndex].PromptTokens
		c.session.Usage.CompletionTokens += usage[usageIndex].CompletionTokens
		c.session.Usage.TotalTokens += usage[usageIndex].TotalTokens
		changed = true
	}

	if summaryUsage.PromptTokens > 0 || summaryUsage.CompletionTokens > 0 {
		c.session.Usage.PromptTokens += summaryUsage.PromptTokens
		c.session.Usage.CompletionTokens += summaryUsage.CompletionTokens
		c.session.Usage.TotalTokens += summaryUsage.TotalTokens
		changed = true
	}

	c.persisted = len(history)
	if changed {
		c.dirty = true
	}
	if !c.dirty {
		return nil
	}
	if err := c.sessions.Save(c.session); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// Close persists pending metadata and releases the session writer lease. It
// returns ErrBusy while a run is active.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return ErrBusy
	}

	err := c.persistCurrentLocked()
	if c.recorder != nil {
		err = errors.Join(err, c.recorder.Release())
	} else if c.lease != nil {
		err = errors.Join(err, c.lease.Release())
	}
	persist.Attach(c.agent, nil)
	c.recorder = nil
	c.lease = nil
	c.recording = false
	if c.session != nil {
		logger.ClearSession(c.session.ID)
	}
	return err
}

func lastAssistant(history []provider.Message) (string, string) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "assistant" {
			continue
		}
		content := messageText(history[i].Content)
		if content != "" || history[i].ReasoningContent != "" {
			return content, history[i].ReasoningContent
		}
	}
	return "", ""
}

func sumUsage(usages []provider.Usage) provider.Usage {
	var total provider.Usage
	for _, usage := range usages {
		total.PromptTokens += usage.PromptTokens
		total.CompletionTokens += usage.CompletionTokens
		total.TotalTokens += usage.TotalTokens
	}
	return total
}

func messageText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []provider.Content:
		var builder strings.Builder
		for _, part := range value {
			if part.Type == "text" {
				builder.WriteString(part.Text)
			}
		}
		return builder.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
