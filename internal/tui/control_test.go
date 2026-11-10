package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/agent"
)

func TestStreamingEnterQueuesSteeringInstruction(t *testing.T) {
	m, fake, _ := newSessionModel(t)
	m.state = StateStreaming
	m.inputText.SetValue("Stop. Use the existing structure.")

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	m = updated.(*model)

	if got := m.inputText.Value(); got != "" {
		t.Fatalf("input after steer = %q, want empty", got)
	}
	if len(m.pendingSteering) != 1 || m.pendingSteering[0] != "Stop. Use the existing structure." {
		t.Fatalf("pending steering = %+v", m.pendingSteering)
	}
	if cmd == nil {
		t.Fatal("streaming Enter returned no command")
	}

	updated, _ = m.Update(cmd())
	m = updated.(*model)

	if len(fake.steering) != 1 || fake.steering[0] != "Stop. Use the existing structure." {
		t.Fatalf("agent steering = %+v", fake.steering)
	}
	if !containsNotice(m, "Steering queued") {
		t.Fatalf("steering notice missing: %+v", m.messages)
	}
	if !strings.Contains(m.spinnerBar(), "steering: 1") {
		t.Fatalf("spinner bar = %q, want pending steering", m.spinnerBar())
	}
}

func TestStreamingAltEnterQueuesFollowUp(t *testing.T) {
	m, fake, _ := newSessionModel(t)
	m.state = StateStreaming
	m.inputText.SetValue("After you finish, summarize the change.")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m = updated.(*model)

	if len(m.pendingFollowUps) != 1 || m.pendingFollowUps[0] != "After you finish, summarize the change." {
		t.Fatalf("pending follow-ups = %+v", m.pendingFollowUps)
	}
	if cmd == nil {
		t.Fatal("streaming Alt+Enter returned no command")
	}

	updated, _ = m.Update(cmd())
	m = updated.(*model)

	if len(fake.followUps) != 1 || fake.followUps[0] != "After you finish, summarize the change." {
		t.Fatalf("agent follow-ups = %+v", fake.followUps)
	}
	if !containsNotice(m, "Follow-up queued") {
		t.Fatalf("follow-up notice missing: %+v", m.messages)
	}
}

func TestInstructionResultRestoresRejectedText(t *testing.T) {
	m, fake, _ := newSessionModel(t)
	m.state = StateStreaming
	fake.steerErr = agent.ErrAgentIdle
	m.inputText.SetValue("keep this instruction")

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	m = updated.(*model)

	updated, _ = m.Update(cmd())
	m = updated.(*model)

	if got := m.inputText.Value(); got != "keep this instruction" {
		t.Fatalf("restored input = %q", got)
	}
	if len(m.pendingSteering) != 0 {
		t.Fatalf("pending steering = %+v, want empty", m.pendingSteering)
	}
	if !containsNotice(m, "restored to the input") {
		t.Fatalf("restore notice missing: %+v", m.messages)
	}
}

func TestQueueEventsUpdatePendingStateAndTranscript(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.state = StateStreaming

	updated, _ := m.Update(agentEventMsg{event: agent.QueueUpdated{
		Steering:  []string{"change direction"},
		FollowUps: []string{"then summarize"},
	}})
	m = updated.(*model)

	if len(m.pendingSteering) != 1 || len(m.pendingFollowUps) != 1 {
		t.Fatalf("pending state = %+v / %+v", m.pendingSteering, m.pendingFollowUps)
	}
	bar := m.spinnerBar()
	if !strings.Contains(bar, "steering: 1") || !strings.Contains(bar, "follow-up: 1") {
		t.Fatalf("spinner bar = %q, want queue counts", bar)
	}

	updated, _ = m.Update(agentEventMsg{event: agent.InstructionDelivered{
		Kind: agent.InstructionSteering,
		Text: "change direction",
	}})
	m = updated.(*model)

	last := m.messages[len(m.messages)-1]
	if last.role != "user" || last.tag != "steer" || last.content != "change direction" {
		t.Fatalf("delivered message = %+v", last)
	}
	if rendered := m.renderMessage(last); !strings.Contains(rendered, "You (steer)>") {
		t.Fatalf("rendered message = %q", rendered)
	}

	updated, _ = m.Update(agentEventMsg{event: agent.QueueUpdated{}})
	m = updated.(*model)
	if len(m.pendingSteering) != 0 || len(m.pendingFollowUps) != 0 {
		t.Fatalf("pending state after empty update = %+v / %+v", m.pendingSteering, m.pendingFollowUps)
	}
}

