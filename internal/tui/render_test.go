package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/session"
)

func sizedApprovalModel(t *testing.T, width, height int) *model {
	t.Helper()
	m := newApprovalModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(*model)
}

func sizedSessionModel(t *testing.T, width, height int) (*model, *fakeAgent, *session.JSONLManager) {
	t.Helper()
	m, fake, manager := newSessionModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(*model), fake, manager
}

func viewLineCount(content string) int {
	return strings.Count(content, "\n") + 1
}

func TestViewFillsTerminalHeight(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	m.messages = append(m.messages,
		message{role: "user", content: "list the files"},
		message{role: "assistant", content: "There are five files:\n\n- a.go\n- b.go"},
		message{role: "tool", toolName: "shell", toolTarget: "ls", toolStatus: "ok"},
		message{role: "summary", outcome: "Task completed"},
	)
	m.resizeLayout(true)
	if got := viewLineCount(m.View().Content); got != 24 {
		t.Fatalf("view lines = %d, want 24", got)
	}

	m.inputText.SetValue("/")
	m.resizeLayout(true)
	if got := viewLineCount(m.View().Content); got != 24 {
		t.Fatalf("view lines with completion = %d, want 24", got)
	}

	reply := make(chan agent.ApprovalDecision, 1)
	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "rm -rf ./build", Workdir: "/workspace"},
		reply:   reply,
	})
	m = updated.(*model)
	if got := viewLineCount(m.View().Content); got != 24 {
		t.Fatalf("view lines with approval = %d, want 24", got)
	}
}

func TestMultilineInputCapsAndShrinksViewport(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)

	lines := make([]string, 0, 30)
	for index := 0; index < 30; index++ {
		lines = append(lines, "line")
	}
	m.inputText.SetValue(strings.Join(lines, "\n"))
	m.resizeLayout(true)

	if height := m.inputText.Height(); height > 8 {
		t.Fatalf("input height = %d, want <= 8 for a 24-row terminal", height)
	}
	if m.viewportHeight() < MinViewportHeight {
		t.Fatalf("viewport height = %d, want >= %d", m.viewportHeight(), MinViewportHeight)
	}
	if !strings.Contains(m.inputRule(), "↑") {
		t.Fatalf("input rule does not show hidden lines: %q", m.inputRule())
	}
	if strings.Contains(m.statusLine(), "\n") {
		t.Fatal("status line must stay on one row")
	}

	m.inputText.SetValue("one\ntwo")
	m.resizeLayout(true)
	if height := m.inputText.Height(); height != 2 {
		t.Fatalf("input height after shrinking = %d, want 2", height)
	}
	if strings.Contains(m.inputRule(), "↑") {
		t.Fatalf("input rule still shows hidden lines: %q", m.inputRule())
	}
}

