package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/m4ns0ur/iris/agent"
)

// approvalRequestMsg carries an approval request from the agent goroutine to
// the TUI event loop.
type approvalRequestMsg struct {
	request agent.ApprovalRequest
	reply   chan agent.ApprovalDecision
}

// approvalTimeoutMsg clears a prompt whose policy deadline has elapsed, so
// the user is not stuck on a request the agent has already abandoned.
type approvalTimeoutMsg struct {
	reply chan agent.ApprovalDecision
}

// tuiApprover implements agent.Approver using a minimal interactive prompt.
// It deliberately stays small: a single line and y/n keys. More elaborate
// permission UIs can be supplied through the public Agent.SetApprover method
// or an external approval hook.
type tuiApprover struct {
	requests chan approvalRequestMsg
}

func newTUIApprover() *tuiApprover {
	return &tuiApprover{requests: make(chan approvalRequestMsg)}
}

// Approve sends the request to the TUI and waits for a decision or context
// cancellation. The reply channel is buffered so a late key press never blocks
// the UI after the policy has already timed out.
func (a *tuiApprover) Approve(ctx context.Context, request agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	reply := make(chan agent.ApprovalDecision, 1)

	select {
	case a.requests <- approvalRequestMsg{request: request, reply: reply}:
	case <-ctx.Done():
		return agent.ApprovalDecision{}, ctx.Err()
	}

	select {
	case decision := <-reply:
		return decision, nil
	case <-ctx.Done():
		return agent.ApprovalDecision{}, ctx.Err()
	}
}

func waitForApproval(requests <-chan approvalRequestMsg) tea.Cmd {
	if requests == nil {
		return nil
	}
	return func() tea.Msg {
		request, ok := <-requests
		if !ok {
			return nil
		}
		return request
	}
}

func (m *model) waitApproval() tea.Cmd {
	if m.approvals == nil {
		return nil
	}
	return waitForApproval(m.approvals)
}

func (m *model) handleApprovalKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		m.resolveApproval(agent.ApprovalDecision{Allow: true, Reason: "approved by user"})
		return m, m.waitApproval()
	case "n", "N", "esc":
		m.resolveApproval(agent.ApprovalDecision{Allow: false, Reason: "denied by user"})
		return m, m.waitApproval()
	case "ctrl+c":
		if m.canceling {
			return m, m.quit()
		}
		m.resolveApproval(agent.ApprovalDecision{Allow: false, Reason: "canceled by user"})
		m.canceling = true
		if m.agent != nil {
			m.agent.Cancel()
		}
		return m, m.waitApproval()
	}
	return m, nil
}

func (m *model) resolveApproval(decision agent.ApprovalDecision) {
	pending := m.pendingApproval
	m.pendingApproval = nil
	if pending == nil || pending.reply == nil {
		return
	}

	select {
	case pending.reply <- decision:
	default:
		// The policy gave up (timeout) and is no longer listening.
	}
}

func (m *model) approvalBar(width int) string {
	prefix := "⚠ shell approval: "
	suffix := "  [y] allow once · [n] deny"

	available := width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
	if available < 8 {
		suffix = " [y]/[n]"
		available = width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
	}

	if available < 1 {
		return lipgloss.NewStyle().
			Width(width).
			Foreground(lipgloss.Color(ColorYellow)).
			Render(truncateRunes("⚠ approve shell? [y]/[n]", width))
	}

	command := summarizeCommand(m.pendingApproval.request.Command, available)
	line := prefix + command + suffix

	return lipgloss.NewStyle().
		Width(width).
		Foreground(lipgloss.Color(ColorYellow)).
		Render(line)
}

func summarizeCommand(command string, max int) string {
	// Strip control characters so a model-provided command cannot inject
	// terminal escape sequences into the approval prompt.
	command = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, command)
	command = strings.Join(strings.Fields(command), " ")
	return truncateRunes(command, max)
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}

	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 1 {
		return "…"
	}
	if max < 8 {
		return string(runes[:max-1]) + "…"
	}

	// Keep both ends so a destructive argument at the end of a long command
	// is not hidden by the approval prompt.
	head := (max - 1) / 2
	tail := max - 1 - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}
