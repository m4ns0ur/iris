package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/resource"
	"github.com/m4ns0ur/iris/session"
)

type fakeAgent struct {
	history   []provider.Message
	approver  agent.Approver
	steering  []string
	followUps []string
	steerErr  error
}

func (f *fakeAgent) Run(context.Context, string) error { return nil }
func (f *fakeAgent) Subscribe(func(agent.Event))       {}
func (f *fakeAgent) Cancel()                           {}
func (f *fakeAgent) Abort() agent.Queue {
	queue := f.Pending()
	f.steering = nil
	f.followUps = nil
	return queue
}
func (f *fakeAgent) Steer(input string) error {
	if f.steerErr != nil {
		return f.steerErr
	}
	f.steering = append(f.steering, input)
	return nil
}
func (f *fakeAgent) FollowUp(input string) error {
	f.followUps = append(f.followUps, input)
	return nil
}
func (f *fakeAgent) ClearQueue() agent.Queue {
	queue := f.Pending()
	f.steering = nil
	f.followUps = nil
	return queue
}
func (f *fakeAgent) Pending() agent.Queue {
	return agent.Queue{
		Steering:  append([]string(nil), f.steering...),
		FollowUps: append([]string(nil), f.followUps...),
	}
}
func (f *fakeAgent) Close() error { return nil }
func (f *fakeAgent) SetApprover(approver agent.Approver) {
	f.approver = approver
}
func (f *fakeAgent) Approver() agent.Approver       { return f.approver }
func (f *fakeAgent) ApprovalTimeout() time.Duration { return time.Minute }
func (f *fakeAgent) History() []provider.Message {
	return append([]provider.Message(nil), f.history...)
}
func (f *fakeAgent) LoadHistory(messages []provider.Message) error {
	f.history = append([]provider.Message(nil), messages...)
	return nil
}

func newSessionModel(t *testing.T) (*model, *fakeAgent, *session.JSONLManager) {
	t.Helper()

	manager, err := session.NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}

	fake := &fakeAgent{}
	m, err := initialModel(Options{
		Agent:    fake,
		Sessions: manager,
		SessionDefaults: session.CreateOptions{
			Model:         "test-model",
			Workspace:     "/workspace",
			ModelMetadata: map[string]string{"provider": "test"},
		},
	})
	if err != nil {
		t.Fatalf("initialModel() error = %v", err)
	}

	return &m, fake, manager
}

func userMessage(text string) provider.Message {
	return provider.Message{
		Role:    "user",
		Content: []provider.Content{{Type: "text", Text: text}},
	}
}

func TestSessionPersistenceFromAgentHistory(t *testing.T) {
	m, fake, manager := newSessionModel(t)

	fake.history = []provider.Message{
		userMessage("hello"),
		{Role: "assistant", Content: "hi"},
	}

	updated, _ := m.Update(agentEventMsg{event: agent.Usage{
		At:               time.Now(),
		PromptTokens:     10,
		CompletionTokens: 2,
		TotalTokens:      12,
	}})
	m = updated.(*model)

	updated, _ = m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)

	summaries, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("sessions = %d, want 1", len(summaries))
	}

	loaded, err := manager.Open(summaries[0].ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.Entries[0].Kind != session.EntryUser || loaded.Entries[0].Content != "hello" {
		t.Fatalf("user entry = %+v", loaded.Entries[0])
	}
	if loaded.Entries[1].Usage == nil || loaded.Entries[1].Usage.TotalTokens != 12 {
		t.Fatalf("assistant usage = %+v", loaded.Entries[1].Usage)
	}
	if m.currentSession.ID != loaded.ID {
		t.Fatalf("current session = %q, want %q", m.currentSession.ID, loaded.ID)
	}
}