func TestFollowModePreservesScrollDuringStreaming(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	m.state = StateStreaming
	m.messages = append(m.messages, message{
		role:    "assistant",
		content: strings.Repeat("a long line of transcript\n", 80),
	})
	m.resizeLayout(true)

	m.viewport.ScrollUp(10)
	m.updateFollow()
	if m.viewport.AtBottom() {
		t.Fatal("test setup did not scroll away from the bottom")
	}
	if m.follow {
		t.Fatal("follow mode stayed enabled after scrolling up")
	}
	offset := m.viewport.YOffset()

	updated, _ := m.Update(agentEventMsg{event: agent.AssistantDelta{Text: "new streamed output"}})
	m = updated.(*model)

	if m.follow {
		t.Fatal("streamed output re-enabled follow mode")
	}
	if got := m.viewport.YOffset(); got != offset {
		t.Fatalf("scroll offset = %d, want %d", got, offset)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(*model)
	if !m.follow || !m.viewport.AtBottom() {
		t.Fatal("esc did not return to the tail")
	}
}

func TestToolLedgerKeepsParallelCalls(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming

	for _, event := range []agent.Event{
		agent.ToolStarted{ToolCallID: "call-1", ToolName: "shell", Args: map[string]any{"command": "ls"}},
		agent.ToolStarted{ToolCallID: "call-2", ToolName: "read", Args: map[string]any{"path": "a.go"}},
		agent.ToolCompleted{
			ToolCallID: "call-1",
			ToolName:   "shell",
			Result:     map[string]any{"output": "a.go\nb.go", "exit_code": 0},
			Duration:   300 * time.Millisecond,
		},
		agent.ToolCompleted{
			ToolCallID: "call-2",
			ToolName:   "read",
			Result:     map[string]any{"content": "package a", "lines": 10},
			Duration:   50 * time.Millisecond,
		},
	} {
		updated, _ := m.Update(agentEventMsg{event: event})
		m = updated.(*model)
	}

	var tools []message
	for _, msg := range m.messages {
		if msg.role == "tool" {
			tools = append(tools, msg)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("tool messages = %d, want 2: %+v", len(tools), tools)
	}
	if tools[0].toolName != "shell" || tools[0].toolTarget != "ls" || tools[0].toolStatus != "ok" {
		t.Fatalf("first tool = %+v", tools[0])
	}
	if tools[1].toolName != "read" || tools[1].toolTarget != "a.go" || tools[1].toolMeta != "10 lines" {
		t.Fatalf("second tool = %+v", tools[1])
	}
	if len(m.activeTools) != 0 {
		t.Fatalf("active tools after completion = %+v", m.activeTools)
	}
}

func TestToolFailuresAreSurfaced(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming

	updated, _ := m.Update(agentEventMsg{event: agent.ToolStarted{
		ToolCallID: "call-1",
		ToolName:   "shell",
		Args:       map[string]any{"command": "go test ./..."},
	}})
	m = updated.(*model)
	updated, _ = m.Update(agentEventMsg{event: agent.ToolCompleted{
		ToolCallID: "call-1",
		ToolName:   "shell",
		Result: map[string]any{
			"output":    "FAIL\tgithub.com/m4ns0ur/iris/internal/tui\nmore detail",
			"exit_code": 1,
		},
		Duration: time.Second,
	}})
	m = updated.(*model)

	last := m.messages[len(m.messages)-1]
	if last.role != "tool" || last.toolStatus != "error" || last.toolExit != 1 {
		t.Fatalf("failed tool message = %+v", last)
	}
	rendered := m.renderMessage(last)
	for _, want := range []string{"✗", "exit 1", "FAIL"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("failure render missing %q:\n%s", want, rendered)
		}
	}

	updated, _ = m.Update(agentEventMsg{event: agent.ToolStarted{
		ToolCallID: "call-2",
		ToolName:   "shell",
		Args:       map[string]any{"command": "rm -rf /"},
	}})
	m = updated.(*model)
	updated, _ = m.Update(agentEventMsg{event: agent.ToolFailed{
		ToolCallID: "call-2",
		ToolName:   "shell",
		Message:    "permission denied by policy",
		Executed:   false,
		Duration:   10 * time.Millisecond,
	}})
	m = updated.(*model)

	last = m.messages[len(m.messages)-1]
	if last.toolStatus != "denied" || !strings.Contains(last.toolDetail, "not run:") {
		t.Fatalf("denied tool message = %+v", last)
	}
	if rendered := m.renderMessage(last); !strings.Contains(rendered, "permission denied by policy") {
		t.Fatalf("denied render missing detail:\n%s", rendered)
	}
}

func TestToolOutputOnlyAppearsOnErrorOrVerbose(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	msg := message{
		role:       "tool",
		toolName:   "shell",
		toolTarget: "ls",
		toolStatus: "ok",
		toolOutput: "a\nb\nc",
		toolMeta:   "3 lines",
	}
	if rendered := ansi.Strip(m.renderMessage(msg)); strings.Contains(rendered, "    a") {
		t.Fatalf("successful tool output leaked into compact mode:\n%s", rendered)
	}

	m.showToolOutput = true
	if rendered := ansi.Strip(m.renderMessage(msg)); !strings.Contains(rendered, "    a") {
		t.Fatalf("verbose mode did not show tool output:\n%s", rendered)
	}

	m.showToolOutput = false
	msg.toolStatus = "error"
	msg.toolDetail = "boom"
	if rendered := ansi.Strip(m.renderMessage(msg)); !strings.Contains(rendered, "boom") || !strings.Contains(rendered, "    a") {
		t.Fatalf("failed tool output was not shown:\n%s", rendered)
	}
}

func TestReasoningCollapsedAndToggle(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	msg := message{role: "reasoning", content: "a detailed chain of thought"}
	rendered := m.renderMessage(msg)
	if !strings.Contains(rendered, "reasoning") || strings.Contains(rendered, "detailed chain") {
		t.Fatalf("reasoning was not collapsed:\n%s", rendered)
	}

	m.showReasoning = true
	rendered = m.renderMessage(msg)
	if !strings.Contains(rendered, "detailed chain of thought") {
		t.Fatalf("expanded reasoning is missing:\n%s", rendered)
	}
}

func TestUserTextIsNotMarkdown(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	msg := message{role: "user", content: "# heading\n- item"}
	rendered := m.renderMessage(msg)
	if !strings.Contains(rendered, "# heading") || !strings.Contains(rendered, "- item") {
		t.Fatalf("user text was interpreted as markdown:\n%s", rendered)
	}
}

func TestCompletionAndHistory(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)

	m.inputText.SetValue("/ne")
	m.resizeLayout(true)
	items := m.completionItems()
	if len(items) != 1 || items[0].name != "/new" {
		t.Fatalf("completion items = %+v, want /new", items)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(*model)
	if got := strings.TrimSpace(m.inputText.Value()); got != "/new" {
		t.Fatalf("input after tab completion = %q, want /new", got)
	}
	if len(m.completionItems()) != 0 {
		t.Fatal("completion menu stayed open after accepting")
	}

	m.recordHistory("first request")
	m.recordHistory("second request")
	m.inputText.SetValue("")
	m.resizeLayout(true)

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(*model)
	if got := m.inputText.Value(); got != "second request" {
		t.Fatalf("first history recall = %q", got)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(*model)
	if got := m.inputText.Value(); got != "first request" {
		t.Fatalf("second history recall = %q", got)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*model)
	if got := m.inputText.Value(); got != "second request" {
		t.Fatalf("history forward = %q", got)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*model)
	if got := m.inputText.Value(); got != "" {
		t.Fatalf("history draft restore = %q, want empty", got)
	}
}

func TestStatusLineFitsNarrowTerminal(t *testing.T) {
	m := sizedApprovalModel(t, 40, 24)
	m.modelName = "Qwen3.5-9B"
	m.workspace = "/Users/mansour/workspace/a-long-project-name"
	m.contextWindow = 75_000
	m.contextTokens = 60_000
	m.sessionTokens = 7_312

	for _, state := range []state{StateInput, StateStreaming} {
		m.state = state
		line := m.statusLine()
		if strings.Contains(line, "\n") {
			t.Fatalf("status line contains a newline: %q", line)
		}
		if width := lipgloss.Width(line); width > 40 {
			t.Fatalf("status line width = %d, want <= 40: %q", width, line)
		}
	}
}

func TestSessionsAndStatusCommands(t *testing.T) {
	m, fake, _ := sizedSessionModel(t, 100, 30)
	fake.history = []provider.Message{
		userMessage("fix the approval timeout"),
		{Role: "assistant", Content: "done"},
	}
	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)
	originalID := m.currentSession.ID

	updated, _ = m.handleCommand("/new")
	m = updated.(*model)
	updated, _ = m.handleCommand("/sessions")
	m = updated.(*model)
	if !containsNotice(m, "Recent sessions") || !containsNotice(m, "fix the approval timeout") {
		t.Fatalf("session list missing title: %+v", m.messages)
	}

	updated, _ = m.handleCommand("/resume 1")
	m = updated.(*model)
	if m.currentSession.ID != originalID {
		t.Fatalf("resumed session = %q, want %q", m.currentSession.ID, originalID)
	}

	m.contextWindow = 75_000
	m.contextTokens = 18_400
	m.contextTrigger = 56_700
	m.policy = "workspace-write sandbox · shell approval"
	updated, _ = m.handleCommand("/status")
	m = updated.(*model)
	for _, want := range []string{"Status:", "test-model", "/workspace", "workspace-write", "18.4k", "75k", "56.7k prompt tokens"} {
		if !containsNotice(m, want) {
			t.Fatalf("/status missing %q: %+v", want, m.messages)
		}
	}
}

func TestResumeRebuildsToolLedger(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	loaded := &session.Session{
		Metadata: session.Metadata{ID: "s-12345678", Model: "test-model", Workspace: "/work"},
		Entries: []session.Entry{
			{Kind: session.EntryUser, Content: "read the file"},
			{
				Kind:             session.EntryAssistant,
				ReasoningContent: "checking the path",
				ToolCalls: []provider.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: provider.FunctionCall{
						Name:      "shell",
						Arguments: `{"command":"ls -la"}`,
					},
				}},
			},
			{Kind: session.EntryTool, ToolCallID: "call-1", ToolName: "shell", Content: "a\nb"},
		},
	}
	m.loadTranscript(loaded)

	var tool *message
	var reasoning *message
	for index := range m.messages {
		switch m.messages[index].role {
		case "tool":
			tool = &m.messages[index]
		case "reasoning":
			reasoning = &m.messages[index]
		}
	}
	if tool == nil {
		t.Fatalf("resumed transcript has no tool entry: %+v", m.messages)
	}
	if tool.toolName != "shell" || tool.toolTarget != "ls -la" || tool.toolMeta != "2 lines" {
		t.Fatalf("resumed tool = %+v", tool)
	}
	if reasoning == nil || reasoning.content != "checking the path" {
		t.Fatalf("resumed reasoning = %+v", reasoning)
	}
}

