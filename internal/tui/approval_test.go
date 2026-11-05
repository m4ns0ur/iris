package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/internal/policy"
)

func TestTUIApproverRoundTrip(t *testing.T) {
	approver := newTUIApprover()

	type outcome struct {
		decision policy.ApprovalDecision
		err      error
	}
	done := make(chan outcome, 1)

	go func() {
		decision, err := approver.Approve(context.Background(), policy.ApprovalRequest{
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
		request.reply <- policy.ApprovalDecision{Allow: true, Reason: "ok"}
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

	_, err := approver.Approve(ctx, policy.ApprovalRequest{Command: "echo hi"})
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

	pol, err := policy.New(t.TempDir(), policy.Options{
		Shell: policy.ShellOptions{Mode: policy.ShellModeApproval},
	})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	m := initialModel(nil, pol)
	return &m
}

func TestModelApprovalAllow(t *testing.T) {
	m := newApprovalModel(t)
	reply := make(chan policy.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: policy.ApprovalRequest{Kind: "shell", Command: "echo hi"},
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
	reply := make(chan policy.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: policy.ApprovalRequest{Kind: "shell", Command: "echo hi"},
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
	reply := make(chan policy.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: policy.ApprovalRequest{Kind: "shell", Command: "echo hi"},
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

	reply := make(chan policy.ApprovalDecision, 1)
	updated, _ = m.Update(approvalRequestMsg{
		request: policy.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)

	updated, cmd := m.Update(doneMsg{})
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

	updated, _ = m.Update(canceledMsg{})
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
	reply := make(chan policy.ApprovalDecision, 1)

	updated, _ := m.Update(approvalRequestMsg{
		request: policy.ApprovalRequest{Kind: "shell", Command: "echo hi"},
		reply:   reply,
	})
	m = updated.(*model)
	m.state = StateStreaming

	updated, cmd := m.Update(canceledMsg{})
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

	updated, _ = m.Update(fatalErrMsg{error: errors.New("boom")})
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