func TestAbortRestoresQueuedInstructions(t *testing.T) {
	m, fake, _ := newSessionModel(t)
	m.state = StateStreaming
	fake.steering = []string{"keep this"}
	fake.followUps = []string{"and this"}
	m.pendingSteering = []string{"keep this"}
	m.pendingFollowUps = []string{"and this"}

	updated, _ := m.Update(abortRunCommand(fake)())
	m = updated.(*model)

	input := m.inputText.Value()
	if !strings.Contains(input, "keep this") || !strings.Contains(input, "and this") {
		t.Fatalf("restored input = %q", input)
	}
	if len(fake.steering) != 0 || len(fake.followUps) != 0 {
		t.Fatalf("agent queue after abort = %+v / %+v", fake.steering, fake.followUps)
	}
	if len(m.pendingSteering) != 0 || len(m.pendingFollowUps) != 0 {
		t.Fatalf("pending state after abort = %+v / %+v", m.pendingSteering, m.pendingFollowUps)
	}
	if !containsNotice(m, "restored to the input") {
		t.Fatalf("restore notice missing: %+v", m.messages)
	}
}

func TestClearQueueCommandRestoresInput(t *testing.T) {
	m, fake, _ := newSessionModel(t)
	fake.steering = []string{"first"}
	fake.followUps = []string{"second"}
	m.pendingSteering = []string{"first"}
	m.pendingFollowUps = []string{"second"}

	updated, _ := m.handleCommand("/clear-queue")
	m = updated.(*model)

	if got := m.inputText.Value(); got != "first\nsecond" {
		t.Fatalf("input after /clear-queue = %q", got)
	}
	if len(fake.steering) != 0 || len(fake.followUps) != 0 {
		t.Fatalf("agent queue after /clear-queue = %+v / %+v", fake.steering, fake.followUps)
	}
	if len(m.pendingSteering) != 0 || len(m.pendingFollowUps) != 0 {
		t.Fatalf("pending state after /clear-queue = %+v / %+v", m.pendingSteering, m.pendingFollowUps)
	}
	if !containsNotice(m, "Cleared 2 queued instruction(s)") {
		t.Fatalf("clear notice missing: %+v", m.messages)
	}
}

func TestSessionCommandsAreRejectedWhileStreaming(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.state = StateStreaming
	m.inputText.SetValue("/new")

	updated, _ := m.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	m = updated.(*model)

	if !containsNotice(m, "Commands are unavailable while Iris is running") {
		t.Fatalf("rejection notice missing: %+v", m.messages)
	}
	if len(m.pendingSteering) != 0 {
		t.Fatalf("command was queued as steering: %+v", m.pendingSteering)
	}
}

func TestTurnRetriedClearsPartialOutput(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.state = StateStreaming
	m.fullResponse.WriteString("partial answer")
	m.fullReason.WriteString("partial reasoning")
	m.toolName = "write"
	m.toolArgs.WriteString(`{"path":"a"}`)

	updated, _ := m.Update(agentEventMsg{event: agent.TurnRetried{
		Turn:        0,
		Reason:      "context_overflow",
		NextAttempt: 2,
	}})
	m = updated.(*model)

	if m.fullResponse.Len() != 0 || m.fullReason.Len() != 0 || m.toolName != "" || m.toolArgs.Len() != 0 {
		t.Fatalf("partial output was not cleared: response=%q reasoning=%q tool=%q args=%q",
			m.fullResponse.String(), m.fullReason.String(), m.toolName, m.toolArgs.String())
	}
	if !containsNotice(m, "compacted the conversation and retrying") {
		t.Fatalf("retry notice missing: %+v", m.messages)
	}
}

func TestEmptyTurnNotice(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.state = StateStreaming

	updated, _ := m.Update(agentEventMsg{event: agent.TurnFinished{
		Turn:           0,
		Attempts:       1,
		FinishReason:   "stop",
		ContentBytes:   0,
		ReasoningBytes: 0,
		ToolCalls:      0,
	}})
	m = updated.(*model)

	updated, _ = m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)

	if !containsNotice(m, "Iris returned an empty response") {
		t.Fatalf("empty response notice missing: %+v", m.messages)
	}
}

func TestTruncatedTurnNotice(t *testing.T) {
	m, _, _ := newSessionModel(t)
	m.state = StateStreaming

	updated, _ := m.Update(agentEventMsg{event: agent.TurnFinished{
		Turn:         0,
		Attempts:     1,
		FinishReason: "length",
		ContentBytes: 12,
	}})
	m = updated.(*model)

	if !containsNotice(m, "output token limit") {
		t.Fatalf("truncation notice missing: %+v", m.messages)
	}
}