func TestQuitCommandReturnsTeaQuit(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	updated, cmd := m.handleCommand("/quit")
	m = updated.(*model)
	if cmd == nil {
		t.Fatal("/quit returned no command")
	}
	select {
	case <-m.done():
	default:
		t.Fatal("/quit did not close the lifecycle")
	}
}

func TestApprovalPanelCommandPreservesTail(t *testing.T) {
	m := sizedApprovalModel(t, 60, 24)
	reply := make(chan agent.ApprovalDecision, 1)
	command := "echo start && " + strings.Repeat("echo filler && ", 40) + "rm -rf ./build"
	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: command, Workdir: "/workspace"},
		reply:   reply,
	})
	m = updated.(*model)

	panel := m.approvalPanel()
	if !strings.Contains(panel, "echo start") || !strings.Contains(panel, "rm -rf ./build") {
		t.Fatalf("approval panel lost a command end:\n%s", panel)
	}
	if !strings.Contains(panel, "lines omitted") {
		t.Fatalf("approval panel did not mark omitted lines:\n%s", panel)
	}
}

func TestAssistantTextPrecedesTool(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming

	for _, event := range []agent.Event{
		agent.AssistantDelta{Text: "Let me check that."},
		agent.ToolStarted{ToolCallID: "call-1", ToolName: "shell", Args: map[string]any{"command": "ls"}},
		agent.ToolCompleted{ToolCallID: "call-1", ToolName: "shell", Result: map[string]any{"output": "a", "exit_code": 0}},
	} {
		updated, _ := m.Update(agentEventMsg{event: event})
		m = updated.(*model)
	}

	var order []string
	for _, msg := range m.messages {
		switch msg.role {
		case "assistant":
			order = append(order, "assistant")
		case "tool":
			order = append(order, "tool")
		}
	}
	if len(order) != 2 || order[0] != "assistant" || order[1] != "tool" {
		t.Fatalf("message order = %v, want assistant before tool", order)
	}

	content := ansi.Strip(m.buildContent())
	assistantAt := strings.Index(content, "Let me check that.")
	toolAt := strings.Index(content, "✓ shell")
	if assistantAt < 0 || toolAt < 0 || assistantAt > toolAt {
		t.Fatalf("rendered order is wrong:\n%s", content)
	}
}