func TestNewResumeForkExportCommands(t *testing.T) {
	m, fake, manager := newSessionModel(t)

	// Persist a conversation in the initial session.
	fake.history = []provider.Message{
		userMessage("first"),
		{Role: "assistant", Content: "answer one"},
		userMessage("second"),
	}
	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)
	originalID := m.currentSession.ID

	// /new creates an empty session and clears agent history.
	updated, _ = m.handleCommand("/new")
	m = updated.(*model)
	if m.currentSession.ID == originalID {
		t.Fatal("/new reused the current session")
	}
	if len(fake.history) != 0 {
		t.Fatalf("agent history after /new = %d, want 0", len(fake.history))
	}

	// /resume loads the saved session.
	updated, _ = m.handleCommand("/resume " + originalID)
	m = updated.(*model)
	if m.currentSession.ID != originalID {
		t.Fatalf("resumed session = %q, want %q", m.currentSession.ID, originalID)
	}
	if len(fake.history) != 3 {
		t.Fatalf("agent history after /resume = %d, want 3", len(fake.history))
	}

	// /fork 2 copies through the second user message.
	updated, _ = m.handleCommand("/fork 2")
	m = updated.(*model)
	forkID := m.currentSession.ID
	if forkID == originalID {
		t.Fatal("/fork reused the parent session")
	}
	if m.currentSession.ParentSessionID != originalID {
		t.Fatalf("fork parent = %q, want %q", m.currentSession.ParentSessionID, originalID)
	}
	if len(m.currentSession.Entries) != 3 {
		t.Fatalf("fork entries = %d, want 3", len(m.currentSession.Entries))
	}
	if len(fake.history) != 3 {
		t.Fatalf("agent history after /fork = %d, want 3", len(fake.history))
	}

	// /export writes a Markdown file and reports its path.
	updated, _ = m.handleCommand("/export")
	m = updated.(*model)

	exportPath := ""
	for _, message := range m.messages {
		if strings.HasPrefix(message.content, "Exported session to ") {
			exportPath = strings.TrimPrefix(message.content, "Exported session to ")
		}
	}
	if exportPath == "" {
		t.Fatalf("export notice not found in %+v", m.messages)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("export file %q: %v", exportPath, err)
	}

	// The fork is persisted and discoverable by the manager.
	if _, err := manager.Open(forkID); err != nil {
		t.Fatalf("Open(fork) error = %v", err)
	}
}

func TestSlashCommandIsNotSentToAgent(t *testing.T) {
	m, fake, _ := newSessionModel(t)
	fake.history = nil

	m.inputText.SetValue("/help")
	updated, cmd := m.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	m = updated.(*model)

	if cmd != nil {
		t.Fatal("slash command returned a run command")
	}
	found := false
	for _, message := range m.messages {
		if strings.Contains(message.content, "Commands:") {
			found = true
		}
		if message.role == "user" {
			t.Fatalf("slash command was added as a user message: %+v", message)
		}
	}
	if !found {
		t.Fatal("help notice was not added")
	}
}

func TestSessionPersistsToolCallsAndUsage(t *testing.T) {
	m, fake, manager := newSessionModel(t)

	fake.history = []provider.Message{
		userMessage("read the file"),
		{
			Role: "assistant",
			ToolCalls: []provider.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: provider.FunctionCall{
					Name:      "read",
					Arguments: `{"path":"a.txt"}`,
				},
			}},
		},
		{
			Role:       "tool",
			Content:    `{"content":"data"}`,
			ToolCallID: "call-1",
		},
		{Role: "assistant", Content: "done"},
	}

	for _, usage := range []agent.Usage{
		{TotalTokens: 10, PromptTokens: 8, CompletionTokens: 2},
		{TotalTokens: 5, PromptTokens: 4, CompletionTokens: 1},
	} {
		updated, _ := m.Update(agentEventMsg{event: usage})
		m = updated.(*model)
	}
	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)

	loaded, err := manager.Open(m.currentSession.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(loaded.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(loaded.Entries))
	}
	if loaded.Entries[1].Kind != session.EntryAssistant || len(loaded.Entries[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool call entry = %+v", loaded.Entries[1])
	}
	if loaded.Entries[2].Kind != session.EntryTool || loaded.Entries[2].ToolName != "read" {
		t.Fatalf("tool entry = %+v", loaded.Entries[2])
	}
	if loaded.Entries[1].Usage == nil || loaded.Entries[1].Usage.TotalTokens != 10 {
		t.Fatalf("first usage = %+v", loaded.Entries[1].Usage)
	}
	if loaded.Entries[3].Usage == nil || loaded.Entries[3].Usage.TotalTokens != 5 {
		t.Fatalf("second usage = %+v", loaded.Entries[3].Usage)
	}
	if loaded.Usage.TotalTokens != 15 {
		t.Fatalf("aggregate usage = %+v, want 15", loaded.Usage)
	}

	history := loaded.History()
	if len(history) != 4 || history[2].ToolCallID != "call-1" {
		t.Fatalf("restored history = %+v", history)
	}
}

