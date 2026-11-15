package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

// approvalTickMsg refreshes the visible countdown.
type approvalTickMsg struct{}

// tuiApprover implements agent.Approver with a compact interactive panel. It
// also remembers commands the user explicitly allowed for this session. The
// cache is exact-match only: a different command, even with the same program,
// asks again.
type tuiApprover struct {
	requests chan approvalRequestMsg

	mu      sync.Mutex
	allowed map[string]struct{}
}

func newTUIApprover() *tuiApprover {
	return &tuiApprover{
		requests: make(chan approvalRequestMsg),
		allowed:  make(map[string]struct{}),
	}
}

// Approve sends the request to the TUI and waits for a decision or context
// cancellation. The reply channel is buffered so a late key press never blocks
// the UI after the policy has already timed out.
func (a *tuiApprover) Approve(ctx context.Context, request agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	if a.isAllowed(request) {
		return agent.ApprovalDecision{Allow: true, Reason: "approved for this session"}, nil
	}

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

// AllowSession remembers one exact command for the current session.
func (a *tuiApprover) AllowSession(request agent.ApprovalRequest) {
	if a == nil {
		return
	}
	key := approvalKey(request)
	if key == "" {
		return
	}
	a.mu.Lock()
	if a.allowed == nil {
		a.allowed = make(map[string]struct{})
	}
	a.allowed[key] = struct{}{}
	a.mu.Unlock()
}

// ClearAllowed forgets session-scoped approvals. It is called when the active
// session changes.
func (a *tuiApprover) ClearAllowed() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.allowed = make(map[string]struct{})
	a.mu.Unlock()
}

func (a *tuiApprover) isAllowed(request agent.ApprovalRequest) bool {
	if a == nil {
		return false
	}
	key := approvalKey(request)
	if key == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.allowed[key]
	return ok
}

func approvalKey(request agent.ApprovalRequest) string {
	command := strings.TrimSpace(request.Command)
	if command == "" {
		return ""
	}
	return strings.TrimSpace(request.Kind) + "\x00" +
		strings.TrimSpace(request.Workdir) + "\x00" +
		command
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

func (m *model) approvalTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return approvalTickMsg{}
	})
}

func (m *model) handleApprovalKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "pgup":
		m.viewport.PageUp()
		m.updateFollow()
		return m, nil
	case "pgdown":
		m.viewport.PageDown()
		m.updateFollow()
		return m, nil
	case "ctrl+u":
		m.viewport.HalfPageUp()
		m.updateFollow()
		return m, nil
	case "ctrl+d":
		m.viewport.HalfPageDown()
		m.updateFollow()
		return m, nil
	case "y", "Y":
		m.resolveApproval(agent.ApprovalDecision{Allow: true, Reason: "approved by user"})
		return m, m.waitApproval()
	case "a", "A":
		if m.pendingApproval != nil {
			m.approver.AllowSession(m.pendingApproval.request)
		}
		m.resolveApproval(agent.ApprovalDecision{Allow: true, Reason: "approved for this session"})
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
		m.syncInputPrompt()
		return m, tea.Batch(m.waitApproval(), abortRunCommand(m.agent))
	}
	return m, nil
}

func (m *model) resolveApproval(decision agent.ApprovalDecision) {
	pending := m.pendingApproval
	m.recordApprovalWait(pending)
	m.pendingApproval = nil
	m.approvalDeadline = time.Time{}
	m.inputText.Focus()
	m.syncInputPrompt()
	m.syncPlaceholder()
	m.resizeLayout(false)
	if pending == nil || pending.reply == nil {
		return
	}

	select {
	case pending.reply <- decision:
	default:
		// The policy gave up (timeout) and is no longer listening.
	}
}

// recordApprovalWait remembers how much of a tool's wall time was spent
// waiting for the user so the ledger can subtract it.
func (m *model) recordApprovalWait(pending *approvalRequestMsg) {
	if pending == nil || m.approvalWaitStart.IsZero() {
		return
	}
	command := strings.TrimSpace(pending.request.Command)
	if command != "" {
		if m.approvalWaits == nil {
			m.approvalWaits = make(map[string]time.Duration)
		}
		m.approvalWaits[command] += time.Since(m.approvalWaitStart)
	}
	m.approvalWaitStart = time.Time{}
}

func (m *model) approvalRemaining() time.Duration {
	if m.approvalDeadline.IsZero() {
		return 0
	}
	remaining := time.Until(m.approvalDeadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func formatCountdown(duration time.Duration) string {
	if duration <= 0 {
		return "expired"
	}
	total := int(duration.Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func (m *model) approvalHeight() int {
	if m.pendingApproval == nil {
		return 0
	}
	return len(m.approvalPanelLines())
}

func (m *model) approvalPanel() string {
	return strings.Join(m.approvalPanelLines(), "\n")
}

func (m *model) approvalPanelLines() []string {
	pending := m.pendingApproval
	if pending == nil {
		return nil
	}

	width := m.contentWidth()
	kind := sanitizeLine(pending.request.Kind)
	if kind == "" {
		kind = "action"
	}

	title := "  " + styleYellow.Render("⚠ approval") +
		styleDim.Render(" · "+kind+" · ") +
		styleYellow.Render(formatCountdown(m.approvalRemaining()))
	lines := []string{title}

	command := sanitizeDisplayText(strings.TrimSpace(pending.request.Command))
	if command == "" {
		command = "(no command supplied)"
	}
	maxCommandLines := 5
	if m.height > 0 {
		maxCommandLines = max(min(m.height-7, 5), 1)
	}
	for _, line := range wrapApprovalCommand(command, max(width-4, 10), maxCommandLines) {
		lines = append(lines, "    "+line)
	}

	context := make([]string, 0, 3)
	if workdir := displayPath(pending.request.Workdir); workdir != "" {
		context = append(context, workdir)
	}
	if reason := sanitizeLine(pending.request.Reason); reason != "" {
		context = append(context, reason)
	}
	if len(pending.request.Programs) > 0 {
		context = append(context, "programs: "+strings.Join(pending.request.Programs, ", "))
	}
	if len(context) == 0 {
		context = append(context, "no additional context")
	}
	contextLine := truncateMiddleDisplay(sanitizeDisplayText(strings.Join(context, " · ")), max(width-4, 10))
	lines = append(lines, "    "+styleDim.Render(contextLine))

	keys := "[y] allow once   [a] always this session   [n] deny   ·   ctrl+c abort"
	if lipgloss.Width(keys)+4 > width {
		keys = "[y] once  [a] session  [n] deny  ·  ctrl+c abort"
	}
	if lipgloss.Width(keys)+4 > width {
		keys = "[y] once  [a] session  [n] deny"
	}
	lines = append(lines, "    "+styleMuted.Render(keys))
	return lines
}

// wrapApprovalCommand wraps a command and keeps both ends when it exceeds
// maxLines. Truncating only the tail could hide a destructive final argument.
func wrapApprovalCommand(command string, width, maxLines int) []string {
	if width < 1 {
		width = 1
	}
	wrapped := ansi.Wordwrap(command, width, "")
	lines := strings.Split(wrapped, "\n")
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}

	head := maxLines / 2
	tail := maxLines - head - 1
	result := make([]string, 0, maxLines)
	result = append(result, lines[:head]...)
	result = append(result, fmt.Sprintf("… %d lines omitted", len(lines)-head-tail))
	result = append(result, lines[len(lines)-tail:]...)
	return result
}