func TestViewFitsSmallTerminal(t *testing.T) {
	m := sizedApprovalModel(t, 80, 10)
	m.inputText.SetValue("/")
	m.resizeLayout(true)
	if got := viewLineCount(m.View().Content); got != 10 {
		t.Fatalf("view lines with completion = %d, want 10:\n%s", got, ansi.Strip(m.View().Content))
	}

	m.inputText.Reset()
	reply := make(chan agent.ApprovalDecision, 1)
	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "rm -rf ./build", Workdir: "/workspace"},
		reply:   reply,
	})
	m = updated.(*model)
	if got := viewLineCount(m.View().Content); got != 10 {
		t.Fatalf("view lines with approval = %d, want 10:\n%s", got, ansi.Strip(m.View().Content))
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m = updated.(*model)
	if got := viewLineCount(m.View().Content); got != 8 {
		t.Fatalf("view lines at 80x8 = %d, want 8:\n%s", got, ansi.Strip(m.View().Content))
	}
}

func TestApprovalPlaceholderAndRestore(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	reply := make(chan agent.ApprovalDecision, 1)
	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)
	if m.inputText.Placeholder != "Approval pending…" {
		t.Fatalf("placeholder = %q, want approval pending", m.inputText.Placeholder)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	m = updated.(*model)
	if m.inputText.Placeholder != "Ask Iris…" {
		t.Fatalf("placeholder after denial = %q, want Ask Iris…", m.inputText.Placeholder)
	}
}

func TestEmptySessionShowsHintOnlyOnce(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	if !containsNotice(m, "Type a request") {
		t.Fatalf("empty-session hint missing: %+v", m.messages)
	}

	m.currentSession = &session.Session{}
	m.messages = nil
	m.appendSessionHeader()
	if !containsNotice(m, "Type a request") {
		t.Fatal("empty session did not show the hint")
	}

	m.currentSession.Entries = []session.Entry{{Kind: session.EntryUser, Content: "hello"}}
	m.messages = nil
	m.appendSessionHeader()
	if containsNotice(m, "Type a request") {
		t.Fatal("non-empty session showed the empty hint")
	}
}

func TestConsecutiveToolLinesAreTight(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming

	for _, event := range []agent.Event{
		agent.ToolStarted{ToolCallID: "call-1", ToolName: "shell", Args: map[string]any{"command": "ls"}},
		agent.ToolCompleted{ToolCallID: "call-1", ToolName: "shell", Result: map[string]any{"output": "a", "exit_code": 0}},
		agent.ToolStarted{ToolCallID: "call-2", ToolName: "read", Args: map[string]any{"path": "a.go"}},
	} {
		updated, _ := m.Update(agentEventMsg{event: event})
		m = updated.(*model)
	}

	content := ansi.Strip(m.buildContent())
	first := strings.Index(content, "✓ shell")
	second := strings.Index(content, "⋯ read")
	if first < 0 || second < 0 || second < first {
		t.Fatalf("tool lines are missing or out of order:\n%s", content)
	}
	between := content[first:second]
	if strings.Count(between, "\n") != 1 {
		t.Fatalf("completed and running tool lines are not tight:\n%s", content)
	}
}

func TestDetailTogglesDoNotEditInput(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	m.inputText.SetValue("keep this")

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(*model)
	if !m.showToolOutput {
		t.Fatal("ctrl+e did not enable tool output")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(*model)
	if !m.showReasoning {
		t.Fatal("ctrl+r did not enable reasoning")
	}
	if got := m.inputText.Value(); got != "keep this" {
		t.Fatalf("input changed after toggles: %q", got)
	}
}

func TestErrorRenderWrapsWithHangingIndent(t *testing.T) {
	m := sizedApprovalModel(t, 60, 24)
	msg := message{role: "error", content: strings.Repeat("the operation failed because of a reason ", 4)}
	rendered := ansi.Strip(m.renderMessage(msg))
	if !strings.HasPrefix(rendered, "  ✗ ") {
		t.Fatalf("error prefix missing:\n%s", rendered)
	}
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if width := lipgloss.Width(line); width > 60 {
			t.Fatalf("error line width = %d, want <= 60: %q", width, line)
		}
	}
	if strings.Count(rendered, "\n") < 2 {
		t.Fatalf("long error was not wrapped:\n%s", rendered)
	}
}