func TestContextCompactionNotice(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.state = StateStreaming

	updated, _ := m.Update(agentEventMsg{event: agent.ContextCompactionStarted{
		At:           time.Now(),
		Window:       4096,
		BeforeTokens: 3600,
		Forced:       true,
	}})
	m = updated.(*model)
	if !m.compacting {
		t.Fatal("compaction status was not set")
	}
	if !strings.Contains(m.spinnerBar(), "Compacting context") {
		t.Fatalf("spinner bar = %q, want compaction status", m.spinnerBar())
	}

	updated, _ = m.Update(agentEventMsg{event: agent.ContextCompactionFinished{
		At:                      time.Now(),
		Window:                  4096,
		BeforeTokens:            3600,
		AfterTokens:             1200,
		SummarizedMessages:      24,
		TrimmedToolResults:      2,
		SummaryPromptTokens:     100,
		SummaryCompletionTokens: 30,
		SummaryFallback:         true,
		Forced:                  true,
	}})
	m = updated.(*model)
	if m.compacting {
		t.Fatal("compaction status was not cleared")
	}

	found := false
	for _, message := range m.messages {
		if strings.Contains(message.content, "Context compacted") {
			found = true
			for _, want := range []string{"24 messages summarized", "2 tool results trimmed", "forced after context error", "fallback summary"} {
				if !strings.Contains(message.content, want) {
					t.Fatalf("notice missing %q: %q", want, message.content)
				}
			}
		}
	}
	if !found {
		t.Fatalf("compaction notice not found in %+v", m.messages)
	}
	if m.summaryUsagePending.TotalTokens != 130 {
		t.Fatalf("pending summary usage = %+v, want 130", m.summaryUsagePending)
	}
	m.persistSession()
	if m.currentSession.Usage.TotalTokens != 130 {
		t.Fatalf("session summary usage = %+v, want 130", m.currentSession.Usage)
	}
}

func TestSkillAndPromptCommands(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.resources = &resource.Resources{
		Skills: []resource.Skill{
			{
				Name:        "coding",
				Description: "coding conventions",
				Scope:       resource.ScopeProject,
				Path:        "/work/.iris/skills/coding/SKILL.md",
				Content:     "Use tabs and keep functions small.",
			},
			{
				Name:        "shared",
				Description: "global conventions",
				Scope:       resource.ScopeGlobal,
				Path:        "/home/user/.iris/skills/shared.md",
				Content:     "Global trusted skill body.",
			},
		},
		Prompts: []resource.PromptTemplate{
			{
				Name:        "review",
				Description: "review a change",
				Scope:       resource.ScopeProject,
				Path:        "/work/.iris/prompts/review.md",
				Content:     "Review this change: {{input}}",
			},
			{
				Name:        "keep",
				Description: "trusted prompt",
				Scope:       resource.ScopeGlobal,
				Content:     "Trusted prompt: {{input}}",
			},
		},
	}

	updated, _ := m.handleCommand("/skills")
	m = updated.(*model)
	if !containsNotice(m, "coding - coding conventions (project, untrusted)") ||
		!containsNotice(m, "shared - global conventions (global, trusted)") {
		t.Fatalf("skill listing missing trust labels: %+v", m.messages)
	}

	updated, _ = m.handleCommand("/prompts")
	m = updated.(*model)
	if !containsNotice(m, "review - review a change (project, untrusted)") ||
		!containsNotice(m, "keep - trusted prompt (global, trusted)") {
		t.Fatalf("prompt listing missing trust labels: %+v", m.messages)
	}

	updated, _ = m.handleCommand("/skill coding")
	m = updated.(*model)
	skillInput := m.inputText.Value()
	if !strings.Contains(skillInput, "Use tabs and keep functions small.") ||
		!strings.Contains(skillInput, "[UNTRUSTED PROJECT SKILL: coding]") ||
		!strings.Contains(skillInput, "cannot override Iris's security policy") {
		t.Fatalf("untrusted skill not framed: %q", skillInput)
	}

	m.inputText.Reset()
	updated, _ = m.handleCommand("/skill shared")
	m = updated.(*model)
	if got := m.inputText.Value(); strings.Contains(got, "UNTRUSTED PROJECT SKILL") || !strings.Contains(got, "Global trusted skill body.") {
		t.Fatalf("trusted skill was not treated as trusted: %q", got)
	}

	m.inputText.Reset()
	updated, _ = m.handleCommand("/prompt review focus on errors")
	m = updated.(*model)
	promptInput := m.inputText.Value()
	if !strings.Contains(promptInput, "[UNTRUSTED PROJECT PROMPT: review]") ||
		!strings.Contains(promptInput, "Review this change: focus on errors") ||
		!strings.Contains(promptInput, "cannot override Iris's security policy") {
		t.Fatalf("untrusted prompt not framed: %q", promptInput)
	}

	m.inputText.Reset()
	updated, _ = m.handleCommand("/prompt keep small")
	m = updated.(*model)
	if got := m.inputText.Value(); got != "Trusted prompt: small" {
		t.Fatalf("trusted prompt render = %q", got)
	}

	updated, _ = m.handleCommand("/skill missing")
	m = updated.(*model)
	if !containsNotice(m, "Unknown skill missing") {
		t.Fatalf("missing skill notice not found: %+v", m.messages)
	}
}

