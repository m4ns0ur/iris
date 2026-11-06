package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/agent"
)

func TestTUIApproverRoundTrip(t *testing.T) {
	approver := newTUIApprover()

	type outcome struct {
		decision agent.ApprovalDecision
		err      error
	}
	done := make(chan outcome, 1)

	go func() {
		decision, err := approver.Approve(context.Background(), agent.ApprovalRequest{
			Kind:    "shell",
			Command: "echo hi",
		})
		done <- outcome{decision: decision, err: err}
	}()

	select {
	case request := <-approver.requests:
		if request.request.Command != "echo hi" {
			t.Fatalf("request command = %q, want %q", request.request.Command, "echo hi")
		}
		request.reply <- agent.ApprovalDecision{Allow: true, Reason: "ok"}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Approve() error = %v", result.err)
		}
		if !result.decision.Allow {
			t.Fatal("Allow = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval decision")
	}
}

func TestTUIApproverRespectsCancellation(t *testing.T) {
	approver := newTUIApprover()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := approver.Approve(ctx, agent.ApprovalRequest{Command: "echo hi"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Approve() error = %v, want context.Canceled", err)
	}
}

func TestSummarizeCommandCollapsesWhitespace(t *testing.T) {
	got := summarizeCommand("echo   hi\nls", 100)
	if got != "echo hi ls" {
		t.Fatalf("summarizeCommand() = %q, want %q", got, "echo hi ls")
	}

	got = summarizeCommand("abcdefgh", 5)
	if got != "abcd…" {
		t.Fatalf("summarizeCommand() = %q, want %q", got, "abcd…")
	}

	got = summarizeCommand("echo ok; "+strings.Repeat("x", 100)+"; rm -rf /", 40)
	if !strings.HasPrefix(got, "echo ok;") || !strings.HasSuffix(got, "rm -rf /") {
		t.Fatalf("summarizeCommand() = %q, want both ends preserved", got)
	}
}

func newApprovalModel(t *testing.T) *model {
	t.Helper()

	m := initialModel(nil)
	// Tests drive events directly; provide an approval channel so re-arm
	// commands are observable.
	m.approvals = make(chan approvalRequestMsg)
	return &m
}

func TestModelApprovalAllow(t *testing.T) {
	m := newApprovalModel(t)
	reply := make(chan agent.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)

	if m.pendingApproval == nil {
		t.Fatal("pendingApproval = nil, want request")
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	m = updated.(*model)

	if m.pendingApproval != nil {
		t.Fatal("pendingApproval was not cleared after decision")
	}
	if cmd == nil {
		t.Fatal("expected a command that re-arms the approval listener")
	}

	select {
	case decision := <-reply:
		if !decision.Allow {
			t.Fatalf("decision = %+v, want allow", decision)
		}
	default:
		t.Fatal("no decision was sent to the approver")
	}
}

func TestModelApprovalDeny(t *testing.T) {
	m := newApprovalModel(t)
	reply := make(chan agent.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)

	updated, _ = m.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	m = updated.(*model)

	if m.pendingApproval != nil {
		t.Fatal("pendingApproval was not cleared after decision")
	}

	select {
	case decision := <-reply:
		if decision.Allow {
			t.Fatalf("decision = %+v, want deny", decision)
		}
	default:
		t.Fatal("no decision was sent to the approver")
	}
}

func TestModelApprovalTimeoutClearsPrompt(t *testing.T) {
	m := newApprovalModel(t)
	reply := make(chan agent.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)

	updated, cmd := m.Update(approvalTimeoutMsg{reply: reply})
	m = updated.(*model)

	if m.pendingApproval != nil {
		t.Fatal("pendingApproval was not cleared after timeout")
	}
	if cmd == nil {
		t.Fatal("expected a command that re-arms the approval listener")
	}
}

func TestModelDoneWhilePendingRearmsApproval(t *testing.T) {
	m := newApprovalModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)
	m.state = StateStreaming

	reply := make(chan agent.ApprovalDecision, 1)
	updated, _ = m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)

	updated, cmd := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)

	if m.pendingApproval != nil {
		t.Fatal("pendingApproval was not cleared when the agent finished")
	}
	if cmd == nil {
		t.Fatal("expected the approval listener to be re-armed")
	}

	select {
	case decision := <-reply:
		if decision.Allow {
			t.Fatalf("decision = %+v, want deny", decision)
		}
	default:
		t.Fatal("no deny decision was sent when the agent finished")
	}
}