func TestContextLabel(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.contextWindow = 75_000
	m.contextTokens = 18_400
	if got := m.contextLabel(); got != "ctx 18.4k/75k" {
		t.Fatalf("context label = %q", got)
	}
	m.contextTokens = 0
	if got := m.contextLabel(); got != "ctx —/75k" {
		t.Fatalf("unknown context label = %q", got)
	}
	m.contextWindow = 0
	m.contextTokens = 18_400
	if got := m.contextLabel(); got != "ctx 18.4k" {
		t.Fatalf("windowless context label = %q", got)
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		0:         "0",
		999:       "999",
		1000:      "1k",
		12450:     "12.4k",
		75000:     "75k",
		123456:    "123k",
		1_200_000: "1.2M",
	}
	for input, want := range cases {
		if got := formatTokens(input); got != want {
			t.Fatalf("formatTokens(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestCanceledSummaryReplacesNotice(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	summary := &agent.RunSummary{LLMRequests: 1, DurationMS: 1500}
	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{
		At:       time.Now(),
		Canceled: true,
		Summary:  summary,
	}})
	m = updated.(*model)

	if containsNotice(m, "Canceled.") {
		t.Fatalf("redundant canceled notice: %+v", m.messages)
	}
	var summaryMessage *message
	for index := range m.messages {
		if m.messages[index].role == "summary" {
			summaryMessage = &m.messages[index]
			break
		}
	}
	if summaryMessage == nil || summaryMessage.outcome != "Run canceled" {
		t.Fatalf("canceled summary missing: %+v", summaryMessage)
	}
	if rendered := ansi.Strip(m.renderMessage(*summaryMessage)); !strings.Contains(rendered, "canceled") {
		t.Fatalf("canceled summary render = %q", rendered)
	}
}

func TestQueueLinesShowPendingText(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	m.state = StateStreaming
	m.pendingSteering = []string{"use the existing helper", "and add a test"}
	m.pendingFollowUps = []string{"then run the suite"}
	m.resizeLayout(true)

	lines := m.queueLines()
	if len(lines) != 2 {
		t.Fatalf("queue lines = %d, want 2: %+v", len(lines), lines)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"steer", "use the existing helper", "+1", "follow-up", "then run the suite"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("queue lines missing %q:\n%s", want, plain)
		}
	}
	if got := viewLineCount(m.View().Content); got != 24 {
		t.Fatalf("view lines with queue = %d, want 24", got)
	}

	m.pendingSteering = nil
	m.pendingFollowUps = nil
	m.resizeLayout(true)
	if len(m.queueLines()) != 0 {
		t.Fatalf("queue lines after clear = %+v", m.queueLines())
	}
}

func TestResumeParsesStructuredToolResults(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	loaded := &session.Session{
		Metadata: session.Metadata{ID: "s-12345678", Model: "test-model"},
		Entries: []session.Entry{
			{Kind: session.EntryUser, Content: "do the work"},
			{
				Kind: session.EntryAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Type: "function", Function: provider.FunctionCall{Name: "shell", Arguments: `{"command":"ls"}`}},
					{ID: "c2", Type: "function", Function: provider.FunctionCall{Name: "edit", Arguments: `{"path":"a.go","edits":[{"find":"old\nline","replace":"new"}]}`}},
				},
			},
			{Kind: session.EntryTool, ToolCallID: "c1", ToolName: "shell", Content: `{"output":"a\nb","exit_code":1}`},
			{Kind: session.EntryTool, ToolCallID: "c2", ToolName: "edit", Content: `{"success":true,"edits":1,"changed":true}`},
		},
	}
	m.loadTranscript(loaded)

	var tools []message
	for _, msg := range m.messages {
		if msg.role == "tool" {
			tools = append(tools, msg)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("resumed tools = %d, want 2: %+v", len(tools), tools)
	}
	if tools[0].toolStatus != "error" || tools[0].toolExit != 1 || tools[0].toolMeta != "2 lines" {
		t.Fatalf("resumed shell tool = %+v", tools[0])
	}
	if tools[1].toolMeta != "+1 −2" || tools[1].toolStatus != "ok" {
		t.Fatalf("resumed edit tool = %+v", tools[1])
	}
}

func TestResumeParsesToolFailure(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	loaded := &session.Session{
		Metadata: session.Metadata{ID: "s-12345678"},
		Entries: []session.Entry{
			{Kind: session.EntryUser, Content: "do the work"},
			{Kind: session.EntryAssistant, ToolCalls: []provider.ToolCall{{
				ID: "c1", Type: "function", Function: provider.FunctionCall{Name: "shell", Arguments: `{"command":"rm -rf /"}`},
			}}},
			{Kind: session.EntryTool, ToolCallID: "c1", ToolName: "shell", Content: `{"ok":false,"error":{"code":"tool_policy_denied","message":"denied by policy","executed":false,"retryable":false}}`},
		},
	}
	m.loadTranscript(loaded)

	var tool *message
	for index := range m.messages {
		if m.messages[index].role == "tool" {
			tool = &m.messages[index]
		}
	}
	if tool == nil {
		t.Fatal("resumed failure has no tool entry")
	}
	if tool.toolStatus != "denied" || !strings.Contains(tool.toolDetail, "not run: denied by policy") {
		t.Fatalf("resumed failure = %+v", tool)
	}
}

func TestEditLineDelta(t *testing.T) {
	added, removed, ok := toolEditDelta(`{"path":"a.go","edits":[{"find":"a\nb","replace":"c"},{"find":"d","replace":"e\nf"}]}`)
	if !ok || added != 3 || removed != 3 {
		t.Fatalf("edit delta = +%d -%d ok=%v, want +3 -3", added, removed, ok)
	}
	if _, _, ok := toolEditDelta(`{}`); ok {
		t.Fatal("empty edit payload reported a delta")
	}
	if lines, ok := toolWriteLines(`{"content":"one\ntwo\n"}`); !ok || lines != 2 {
		t.Fatalf("write lines = %d ok=%v, want 2", lines, ok)
	}
}