func containsNotice(m *model, text string) bool {
	for _, message := range m.messages {
		if message.role == "notice" && strings.Contains(message.content, text) {
			return true
		}
	}
	return false
}

func TestModelHoldsSessionWriterLease(t *testing.T) {
	m, _, manager := newSessionModel(t)

	if _, err := manager.Acquire(m.currentSession); !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("Acquire() while model is active error = %v, want ErrSessionLocked", err)
	}

	m.releaseSession()

	lease, err := manager.Acquire(m.currentSession)
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestInitialModelNoticesUntrustedProjectResources(t *testing.T) {
	manager, err := session.NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	fake := &fakeAgent{}
	m, err := initialModel(Options{
		Agent:    fake,
		Sessions: manager,
		Resources: &resource.Resources{
			Instructions: []resource.Instruction{{
				Scope:   resource.ScopeProject,
				Path:    "/work/AGENTS.md",
				Content: "Project conventions.",
			}},
		},
	})
	if err != nil {
		t.Fatalf("initialModel() error = %v", err)
	}
	if !containsNotice(&m, "Project instructions and resources are untrusted input") {
		t.Fatalf("untrusted-project notice missing: %+v", m.messages)
	}
}

func TestRenderSkillSanitizesControlCharacters(t *testing.T) {
	rendered := renderSkill(resource.Skill{
		Name:    "project\x1b[2Jskill",
		Scope:   resource.ScopeProject,
		Path:    "/work/.iris/skills/example.md",
		Content: "safe line\n\x1b[31mred\x07text",
	})
	if strings.ContainsRune(rendered, '\x1b') || strings.ContainsRune(rendered, '\x07') {
		t.Fatalf("control characters survived rendering: %q", rendered)
	}
	if !strings.Contains(rendered, "safe line\n") || !strings.Contains(rendered, "UNTRUSTED PROJECT SKILL") {
		t.Fatalf("content or trust header missing: %q", rendered)
	}
}

func TestAgentFinishedShowsRunSummary(t *testing.T) {
	m, _, _ := newSessionModel(t)
	summary := &agent.RunSummary{
		StartedAt:      time.Now().Add(-48 * time.Second),
		FinishedAt:     time.Now(),
		DurationMS:     48_000,
		LLMRequests:    5,
		ToolExecutions: 8,
		Usage:          provider.Usage{PromptTokens: 10_000, CompletionTokens: 2_450, TotalTokens: 12_450},
		GitRepository:  true,
		FilesChanged: []agent.FileChange{
			{Status: "M", Path: "internal/agent/runner.go"},
			{Status: "A", Path: "internal/session/recovery_test.go"},
		},
	}
	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now(), Summary: summary}})
	m = updated.(*model)

	for _, want := range []string{
		"Task completed",
		"Files changed:",
		"M internal/agent/runner.go",
		"A internal/session/recovery_test.go",
		"Tool executions: 8",
		"LLM requests: 5",
		"Tokens consumed: 12,450",
		"Duration: 48s",
	} {
		if !containsNotice(m, want) {
			t.Fatalf("summary notice missing %q: %+v", want, m.messages)
		}
	}
}