func TestModelCanceledMessagePreservesPartialOutput(t *testing.T) {
	m := newApprovalModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)
	m.state = StateStreaming
	m.canceling = true
	m.fullResponse.WriteString("partial answer")

	updated, _ = m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now(), Canceled: true}})
	m = updated.(*model)

	if m.state != StateInput {
		t.Fatalf("state = %v, want StateInput", m.state)
	}
	if m.canceling {
		t.Fatal("canceling = true, want false")
	}
	if m.fullResponse.Len() != 0 {
		t.Fatalf("fullResponse was not reset: %q", m.fullResponse.String())
	}

	foundPartial := false
	foundNotice := false
	for _, message := range m.messages {
		if message.role == "assistant" && message.content == "partial answer" {
			foundPartial = true
		}
		if message.role == "notice" && message.content == "Canceled." {
			foundNotice = true
		}
	}
	if !foundPartial {
		t.Fatal("partial assistant output was not preserved")
	}
	if !foundNotice {
		t.Fatal("canceled notice was not appended")
	}
}

func TestModelCanceledMessageResolvesPendingApproval(t *testing.T) {
	m := newApprovalModel(t)
	reply := make(chan agent.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: agent.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)
	m.state = StateStreaming

	updated, cmd := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now(), Canceled: true}})
	m = updated.(*model)

	if m.pendingApproval != nil {
		t.Fatal("pendingApproval was not cleared")
	}
	if cmd == nil {
		t.Fatal("expected the approval listener to be re-armed")
	}

	select {
	case decision := <-reply:
		if decision.Allow {
			t.Fatalf("decision = %+v, want deny", decision)
		}
	default:
		t.Fatal("no deny decision was sent to the approver")
	}
}

func TestQuitClosesLifecycleOnce(t *testing.T) {
	m := newApprovalModel(t)

	_ = m.quit()
	_ = m.quit()

	select {
	case <-m.done():
	default:
		t.Fatal("lifecycle done channel was not closed")
	}
}

func TestModelFatalErrorPreservesPartialOutput(t *testing.T) {
	m := newApprovalModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)
	m.state = StateStreaming
	m.canceling = true
	m.fullResponse.WriteString("partial answer")

	updated, _ = m.Update(agentEventMsg{event: agent.AgentError{At: time.Now(), Message: "boom"}})
	m = updated.(*model)
	updated, _ = m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now(), Err: "boom"}})
	m = updated.(*model)

	if m.state != StateInput {
		t.Fatalf("state = %v, want StateInput", m.state)
	}
	if m.canceling {
		t.Fatal("canceling = true, want false")
	}

	foundPartial := false
	foundError := false
	for _, message := range m.messages {
		if message.role == "assistant" && message.content == "partial answer" {
			foundPartial = true
		}
		if message.role == "error" && strings.Contains(message.content, "boom") {
			foundError = true
		}
	}
	if !foundPartial {
		t.Fatal("partial assistant output was not preserved")
	}
	if !foundError {
		t.Fatal("fatal error message was not appended")
	}
}

func TestModelConsumesTypedAgentEvents(t *testing.T) {
	m := newApprovalModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)
	m.state = StateStreaming

	events := []agent.Event{
		agent.AgentStarted{At: time.Now(), Input: "hi"},
		agent.TurnStarted{At: time.Now(), Turn: 0},
		agent.ReasoningDelta{At: time.Now(), Text: "thinking"},
		agent.AssistantDelta{At: time.Now(), Text: "hello "},
		agent.ToolProgress{At: time.Now(), ToolName: "shell", ArgumentsDelta: `{"command":"ls"}`},
		agent.AgentFinished{At: time.Now()},
	}
	for _, event := range events {
		updated, _ = m.Update(agentEventMsg{event: event})
		m = updated.(*model)
	}

	if m.state != StateInput {
		t.Fatalf("state = %v, want StateInput", m.state)
	}

	var hasReasoning, hasAssistant, hasTool bool
	for _, message := range m.messages {
		switch {
		case message.role == "reasoning" && message.content == "thinking":
			hasReasoning = true
		case message.role == "assistant" && message.content == "hello ":
			hasAssistant = true
		case message.role == "tool" && message.toolName == "shell" && message.previewCommand == "ls":
			hasTool = true
		}
	}

	if !hasReasoning || !hasAssistant || !hasTool {
		t.Fatalf("messages = %+v, want reasoning, assistant, and tool preview", m.messages)
	}
}