func TestHelpOutputHasNoRawANSI(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	updated, _ := m.handleCommand("/help")
	m = updated.(*model)

	last := m.messages[len(m.messages)-1]
	if strings.ContainsRune(last.content, '\x1b') {
		t.Fatalf("help content contains an escape character: %q", last.content)
	}
	rendered := ansi.Strip(m.renderMessage(last))
	for _, fragment := range []string{"[38;", "[0m", "[m"} {
		if strings.Contains(rendered, fragment) {
			t.Fatalf("help output leaked an ANSI fragment %q:\n%s", fragment, rendered)
		}
	}
}

func TestLiveAssistantRendersMarkdown(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	m.fullResponse.WriteString("## Heading\n\n**bold** text with `code`")
	m.resizeLayout(true)

	content := ansi.Strip(m.buildContent())
	if strings.Contains(content, "**") {
		t.Fatalf("live markdown emphasis was not rendered:\n%s", content)
	}
	if !strings.Contains(content, "Heading") || !strings.Contains(content, "bold") {
		t.Fatalf("live markdown content missing:\n%s", content)
	}

	m.fullResponse.Reset()
	m.fullResponse.WriteString("```go\nfunc main() {\n")
	m.resizeLayout(true)
	content = ansi.Strip(m.buildContent())
	if !strings.Contains(content, "```") {
		t.Fatalf("open code fence was not kept in plain fallback:\n%s", content)
	}
}

func TestRespondingPhase(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	m.fullResponse.WriteString("hello")

	if got := m.currentPhase(); got != phaseResponding {
		t.Fatalf("phase = %v, want responding", got)
	}
	if got := m.statusLeft(); strings.Contains(got, "responding") {
		t.Fatalf("status left still contains the phase word: %q", got)
	}
	if got := m.statusLeft(); !strings.Contains(got, "ctrl+c abort") {
		t.Fatalf("status left = %q, want abort hint", got)
	}
	if got := m.spinnerFrame(phaseResponding); got != respondingSpinner.Frames[0] {
		t.Fatalf("responding frame = %q, want %q", got, respondingSpinner.Frames[0])
	}

	m.activeTools["call-1"] = &activeTool{id: "call-1", name: "shell"}
	if got := m.currentPhase(); got != phaseTooling {
		t.Fatalf("phase with active tool = %v, want tooling", got)
	}
	if got := m.spinnerFrame(phaseTooling); got != toolingSpinner.Frames[0] {
		t.Fatalf("tooling frame = %q, want %q", got, toolingSpinner.Frames[0])
	}
}

// Every frame of one phase must share a cell width, because the status line
// sizes the gap from the current frame: mixed widths would shift the footer on
// every tick. Model-work families are one cell; compaction's trigrams are two
// cells wide and widen the line for the whole phase instead of jittering.
func TestPhaseSpinnersHaveStableFrameWidth(t *testing.T) {
	for p := phaseThinking; p <= phaseWaitingSlow; p++ {
		sp := phaseSpinner(p)
		if len(sp.Frames) == 0 || sp.FPS <= 0 {
			t.Fatalf("phase %v has an empty spinner", p)
		}
		width := lipgloss.Width(sp.Frames[0])
		if width < 1 {
			t.Fatalf("phase %v frame %q has no width", p, sp.Frames[0])
		}
		for _, frame := range sp.Frames {
			if got := lipgloss.Width(frame); got != width {
				t.Fatalf("phase %v frames differ in width: %q is %d, want %d", p, frame, got, width)
			}
		}
	}
}

func TestPhaseMapping(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	if got := m.currentPhase(); got != phaseIdle {
		t.Fatalf("idle phase = %v", got)
	}

	m.state = StateStreaming
	if got := m.currentPhase(); got != phaseThinking {
		t.Fatalf("thinking phase = %v", got)
	}
	m.fullReason.WriteString("reasoning")
	if got := m.currentPhase(); got != phaseReasoning {
		t.Fatalf("reasoning phase = %v", got)
	}
	m.fullResponse.WriteString("answer")
	if got := m.currentPhase(); got != phaseResponding {
		t.Fatalf("responding phase = %v", got)
	}
	m.activeTools["call-1"] = &activeTool{id: "call-1", name: "shell"}
	if got := m.currentPhase(); got != phaseTooling {
		t.Fatalf("tooling phase = %v", got)
	}
	m.compacting = true
	if got := m.currentPhase(); got != phaseCompacting {
		t.Fatalf("compacting phase = %v", got)
	}
	m.canceling = true
	if got := m.currentPhase(); got != phaseCanceling {
		t.Fatalf("canceling phase = %v", got)
	}
}

func TestApprovalWaitNotCountedInToolDuration(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	m.trackToolStarted(agent.ToolStarted{
		ToolCallID: "call-1",
		ToolName:   "shell",
		Args:       map[string]any{"command": "echo hi"},
	})
	m.approvalWaits["echo hi"] = 2 * time.Second

	m.finishToolCompleted(agent.ToolCompleted{
		ToolCallID: "call-1",
		ToolName:   "shell",
		Result:     map[string]any{"output": "hi", "exit_code": 0},
		Duration:   3 * time.Second,
	})

	last := m.messages[len(m.messages)-1]
	if last.toolDuration != time.Second {
		t.Fatalf("tool duration = %s, want 1s after subtracting a 2s approval wait", last.toolDuration)
	}
	if _, ok := m.approvalWaits["echo hi"]; ok {
		t.Fatal("approval wait was not consumed")
	}
}

func TestApprovalWaitIsRecorded(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	reply := make(chan agent.ApprovalDecision, 1)
	m.pendingApproval = &approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	}
	m.approvalWaitStart = time.Now().Add(-2 * time.Second)

	m.resolveApproval(agent.ApprovalDecision{Allow: true, Reason: "test"})

	if wait := m.approvalWaits["echo hi"]; wait < time.Second {
		t.Fatalf("recorded approval wait = %s, want ~2s", wait)
	}
	if !m.approvalWaitStart.IsZero() {
		t.Fatal("approval wait start was not cleared")
	}
}

func TestPlaceholderRestoredAfterRun(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	m.state = StateStreaming
	m.syncPlaceholder()
	if m.inputText.Placeholder != "Steer Iris…" {
		t.Fatalf("streaming placeholder = %q", m.inputText.Placeholder)
	}

	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)
	if m.inputText.Placeholder != "Ask Iris…" {
		t.Fatalf("placeholder after finish = %q, want Ask Iris…", m.inputText.Placeholder)
	}
}

func TestCompletionMenuIsFramedByRules(t *testing.T) {
	m := sizedApprovalModel(t, 80, 24)
	m.inputText.SetValue("/")
	m.resizeLayout(true)

	content := m.View().Content
	if strings.Count(content, "──") < 2 {
		t.Fatalf("completion menu is not framed by two rules")
	}
}

func TestSessionTotalInStatus(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.modelName = "qwen3.6-35B-Q1"
	m.workspace = "/private/tmp/foo"
	m.contextWindow = 65_500
	m.contextTokens = 1_600
	m.sessionTokens = 7_312

	right := ansi.Strip(m.statusRight())
	for _, want := range []string{"qwen3.6-35B-Q1", "foo", "ctx 1.6k/65.5k", "total 7,312"} {
		if !strings.Contains(right, want) {
			t.Fatalf("status right missing %q: %q", want, right)
		}
	}
}

func TestSessionTokensAccumulate(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.sessionTokens = 100

	updated, _ := m.Update(agentEventMsg{event: agent.Usage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}})
	m = updated.(*model)
	if m.sessionTokens != 115 {
		t.Fatalf("session tokens = %d, want 115", m.sessionTokens)
	}

	updated, _ = m.Update(agentEventMsg{event: agent.ContextCompactionFinished{
		SummaryPromptTokens:     20,
		SummaryCompletionTokens: 10,
	}})
	m = updated.(*model)
	if m.sessionTokens != 145 {
		t.Fatalf("session tokens after compaction = %d, want 145", m.sessionTokens)
	}
}

func TestFormatPreciseTokens(t *testing.T) {
	if got := formatPreciseTokens(1412); got != "1,412" {
		t.Fatalf("formatPreciseTokens(1412) = %q", got)
	}
	if got := formatPreciseTokens(12450); got != "12.4k" {
		t.Fatalf("formatPreciseTokens(12450) = %q", got)
	}
}

func TestRunSummaryUsesExactSmallTokens(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	summary := &agent.RunSummary{
		LLMRequests: 1,
		Usage:       provider.Usage{PromptTokens: 1300, CompletionTokens: 112, TotalTokens: 1412},
	}
	rendered := ansi.Strip(m.renderMessage(message{
		role:       "summary",
		outcome:    "Task completed",
		runSummary: summary,
	}))
	if !strings.Contains(rendered, "1,412 tok") {
		t.Fatalf("small run tokens were rounded: %q", rendered)
	}
}

// The compacting trigrams are two cells wide, one more than every other
// family; the footer must still fit the terminal exactly.
func TestStatusLineFitsWithTheCompactingSpinner(t *testing.T) {
	m, _, _ := sizedSessionModel(t, 40, 30)
	m.state = StateStreaming
	m.compacting = true
	m.runStartedAt = time.Now()

	line := m.statusLine()
	if width := lipgloss.Width(line); width > 40 {
		t.Fatalf("status line width = %d, want <= 40: %q", width, line)
	}
	shown := false
	for _, frame := range compactingSpinner.Frames {
		if strings.Contains(line, frame) {
			shown = true
			break
		}
	}
	if !shown {
		t.Fatalf("status line = %q, want a compacting frame", line)
	}
}

// displayPath must shorten only paths under the home directory this machine
// reports, so tests and CI never depend on the developer's home.
func TestDisplayPathShortensOnlyUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if got, want := displayPath(home), "~"; got != want {
		t.Fatalf("displayPath(home) = %q, want %q", got, want)
	}
	inside := filepath.Join(home, "workspace", "iris")
	if got, want := displayPath(inside), filepath.Join("~", "workspace", "iris"); got != want {
		t.Fatalf("displayPath(inside) = %q, want %q", got, want)
	}
	outside := filepath.Join(filepath.Dir(home), "elsewhere")
	if got := displayPath(outside); got != outside {
		t.Fatalf("displayPath(outside) = %q, want %q", got, outside)
	}
	if got := displayPath(""); got != "" {
		t.Fatalf("displayPath(empty) = %q, want empty", got)
	}
}

func TestStatusHintAdvertisesShiftEnter(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	if got := m.statusLeft(); !strings.Contains(got, "shift+enter newline") {
		t.Fatalf("idle status hint = %q, want shift+enter newline", got)
	}

	m.inputText.SetValue("first line\nsecond line")
	if got := m.statusLeft(); !strings.Contains(got, "shift+enter newline") {
		t.Fatalf("multiline status hint = %q, want shift+enter newline", got)
	}
}

// firstStatusPart returns the first " · "-separated segment of a status hint.
func firstStatusPart(left string) string {
	if index := strings.Index(left, " · "); index >= 0 {
		return left[:index]
	}
	return left
}

func TestStatusNamesAWaitForTheServer(t *testing.T) {
	m, _, _ := sizedSessionModel(t, 120, 30)
	m.state = StateStreaming
	m.runStartedAt = time.Now()

	updated, _ := m.Update(agentEventMsg{event: agent.RequestStarted{
		At:      time.Now(),
		Turn:    1,
		Attempt: 1,
	}})
	m = updated.(*model)
	if !m.awaitingResponse {
		t.Fatal("awaitingResponse = false after request.started")
	}
	if phase := m.currentPhase(); phase != phaseWaiting {
		t.Fatalf("phase = %d after request.started, want phaseWaiting", phase)
	}

	// A wait of a few seconds names itself in the rendered status line, and the
	// wait timer replaces the run timer instead of repeating the same seconds.
	m.runStartedAt = time.Now().Add(-3 * time.Second)
	m.responseWaitSince = time.Now().Add(-3 * time.Second)
	left := m.statusLeft()
	if !strings.HasPrefix(left, "waiting for server") {
		t.Fatalf("status left = %q, want the wait timer first", left)
	}
	if strings.Count(left, "waiting for server") != 1 {
		t.Fatalf("status left = %q, want one wait label", left)
	}
	if line := m.statusLine(); !strings.Contains(line, "waiting for server") {
		t.Fatalf("status line = %q, want a waiting hint", line)
	}
	frame := m.spinnerFrame(m.currentPhase())
	known := false
	for _, candidate := range waitingSpinner.Frames {
		if frame == candidate {
			known = true
			break
		}
	}
	if !known {
		t.Fatalf("spinner frame = %q, want one of the MiniDot frames %v", frame, waitingSpinner.Frames)
	}

	// Below the hint threshold the run timer is all there is.
	m.awaitingResponse = false
	m.runStartedAt = time.Now().Add(-3 * time.Second)
	if left := m.statusLeft(); strings.Contains(left, "waiting for server") || !strings.HasSuffix(firstStatusPart(left), "s") {
		t.Fatalf("status left = %q, want the run timer while not waiting", left)
	}
	m.awaitingResponse = true

	// A long wait suggests on-demand model loading.
	m.responseWaitSince = time.Now().Add(-30 * time.Second)
	if phase := m.currentPhase(); phase != phaseWaitingSlow {
		t.Fatalf("phase = %d after a long wait, want phaseWaitingSlow", phase)
	}
	if left := m.statusLeft(); !strings.HasPrefix(left, "waiting for server 30s · model may be loading") {
		t.Fatalf("status left = %q, want a single model-loading hint", left)
	}

	updated, _ = m.Update(agentEventMsg{event: agent.ResponseStarted{
		At:      time.Now(),
		Turn:    1,
		Attempt: 1,
		Waited:  30 * time.Second,
	}})
	m = updated.(*model)
	if m.awaitingResponse {
		t.Fatal("awaitingResponse = true after response.started")
	}
	if phase := m.currentPhase(); phase != phaseThinking {
		t.Fatalf("phase = %d after response.started, want phaseThinking", phase)
	}
	if left := m.statusLeft(); strings.Contains(left, "waiting for server") {
		t.Fatalf("status left = %q, still waiting after the server answered", left)
	}
}

func TestSettledRunClearsAWaitForTheServer(t *testing.T) {
	m, _, _ := sizedSessionModel(t, 120, 30)
	m.state = StateStreaming
	m.runStartedAt = time.Now()

	updated, _ := m.Update(agentEventMsg{event: agent.RequestStarted{At: time.Now(), Turn: 0, Attempt: 1}})
	m = updated.(*model)
	if !m.awaitingResponse {
		t.Fatal("awaitingResponse = false after request.started")
	}

	updated, _ = m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now(), Err: "boom"}})
	m = updated.(*model)
	if m.awaitingResponse || !m.responseWaitSince.IsZero() {
		t.Fatalf("wait survived the settled run: awaiting=%t since=%v", m.awaitingResponse, m.responseWaitSince)
	}
	if wait := m.serverWaitLabel(); wait != "" {
		t.Fatalf("server wait label = %q after the run settled", wait)
	}
}
