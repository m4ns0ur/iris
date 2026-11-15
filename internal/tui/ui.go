package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/persist"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/resource"
	"github.com/m4ns0ur/iris/session"
)

const (
	InputHeight           = 1
	InputHeightMax        = 14
	InputContentHeightMax = 1000
	RuleHeight            = 1
	StatusHeight          = 1
	MinViewportHeight     = 5
)

type state int

const (
	StateInput state = iota
	StateStreaming
)

var Logger = logger.Logger

type lifecycle struct {
	once sync.Once
	done chan struct{}
}

func newLifecycle() *lifecycle {
	return &lifecycle{done: make(chan struct{})}
}

func (l *lifecycle) close() {
	if l == nil {
		return
	}
	l.once.Do(func() { close(l.done) })
}

// Options configures the TUI consumer.
type Options struct {
	Agent           agent.Agent
	Sessions        session.Manager
	SessionDefaults session.CreateOptions
	Resources       *resource.Resources

	// ContextWindow is the model context size used for the status meter.
	// Zero falls back to the session's model metadata and then to unknown.
	ContextWindow int
	// Policy is a short human-readable description of the active sandbox and
	// shell policy, shown by /status and in the startup header.
	Policy string
	// SessionDir is the directory containing session journals, shown by
	// /status. It is optional.
	SessionDir string
}

// activeTool is a tool call that has started but not yet completed. Each call
// is tracked separately; the old single toolName field lost every tool but the
// last one in a multi-tool turn.
type activeTool struct {
	id     string
	name   string
	args   string
	target string
}

type model struct {
	agent                agent.Agent
	fullReason           *strings.Builder
	fullResponse         *strings.Builder
	activeTools          map[string]*activeTool
	toolOrder            []string
	toolSeq              int
	width                int
	height               int
	state                state
	inputText            textarea.Model
	stream               chan tea.Msg
	spinner              spinner.Model
	messages             []message
	viewport             viewport.Model
	ready                bool
	follow               bool
	approvals            chan approvalRequestMsg
	approver             *tuiApprover
	pendingApproval      *approvalRequestMsg
	approvalTimeout      time.Duration
	approvalDeadline     time.Time
	approvalWaitStart    time.Time
	approvalWaits        map[string]time.Duration
	canceling            bool
	agentErrored         bool
	lastTurnEmpty        bool
	compacting           bool
	contextTokens        int
	sessionTokens        int
	contextWindow        int
	defaultContextWindow int
	modelName            string
	workspace            string
	policy               string
	sessionDir           string
	runStartedAt         time.Time
	lifecycle            *lifecycle
	sessions             session.Manager
	sessionDefaults      session.CreateOptions
	currentSession       *session.Session
	sessionPersisted     int
	sessionDirty         bool
	usagePending         []provider.Usage
	summaryUsagePending  provider.Usage
	lease                session.Lease
	recorder             *persist.Recorder
	recording            bool
	resources            *resource.Resources
	pendingSteering      []string
	pendingFollowUps     []string

	// Rendering and interaction state.
	showToolOutput      bool
	showReasoning       bool
	inputHistory        []string
	historyIndex        int
	historyDraft        string
	completionIndex     int
	completionDismissed string
	lastSummary         *agent.RunSummary
}

type message struct {
	role    string
	content string
	tag     string

	// Tool fields.
	toolCallID     string
	toolName       string
	toolTarget     string
	toolStatus     string
	toolMeta       string
	toolDetail     string
	toolOutput     string
	toolOutputPath string
	toolDuration   time.Duration
	toolExit       int

	// Summary fields.
	runSummary *agent.RunSummary
	outcome    string

	// Render cache.
	rendered  string
	renderKey renderKey
}

type agentEventMsg struct {
	event agent.Event
}

func (m *model) viewportHeight() int {
	if m.height <= 0 {
		return 1
	}
	return max(m.height-m.chromeHeight(), 1)
}

func (m *model) chromeHeight() int {
	return m.approvalHeight() + m.completionHeight() + m.queueHeight() + RuleHeight + m.inputText.Height() + StatusHeight
}

func (m *model) maxInputHeight() int {
	if m.height <= 0 {
		return InputHeightMax
	}
	limit := max(m.height/3, 4)
	limit = min(limit, InputHeightMax)
	available := m.height - MinViewportHeight - RuleHeight - StatusHeight - m.completionHeight() - m.approvalHeight() - m.queueHeight()
	if available < 1 {
		available = 1
	}
	return max(min(limit, available), 1)
}

func (m *model) contentWidth() int {
	if m.width < 1 {
		return 80
	}
	return max(m.width-1, 20)
}

func (m *model) buildContent() string {
	var sb strings.Builder
	previous := ""
	for index := range m.messages {
		msg := &m.messages[index]
		rendered := m.renderCached(msg)
		if rendered == "" {
			continue
		}
		if sb.Len() > 0 && messageNeedsGap(previous, msg.role) {
			sb.WriteByte('\n')
		}
		sb.WriteString(rendered)
		previous = msg.role
	}

	if live := m.renderLive(); live != "" {
		if sb.Len() > 0 && (previous != "tool" || !m.liveStartsWithTool()) {
			sb.WriteByte('\n')
		}
		sb.WriteString(live)
	}

	if sb.Len() == 0 {
		return ""
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

func (m *model) liveStartsWithTool() bool {
	if m.state != StateStreaming {
		return false
	}
	if m.showReasoning && m.fullReason.Len() > 0 {
		return false
	}
	if m.fullResponse.Len() > 0 {
		return false
	}
	return len(m.activeTools) > 0
}

func messageNeedsGap(previous, current string) bool {
	if previous == "" {
		return false
	}
	switch current {
	case "user", "assistant", "summary", "header":
		return true
	case "notice", "error":
		return previous != "notice" && previous != "error" && previous != "header"
	case "tool", "reasoning":
		return false
	default:
		return false
	}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.inputText.Focus(),
		waitForStream(m.stream),
	}
	if m.approvals != nil {
		cmds = append(cmds, waitForApproval(m.approvals))
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.viewport = viewport.New(
				viewport.WithWidth(m.contentWidth()),
				viewport.WithHeight(m.viewportHeight()),
			)
			m.viewport.SoftWrap = true
			m.viewport.FillHeight = true
			m.viewport.KeyMap = viewport.KeyMap{
				PageDown: key.NewBinding(key.WithKeys("pgdown")),
				PageUp:   key.NewBinding(key.WithKeys("pgup")),
			}
			m.ready = true
		}
		m.resizeLayout(true)
		return m, nil

	case approvalRequestMsg:
		if m.pendingApproval != nil {
			select {
			case msg.reply <- agent.ApprovalDecision{
				Allow:  false,
				Reason: "another approval request is already pending",
			}:
			default:
			}
			return m, nil
		}

		m.pendingApproval = &msg
		m.approvalDeadline = time.Now().Add(m.approvalTimeout)
		m.approvalWaitStart = time.Now()
		m.inputText.Blur()
		m.syncInputPrompt()
		m.syncPlaceholder()
		m.resizeLayout(false)
		return m, tea.Batch(
			m.approvalTickCmd(),
			tea.Tick(m.approvalTimeout, func(time.Time) tea.Msg {
				return approvalTimeoutMsg{reply: msg.reply}
			}),
		)

	case approvalTickMsg:
		if m.pendingApproval == nil {
			return m, nil
		}
		if time.Now().After(m.approvalDeadline) {
			return m, nil
		}
		return m, m.approvalTickCmd()

	case approvalTimeoutMsg:
		if m.pendingApproval != nil && m.pendingApproval.reply == msg.reply {
			m.recordApprovalWait(m.pendingApproval)
			m.pendingApproval = nil
			m.approvalDeadline = time.Time{}
			m.inputText.Focus()
			m.syncInputPrompt()
			m.syncPlaceholder()
			m.resizeLayout(false)
			return m, m.waitApproval()
		}
		return m, nil

	case agentEventMsg:
		return m.handleAgentEvent(msg.event)

	case instructionResultMsg:
		return m.handleInstructionResult(msg)

	case abortResultMsg:
		return m.handleAbortResult(msg)

	case tea.MouseWheelMsg:
		if m.pendingApproval != nil {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			m.updateFollow()
			return m, cmd
		}
	}

	if key, ok := msg.(tea.KeyPressMsg); ok {
		if m.pendingApproval != nil {
			return m.handleApprovalKey(key)
		}

		switch key.String() {
		case "ctrl+e":
			m.showToolOutput = !m.showToolOutput
			m.resizeLayout(false)
			return m, nil
		case "ctrl+r":
			m.showReasoning = !m.showReasoning
			m.resizeLayout(false)
			return m, nil
		}
	}

	switch m.state {
	case StateInput:
		if key, ok := msg.(tea.KeyPressMsg); ok {
			if handled, cmd := m.handleIdleKey(key); handled {
				return m, cmd
			}
			return m, m.updateInput(msg)
		}
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(wheel)
			m.updateFollow()
			return m, cmd
		}
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		m.updateFollow()
		cmds = append(cmds, vpCmd)
		return m, tea.Batch(append(cmds, m.updateInput(msg))...)

	case StateStreaming:
		if key, ok := msg.(tea.KeyPressMsg); ok {
			if isShiftEnter(key) {
				m.insertInputNewline()
				return m, nil
			}
			switch key.String() {
			case "ctrl+c":
				if m.canceling {
					return m, m.quit()
				}
				m.canceling = true
				m.syncInputPrompt()
				m.resizeLayout(false)
				return m, abortRunCommand(m.agent)
			case "enter":
				return m.submitStreamingInstruction(instructionSteer)
			case "alt+enter":
				return m.submitStreamingInstruction(instructionFollowUp)
			case "esc":
				if !m.viewport.AtBottom() {
					m.follow = true
					m.viewport.GotoBottom()
				}
				return m, nil
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
			}
			return m, m.updateInput(msg)
		}

		switch msg := msg.(type) {
		case tea.MouseWheelMsg:
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			m.updateFollow()
			return m, vpCmd
		case spinner.TickMsg:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

		return m, m.updateInput(msg)
	}

	return m, nil
}

func isShiftEnter(key tea.KeyPressMsg) bool {
	return key.Code == tea.KeyEnter && key.Mod.Contains(tea.ModShift)
}

// insertInputNewline inserts a newline without submitting the prompt.
func (m *model) insertInputNewline() {
	m.inputText.InsertString("\n")
	m.completionDismissed = ""
	m.resizeLayout(false)
}

// handleIdleKey handles keys that take precedence over the textarea.
func (m *model) handleIdleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	completionOpen := len(m.visibleCompletionItems()) > 0

	if completionOpen {
		switch key.String() {
		case "up":
			m.moveCompletion(-1)
			return true, nil
		case "down":
			m.moveCompletion(1)
			return true, nil
		case "tab":
			m.acceptCompletion()
			return true, nil
		case "esc":
			m.completionDismissed = m.inputText.Value()
			m.resizeLayout(false)
			return true, nil
		}
	}

	// Use the key code/modifier rather than the string form: some terminals
	// attach a Text value (for example "\r") to modified Enter, and the
	// textarea's key matcher would then miss it.
	if isShiftEnter(key) || key.String() == "alt+enter" {
		m.insertInputNewline()
		return true, nil
	}

	switch key.String() {
	case "enter":
		return true, m.submitInput()
	case "ctrl+c":
		return true, m.quit()
	case "esc":
		if !m.viewport.AtBottom() {
			m.follow = true
			m.viewport.GotoBottom()
			return true, nil
		}
	case "pgup":
		m.viewport.PageUp()
		m.updateFollow()
		return true, nil
	case "pgdown":
		m.viewport.PageDown()
		m.updateFollow()
		return true, nil
	case "ctrl+u":
		m.viewport.HalfPageUp()
		m.updateFollow()
		return true, nil
	case "ctrl+d":
		m.viewport.HalfPageDown()
		m.updateFollow()
		return true, nil
	case "up":
		if m.inputText.Line() == 0 && m.historyPrev() {
			return true, nil
		}
	case "down":
		if m.historyNext() {
			return true, nil
		}
	}
	return false, nil
}

func (m *model) updateInput(msg tea.Msg) tea.Cmd {
	oldHeight := m.inputText.Height()
	oldCompletion := m.completionHeight()

	var cmd tea.Cmd
	m.inputText, cmd = m.inputText.Update(msg)

	if m.inputText.Height() != oldHeight || m.completionHeight() != oldCompletion {
		m.resizeLayout(false)
	}
	return cmd
}

func (m *model) submitInput() tea.Cmd {
	input := strings.TrimSpace(m.inputText.Value())
	if input == "" {
		return nil
	}

	m.inputText.Reset()
	m.recordHistory(input)
	m.completionDismissed = ""
	m.completionIndex = 0

	if strings.HasPrefix(input, "/") {
		_, cmd := m.handleCommand(input)
		m.resizeLayout(false)
		m.follow = true
		m.viewport.GotoBottom()
		return cmd
	}

	m.messages = append(m.messages, message{role: "user", content: input})
	m.resetStreamingUI()
	m.lastSummary = nil
	m.state = StateStreaming
	m.follow = true
	m.runStartedAt = time.Now()
	m.syncInputPrompt()
	m.syncPlaceholder()
	m.resizeLayout(true)
	m.updateFollow()

	return m.send(input)
}

func (m *model) send(input string) tea.Cmd {
	if m.agent == nil {
		return nil
	}
	return tea.Batch(
		m.spinner.Tick,
		runAgentCommand(m.agent, input, m.done()),
	)
}

func (m *model) updateFollow() {
	m.follow = m.viewport.AtBottom()
}

func (m *model) View() tea.View {
	var v tea.View
	v.MouseMode = tea.MouseModeCellMotion
	if m.width > 0 {
		title := "iris"
		if workspace := workspaceLabel(m.workspace); workspace != "" {
			title += " · " + workspace
		}
		v.WindowTitle = title
	}

	if !m.ready {
		v.SetContent("Initializing Iris...")
		return v
	}

	parts := []string{m.viewport.View()}
	if m.pendingApproval != nil {
		parts = append(parts, m.approvalPanel())
	}
	if items := m.visibleCompletionItems(); len(items) > 0 {
		parts = append(parts, m.completionRule(), m.completionView())
	}
	parts = append(parts, m.inputRule())
	parts = append(parts, m.queueLines()...)
	parts = append(parts, m.inputText.View(), m.statusLine())
	v.SetContent(strings.Join(parts, "\n"))
	return v
}

// queueLines shows the next queued steering instruction and follow-up above
// the prompt, so the user can see what will be delivered without waiting for
// the model to reach a turn boundary.
func (m *model) queueLines() []string {
	if m.state != StateStreaming || m.pendingApproval != nil {
		return nil
	}
	width := m.contentWidth()
	lines := make([]string, 0, 2)
	if len(m.pendingSteering) > 0 {
		lines = append(lines, queueLine("steer", m.pendingSteering[0], len(m.pendingSteering)-1, width))
	}
	if len(m.pendingFollowUps) > 0 {
		lines = append(lines, queueLine("follow-up", m.pendingFollowUps[0], len(m.pendingFollowUps)-1, width))
	}
	return lines
}

func queueLine(kind, text string, more, width int) string {
	prefix := fmt.Sprintf("  ↳ %-9s ", kind)
	text = sanitizeLine(text)
	suffix := ""
	if more > 0 {
		suffix = fmt.Sprintf(" · +%d", more)
	}
	available := width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
	text = truncateMiddleDisplay(text, max(available, 8))
	return styleDim.Render(prefix) + styleMuted.Render(text) + styleDim.Render(suffix)
}

func (m *model) queueHeight() int {
	return len(m.queueLines())
}

func (m *model) inputRule() string {
	if m.width <= 0 {
		return ""
	}

	marker := ""
	if offset := m.inputText.ScrollYOffset(); offset > 0 {
		marker = fmt.Sprintf(" ↑ %d ", offset)
	}
	if lipgloss.Width(marker) >= m.width {
		marker = ""
	}
	lineWidth := max(m.width-lipgloss.Width(marker), 0)
	line := strings.Repeat("─", lineWidth) + marker
	return styleBorder.Render(line)
}

func (m *model) resizeLayout(forceBottom bool) {
	if m.width <= 0 || m.height <= 0 {
		return
	}

	m.inputText.MaxHeight = m.maxInputHeight()
	m.inputText.SetWidth(max(m.width-1, 10))
	height := m.inputText.Height()
	if height > m.inputText.MaxHeight {
		height = m.inputText.MaxHeight
	}
	m.inputText.SetHeight(height)
	m.repositionInput()

	m.viewport.SetWidth(max(m.width, 1))
	m.viewport.SetHeight(m.viewportHeight())

	offset := m.viewport.YOffset()
	m.viewport.SetContent(m.buildContent())
	if forceBottom || m.follow {
		m.follow = true
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(offset)
	}
}

func (m *model) statusLine() string {
	if m.width <= 0 {
		return ""
	}

	left := m.statusLeft()
	right := m.statusRight()

	p := m.currentPhase()
	frame := ""
	spinnerWidth := 0
	if m.state == StateStreaming && m.pendingApproval == nil {
		frame = m.spinnerFrame(p)
		spinnerWidth = lipgloss.Width(frame) + 1
	}

	leftWidth := lipgloss.Width(left) + spinnerWidth
	rightWidth := lipgloss.Width(right)
	if leftWidth+rightWidth+1 > m.width {
		// Context is the most useful ambient fact; trim the hint first.
		available := m.width - rightWidth - spinnerWidth - 1
		if available <= 0 {
			left = ""
			available = m.width - spinnerWidth
		}
		left = truncateMiddleDisplay(left, max(available, 0))
		leftWidth = lipgloss.Width(left) + spinnerWidth
	}
	gap := max(m.width-leftWidth-rightWidth, 0)
	leftStyled := ""
	if left != "" {
		leftStyled = styleMuted.Render(left)
	}
	if spinnerWidth > 0 {
		leftStyled = lipgloss.NewStyle().
			Foreground(lipgloss.Color(phaseColor(p))).
			Render(frame+" ") + leftStyled
	}
	return leftStyled + strings.Repeat(" ", gap) + right
}

func (m *model) statusLeft() string {
	if m.pendingApproval != nil {
		return "approval pending"
	}

	if m.state == StateStreaming {
		parts := make([]string, 0, 4)
		if !m.runStartedAt.IsZero() {
			parts = append(parts, elapsedLabel(time.Since(m.runStartedAt)))
		}
		if summary := m.pendingSummary(); summary != "" {
			parts = append(parts, summary)
		}
		if !m.viewport.AtBottom() {
			parts = append(parts, "↓ "+fmt.Sprintf("%.0f%%", m.viewport.ScrollPercent()*100)+" · esc")
		}
		if m.canceling {
			parts = append(parts, "ctrl+c quit")
		} else {
			parts = append(parts, "ctrl+c abort")
		}
		return strings.Join(parts, " · ")
	}

	if !m.viewport.AtBottom() {
		return "↓ " + fmt.Sprintf("%.0f%%", m.viewport.ScrollPercent()*100) + " · esc to follow"
	}

	value := m.inputText.Value()
	if len(m.completionItems()) > 0 {
		return "tab complete · enter run · /help"
	}
	if strings.Contains(value, "\n") {
		return "enter send · shift+enter newline · /help"
	}
	return "enter send · shift+enter newline · /help"
}

func (m *model) statusRight() string {
	modelName := sanitizeLine(m.modelName)
	workspace := sanitizeLine(workspaceLabel(m.workspace))

	extras := make([]string, 0, 2)
	if modelName != "" {
		extras = append(extras, modelName)
	}
	if workspace != "" {
		extras = append(extras, workspace)
	}

	ctx := m.contextLabel()
	total := m.sessionTotalLabel()
	ctxStyle := styleMuted
	if m.contextWindow > 0 && m.contextTokens > 0 {
		ratio := float64(m.contextTokens) / float64(m.contextWindow)
		switch {
		case ratio >= 0.9:
			ctxStyle = styleRed
		case ratio >= 0.75:
			ctxStyle = styleYellow
		}
	}

	compose := func(withTotal bool) []string {
		values := make([]string, 0, len(extras)+2)
		values = append(values, extras...)
		if ctx != "" {
			values = append(values, ctx)
		}
		if withTotal && total != "" {
			values = append(values, total)
		}
		return values
	}
	widthOf := func(values []string) int {
		totalWidth := 3 * max(len(values)-1, 0)
		for _, value := range values {
			totalWidth += lipgloss.Width(value)
		}
		return totalWidth
	}

	// Drop the workspace, then the model name, then the total: context is the
	// most useful fact when the terminal is narrow.
	for len(extras) > 0 && widthOf(compose(true)) > m.width {
		extras = extras[:len(extras)-1]
	}
	withTotal := total != ""
	if withTotal && widthOf(compose(true)) > m.width {
		withTotal = false
	}
	if widthOf(compose(withTotal)) > m.width {
		ctx = truncateMiddleDisplay(ctx, m.width)
	}

	right := ""
	if len(extras) > 0 {
		right = styleMuted.Render(strings.Join(extras, " · "))
	}
	appendPart := func(text string, style lipgloss.Style) {
		if text == "" {
			return
		}
		if right != "" {
			right += styleFaint.Render(" · ")
		}
		right += style.Render(text)
	}
	appendPart(ctx, ctxStyle)
	if withTotal {
		appendPart(total, styleDim)
	}
	return right
}

func (m *model) contextLabel() string {
	window := m.contextWindow
	tokens := m.contextTokens
	switch {
	case window > 0 && tokens > 0:
		return fmt.Sprintf("ctx %s/%s", formatTokens(tokens), formatTokens(window))
	case window > 0:
		return fmt.Sprintf("ctx —/%s", formatTokens(window))
	case tokens > 0:
		return fmt.Sprintf("ctx %s", formatTokens(tokens))
	default:
		return ""
	}
}

func (m *model) sessionTotalLabel() string {
	if m.sessionTokens <= 0 {
		return ""
	}
	return "total " + formatPreciseTokens(m.sessionTokens)
}

// repositionInput keeps the cursor visible after a bulk value change or a
// height cap. The textarea only repositions against content that has already
// been rendered, so ask it for a view first and then re-apply its height.
func (m *model) repositionInput() {
	if m.inputText.Height() < 1 {
		return
	}
	_ = m.inputText.View()
	m.inputText.SetHeight(m.inputText.Height())
}

func (m *model) syncInputPrompt() {
	color := ColorBorderFocus
	switch {
	case m.pendingApproval != nil:
		color = ColorYellow
	case m.canceling:
		color = ColorRedSoft
	case m.state == StateStreaming:
		color = ColorOrange
	}

	styles := m.inputText.Styles()
	prompt := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	styles.Focused.Prompt = prompt
	styles.Blurred.Prompt = prompt
	m.inputText.SetStyles(styles)
}

func (m *model) syncPlaceholder() {
	switch {
	case m.pendingApproval != nil:
		m.inputText.Placeholder = "Approval pending…"
	case m.state == StateStreaming:
		m.inputText.Placeholder = "Steer Iris…"
	default:
		m.inputText.Placeholder = "Ask Iris…"
	}
}

func (m *model) handleAgentEvent(event agent.Event) (tea.Model, tea.Cmd) {
	rearmApproval := false

	switch e := event.(type) {
	case agent.AgentStarted:
		// State is already streaming; no action.

	case agent.TurnStarted:
		m.flushPartialStream()

	case agent.TurnRetried:
		m.fullReason.Reset()
		m.fullResponse.Reset()
		m.clearActiveTools()
		m.notice("Context limit reached; compacted the conversation and retrying the request.")

	case agent.TurnFinished:
		m.lastTurnEmpty = e.Err == "" && !e.Canceled &&
			e.ContentBytes == 0 && e.ReasoningBytes == 0 && e.ToolCalls == 0
		if e.Err == "" && !e.Canceled && provider.IsTruncatedFinish(e.FinishReason) {
			m.notice("The response hit the output token limit.")
		}

	case agent.AssistantDelta:
		m.fullResponse.WriteString(e.Text)

	case agent.ReasoningDelta:
		m.fullReason.WriteString(e.Text)

	case agent.ToolProgress:
		m.trackToolProgress(e)

	case agent.ToolStarted:
		m.trackToolStarted(e)

	case agent.ToolCompleted:
		m.finishToolCompleted(e)

	case agent.ToolFailed:
		m.finishToolFailed(e)

	case agent.Usage:
		m.contextTokens = e.PromptTokens + e.CompletionTokens
		usage := provider.Usage{
			PromptTokens:     e.PromptTokens,
			CompletionTokens: e.CompletionTokens,
			TotalTokens:      e.TotalTokens,
		}
		m.sessionTokens += usageTotal(usage)
		if !m.recording {
			m.usagePending = append(m.usagePending, usage)
		}

	case agent.ContextCompactionStarted:
		m.compacting = true

	case agent.ContextCompactionFinished:
		m.compacting = false
		m.contextTokens = e.AfterTokens
		text := fmt.Sprintf(
			"Context compacted: %d → %d estimated tokens",
			e.BeforeTokens,
			e.AfterTokens,
		)
		if e.SummarizedMessages > 0 {
			text += fmt.Sprintf(" · %d messages summarized", e.SummarizedMessages)
		}
		if e.TrimmedToolResults > 0 {
			text += fmt.Sprintf(" · %d tool results trimmed", e.TrimmedToolResults)
		}
		if e.Forced {
			text += " · forced after context error"
		}
		if e.SummaryFallback {
			text += " · fallback summary"
		}
		text += "."
		if e.SummarizedMessages == 0 && e.TrimmedToolResults == 0 {
			text = "Context compaction finished without changes."
		}
		m.notice(text)

		// Compaction summaries are real provider calls, but they do not map to
		// an assistant entry. Defer updating the session aggregate until the
		// model goroutine is idle so it cannot race the recorder appending from
		// the run goroutine.
		m.sessionTokens += e.SummaryPromptTokens + e.SummaryCompletionTokens
		m.summaryUsagePending.PromptTokens += e.SummaryPromptTokens
		m.summaryUsagePending.CompletionTokens += e.SummaryCompletionTokens
		m.summaryUsagePending.TotalTokens += e.SummaryPromptTokens + e.SummaryCompletionTokens

	case agent.QueueUpdated:
		m.pendingSteering = append([]string(nil), e.Steering...)
		m.pendingFollowUps = append([]string(nil), e.FollowUps...)

	case agent.InstructionDelivered:
		tag := "steer"
		kind := instructionSteer
		if e.Kind == agent.InstructionFollowUp {
			tag = "follow-up"
			kind = instructionFollowUp
		}
		m.removePending(kind, e.Text)
		m.messages = append(m.messages, message{
			role:    "user",
			content: e.Text,
			tag:     tag,
		})

	case agent.AgentError:
		m.flushPartialStream()
		m.messages = append(m.messages, message{role: "error", content: e.Message})
		m.agentErrored = true

	case agent.AgentFinished:
		rearmApproval = m.pendingApproval != nil
		if rearmApproval {
			m.resolveApproval(agent.ApprovalDecision{
				Allow:  false,
				Reason: "run finished before the approval was answered",
			})
		}

		m.flushPartialStream()

		if e.Canceled {
			if e.Summary == nil {
				m.notice("Canceled.")
			}
		} else if e.Err != "" && !m.agentErrored {
			m.messages = append(m.messages, message{role: "error", content: e.Err})
		} else if m.lastTurnEmpty {
			m.notice("Iris returned an empty response.")
		}
		m.lastTurnEmpty = false

		if e.Summary != nil {
			outcome := "Task completed"
			if e.Canceled {
				outcome = "Run canceled"
			} else if e.Err != "" {
				outcome = "Run failed"
			}
			m.lastSummary = e.Summary
			m.messages = append(m.messages, message{
				role:       "summary",
				outcome:    outcome,
				runSummary: e.Summary,
			})
		}

		m.canceling = false
		m.compacting = false
		m.pendingSteering = nil
		m.pendingFollowUps = nil
		m.approvalWaitStart = time.Time{}
		m.approvalWaits = make(map[string]time.Duration)
		m.state = StateInput
		m.runStartedAt = time.Time{}
		m.inputText.Focus()
		m.syncInputPrompt()
		m.syncPlaceholder()
		m.persistSession()
	}

	m.resizeLayout(false)
	if m.follow {
		m.viewport.GotoBottom()
	}

	cmds := []tea.Cmd{waitForStream(m.stream)}
	if rearmApproval {
		cmds = append(cmds, m.waitApproval())
	}
	return m, tea.Batch(cmds...)
}

// submitStreamingInstruction turns the current input into a steering message
// or follow-up without leaving the streaming state.
func (m *model) submitStreamingInstruction(kind instructionKind) (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.inputText.Value())
	if input == "" {
		return m, nil
	}

	if strings.HasPrefix(input, "/") {
		switch strings.Fields(input)[0] {
		case "/clear-queue", "/help", "/?", "/status", "/quit", "/exit":
			m.inputText.Reset()
			m.recordHistory(input)
			m.resizeLayout(false)
			return m.handleCommand(input)
		default:
			m.notice("Commands are unavailable while Iris is running. Press ctrl+c to abort first.")
			m.follow = true
			m.viewport.GotoBottom()
			return m, nil
		}
	}

	m.inputText.Reset()
	m.recordHistory(input)
	m.completionDismissed = ""
	m.completionIndex = 0

	if kind == instructionFollowUp {
		m.pendingFollowUps = append(m.pendingFollowUps, input)
		m.notice("Follow-up queued until the current run stops.")
	} else {
		m.pendingSteering = append(m.pendingSteering, input)
		m.notice("Steering queued for the next turn.")
	}

	m.follow = true
	m.resizeLayout(true)
	return m, submitInstructionCommand(m.agent, kind, input)
}

func (m *model) handleInstructionResult(msg instructionResultMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}

	if errors.Is(msg.err, agent.ErrAgentIdle) {
		m.removePending(msg.kind, msg.text)
		m.restoreInstructionText(msg.text)
		m.notice("Iris finished before the instruction was queued; the text was restored to the input.")
	} else {
		m.removePending(msg.kind, msg.text)
		m.notice("Could not queue the instruction: " + msg.err.Error())
	}

	m.follow = true
	m.resizeLayout(true)
	return m, nil
}

func (m *model) handleAbortResult(msg abortResultMsg) (tea.Model, tea.Cmd) {
	texts := append(append([]string(nil), msg.steering...), msg.followUps...)
	for _, text := range texts {
		m.restoreInstructionText(text)
	}

	m.pendingSteering = nil
	m.pendingFollowUps = nil
	if len(texts) > 0 {
		m.notice("Queued instructions were restored to the input.")
	}

	m.follow = true
	m.resizeLayout(true)
	return m, nil
}

func (m *model) removePending(kind instructionKind, text string) {
	target := &m.pendingSteering
	if kind == instructionFollowUp {
		target = &m.pendingFollowUps
	}

	for i, value := range *target {
		if value == text {
			*target = append((*target)[:i], (*target)[i+1:]...)
			return
		}
	}
}

func (m *model) restoreInstructionText(text string) {
	current := m.inputText.Value()
	if strings.TrimSpace(current) == "" {
		m.inputText.SetValue(text)
	} else {
		m.inputText.SetValue(current + "\n" + text)
	}
	m.inputText.CursorEnd()
	m.inputText.Focus()
}

func (m *model) pendingSummary() string {
	count := len(m.pendingSteering) + len(m.pendingFollowUps)
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("%d queued", count)
}

// ---- Tool tracking -------------------------------------------------------

func (m *model) toolForEvent(id, name string) *activeTool {
	if id == "" {
		id = fmt.Sprintf("tool-%d", m.toolSeq)
		m.toolSeq++
	}
	if tool := m.activeTools[id]; tool != nil {
		if name != "" {
			tool.name = name
		}
		return tool
	}

	tool := &activeTool{id: id, name: name}
	if tool.name == "" {
		tool.name = "tool"
	}
	m.activeTools[id] = tool
	m.toolOrder = append(m.toolOrder, id)
	return tool
}

func (m *model) trackToolProgress(e agent.ToolProgress) {
	if e.ToolName == "" && e.ArgumentsDelta == "" {
		return
	}
	// Assistant text that precedes a tool call belongs before the tool in the
	// transcript. Flush it now so an immediately appended tool line cannot
	// overtake it.
	m.flushTextOnly()
	tool := m.toolForEvent(e.ToolCallID, e.ToolName)
	if e.ArgumentsDelta != "" {
		tool.args += e.ArgumentsDelta
		m.applyToolPreview(tool)
	}
}

func (m *model) trackToolStarted(e agent.ToolStarted) {
	m.flushTextOnly()
	tool := m.toolForEvent(e.ToolCallID, e.ToolName)
	if e.ToolName != "" {
		tool.name = e.ToolName
	}
	if len(e.Args) > 0 {
		if encoded, err := json.Marshal(e.Args); err == nil {
			tool.args = string(encoded)
		}
		tool.target = toolTargetFromArgs(tool.name, e.Args)
	}
	if tool.target == "" {
		m.applyToolPreview(tool)
	}
}

func (m *model) applyToolPreview(tool *activeTool) {
	name := tool.name
	if path, ok := partialJSONField(tool.args, "path"); ok {
		tool.target = path
	}
	if command, ok := partialJSONField(tool.args, "command"); ok {
		tool.target = command
	}
	if name == "" && tool.target != "" {
		tool.name = "tool"
	}
}

func toolTargetFromArgs(name string, args map[string]any) string {
	field := "path"
	switch name {
	case "shell":
		field = "command"
	case "read", "write", "edit":
		field = "path"
	}
	if value, ok := args[field].(string); ok {
		return value
	}
	// Fall back to the first useful string field.
	for _, key := range []string{"command", "path", "query"} {
		if value, ok := args[key].(string); ok {
			return value
		}
	}
	return ""
}

func (m *model) finishToolCompleted(e agent.ToolCompleted) {
	tool := m.takeActiveTool(e.ToolCallID, e.ToolName)
	if tool == nil {
		tool = &activeTool{id: e.ToolCallID, name: e.ToolName}
	}

	duration := e.Duration
	if wait := m.takeApprovalWait(tool); wait > 0 {
		duration -= wait
		if duration < 0 {
			duration = 0
		}
	}

	msg := message{
		role:         "tool",
		toolCallID:   e.ToolCallID,
		toolName:     e.ToolName,
		toolTarget:   tool.target,
		toolStatus:   "ok",
		toolDuration: duration,
	}
	if msg.toolName == "" {
		msg.toolName = tool.name
	}
	applyToolResult(&msg, tool.args, e.Result)
	m.messages = append(m.messages, msg)
}

// applyToolResult derives the compact ledger line from a tool result. It is
// shared by live completion events and session replay, so a resumed transcript
// shows the same exit codes, line counts, and edit deltas as the live one.
func applyToolResult(msg *message, argsJSON string, result map[string]any) {
	if result == nil {
		return
	}
	msg.toolOutput = toolResultOutput(result)
	msg.toolOutputPath = toolResultString(result, "full_output_file")

	if okValue, hasOK := result["ok"].(bool); hasOK && !okValue {
		msg.toolStatus = "error"
		errorValue, _ := result["error"].(map[string]any)
		detail := sanitizeLine(toolResultString(errorValue, "message"))
		if executed, ok := errorValue["executed"].(bool); ok && !executed {
			msg.toolStatus = "denied"
			if detail != "" {
				detail = "not run: " + detail
			}
		}
		msg.toolDetail = detail
		return
	}

	switch msg.toolName {
	case "shell":
		msg.toolExit = toolResultInt(result, "exit_code")
		msg.toolMeta = toolOutputMeta(msg.toolOutput)
		if msg.toolExit != 0 {
			msg.toolStatus = "error"
			msg.toolDetail = firstLine(msg.toolOutput)
		}
	case "read":
		msg.toolMeta = plural(toolResultInt(result, "lines"), "line")
	case "write":
		if lines, ok := toolWriteLines(argsJSON); ok {
			msg.toolMeta = fmt.Sprintf("+%d", lines)
		} else {
			msg.toolMeta = fmt.Sprintf("%d bytes", toolResultInt(result, "bytes"))
		}
	case "edit":
		changed, _ := result["changed"].(bool)
		if !changed {
			msg.toolMeta = "no change"
			return
		}
		if added, removed, ok := toolEditDelta(argsJSON); ok {
			msg.toolMeta = fmt.Sprintf("+%d −%d", added, removed)
		} else {
			msg.toolMeta = plural(toolResultInt(result, "edits"), "edit")
		}
	default:
		msg.toolMeta = toolOutputMeta(msg.toolOutput)
	}
}

func toolWriteLines(argsJSON string) (int, bool) {
	if strings.TrimSpace(argsJSON) == "" {
		return 0, false
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
		return 0, false
	}
	return countLines(payload.Content), true
}

func toolEditDelta(argsJSON string) (int, int, bool) {
	if strings.TrimSpace(argsJSON) == "" {
		return 0, 0, false
	}
	var payload struct {
		Edits []struct {
			Find    string `json:"find"`
			Replace string `json:"replace"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil || len(payload.Edits) == 0 {
		return 0, 0, false
	}
	added, removed := 0, 0
	for _, edit := range payload.Edits {
		added += countLines(edit.Replace)
		removed += countLines(edit.Find)
	}
	return added, removed, true
}

func (m *model) finishToolFailed(e agent.ToolFailed) {
	tool := m.takeActiveTool(e.ToolCallID, e.ToolName)
	if tool == nil {
		tool = &activeTool{id: e.ToolCallID, name: e.ToolName}
	}

	name := e.ToolName
	if name == "" {
		name = tool.name
	}
	status := "error"
	if !e.Executed {
		status = "denied"
	}

	detail := sanitizeLine(e.Message)
	if !e.Executed && detail != "" {
		detail = "not run: " + detail
	}

	duration := e.Duration
	if wait := m.takeApprovalWait(tool); wait > 0 {
		duration -= wait
		if duration < 0 {
			duration = 0
		}
	}

	m.messages = append(m.messages, message{
		role:         "tool",
		toolCallID:   e.ToolCallID,
		toolName:     name,
		toolTarget:   tool.target,
		toolStatus:   status,
		toolDuration: duration,
		toolDetail:   detail,
	})
}

// takeActiveTool removes and returns the active call matching an id, falling
// back to the name when a provider omits call ids.
func (m *model) takeActiveTool(id, name string) *activeTool {
	if id != "" {
		if tool := m.activeTools[id]; tool != nil {
			m.removeActiveTool(id)
			return tool
		}
	}
	if name != "" {
		for _, active := range m.toolOrder {
			tool := m.activeTools[active]
			if tool != nil && tool.name == name {
				m.removeActiveTool(active)
				return tool
			}
		}
	}
	return nil
}

func (m *model) removeActiveTool(id string) {
	delete(m.activeTools, id)
	for index, active := range m.toolOrder {
		if active == id {
			m.toolOrder = append(m.toolOrder[:index], m.toolOrder[index+1:]...)
			break
		}
	}
}

// takeApprovalWait removes a recorded approval wait for the tool's command.
// Tool durations start before approval is requested, so subtracting the wait
// keeps a fast command from looking slow just because the user was thinking.
func (m *model) takeApprovalWait(tool *activeTool) time.Duration {
	if tool == nil || len(m.approvalWaits) == 0 {
		return 0
	}
	command := strings.TrimSpace(tool.target)
	if command == "" {
		return 0
	}
	wait, ok := m.approvalWaits[command]
	if !ok {
		return 0
	}
	delete(m.approvalWaits, command)
	return wait
}

func (m *model) clearActiveTools() {
	m.activeTools = make(map[string]*activeTool)
	m.toolOrder = nil
}

func toolResultOutput(result map[string]any) string {
	if result == nil {
		return ""
	}
	for _, key := range []string{"output", "content"} {
		if value, ok := result[key].(string); ok && value != "" {
			return truncateOutput(sanitizeDisplayText(value))
		}
	}
	return ""
}

func toolResultString(result map[string]any, key string) string {
	if result == nil {
		return ""
	}
	value, _ := result[key].(string)
	return value
}

func toolResultInt(result map[string]any, key string) int {
	if result == nil {
		return 0
	}
	switch value := result[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	return 0
}

func toolOutputMeta(output string) string {
	lines := countLines(output)
	if lines <= 0 {
		return ""
	}
	return plural(lines, "line")
}

// ---- Misc ----------------------------------------------------------------

func (m *model) done() <-chan struct{} {
	if m.lifecycle == nil {
		return nil
	}
	return m.lifecycle.done
}

func (m *model) closeDone() {
	if m.lifecycle != nil {
		m.lifecycle.close()
	}
}

func (m *model) quit() tea.Cmd {
	if m.state == StateInput {
		m.persistSession()
	}
	if m.agent != nil {
		m.agent.Cancel()
	}
	m.closeDone()
	return tea.Quit
}

// flushTextOnly appends the current reasoning and response without touching
// active tools. It preserves the order of assistant prose before tool calls.
func (m *model) flushTextOnly() {
	if m.fullReason.Len() > 0 {
		m.messages = append(m.messages, message{
			role:    "reasoning",
			content: m.fullReason.String(),
		})
		m.fullReason.Reset()
	}
	if m.fullResponse.Len() > 0 {
		m.messages = append(m.messages, message{
			role:    "assistant",
			content: m.fullResponse.String(),
		})
		m.fullResponse.Reset()
	}
}

// flushPartialStream persists partial output before returning to input.
func (m *model) flushPartialStream() {
	m.flushTextOnly()

	for _, id := range m.toolOrder {
		tool := m.activeTools[id]
		if tool == nil {
			continue
		}
		m.messages = append(m.messages, message{
			role:       "tool",
			toolCallID: tool.id,
			toolName:   tool.name,
			toolTarget: tool.target,
			toolStatus: "interrupted",
			toolDetail: "no result was recorded",
		})
	}
	m.clearActiveTools()
}

func (m *model) resetStreamingUI() {
	m.fullReason.Reset()
	m.fullResponse.Reset()
	m.clearActiveTools()
	m.approvalWaitStart = time.Time{}
	m.approvalWaits = make(map[string]time.Duration)
	m.canceling = false
	m.agentErrored = false
	m.lastTurnEmpty = false
	m.compacting = false
	m.pendingSteering = nil
	m.pendingFollowUps = nil
}

// ---- Startup -------------------------------------------------------------

func waitForStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func Run(ctx context.Context, options Options) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m, err := initialModel(options)
	if err != nil {
		return err
	}

	prg := tea.NewProgram(&m, tea.WithContext(ctx))
	_, err = prg.Run()

	// Save an idle session before tearing down. A run interrupted by quit is
	// persisted by the AgentFinished path when it can still be observed.
	if m.state == StateInput {
		m.persistSession()
	}

	// Unblock the event subscription after the program stops. cmd calls
	// Agent.Close next, which cancels the run and waits for cleanup.
	m.closeDone()
	m.releaseSession()

	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

func initialModel(options Options) (model, error) {
	ta := textarea.New()
	ta.Placeholder = "Ask Iris…"
	ta.SetHeight(InputHeight)
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = InputHeight
	ta.MaxHeight = InputHeightMax
	ta.MaxContentHeight = InputContentHeightMax
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j", "alt+enter"))
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
	ta.Focus()

	styles := ta.Styles()
	styles.Focused.Base = lipgloss.NewStyle()
	styles.Focused.Text = lipgloss.NewStyle()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Focused.Placeholder = styleDim
	styles.Focused.Prompt = styleBlue
	styles.Blurred.Base = lipgloss.NewStyle()
	styles.Blurred.Text = styleDim
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()
	styles.Blurred.Placeholder = styleDim
	styles.Blurred.Prompt = styleBlue
	styles.Cursor.Color = lipgloss.Color(ColorBlue)
	styles.Cursor.Shape = tea.CursorBar
	styles.Cursor.Blink = true
	ta.SetStyles(styles)

	// The spinner model is only a redraw metronome; the visible frame is
	// derived from the run's elapsed time and the current phase family.
	spin := spinner.New()
	spin.Spinner = spinner.Spinner{
		Frames: []string{" "},
		FPS:    time.Second / 12,
	}

	stream := make(chan tea.Msg)
	lifecycle := newLifecycle()
	instance := options.Agent

	approvalTimeout := 5 * time.Minute
	var approvals chan approvalRequestMsg
	var approver *tuiApprover
	if instance != nil {
		if configured := instance.ApprovalTimeout(); configured > 0 {
			approvalTimeout = configured
		}
		if instance.Approver() == nil {
			approver = newTUIApprover()
			instance.SetApprover(approver)
			approvals = approver.requests
		}

		instance.Subscribe(func(event agent.Event) {
			select {
			case stream <- agentEventMsg{event: event}:
			case <-lifecycle.done:
			}
		})
	}

	var initialMessages []message

	if options.Resources != nil {
		for _, warning := range options.Resources.Warnings {
			initialMessages = append(initialMessages, message{
				role:    "notice",
				content: "Resource warning: " + warning,
			})
		}
	}

	workspace := options.SessionDefaults.Workspace
	modelName := options.SessionDefaults.Model

	m := model{
		agent:                instance,
		fullReason:           &strings.Builder{},
		fullResponse:         &strings.Builder{},
		activeTools:          make(map[string]*activeTool),
		state:                StateInput,
		inputText:            ta,
		stream:               stream,
		spinner:              spin,
		approvals:            approvals,
		approver:             approver,
		approvalTimeout:      approvalTimeout,
		approvalWaits:        make(map[string]time.Duration),
		lifecycle:            lifecycle,
		sessions:             options.Sessions,
		sessionDefaults:      options.SessionDefaults,
		resources:            options.Resources,
		messages:             initialMessages,
		follow:               true,
		historyIndex:         -1,
		contextWindow:        options.ContextWindow,
		defaultContextWindow: options.ContextWindow,
		modelName:            modelName,
		workspace:            workspace,
		policy:               options.Policy,
		sessionDir:           options.SessionDir,
	}

	if options.Sessions != nil {
		created, err := options.Sessions.Create(options.SessionDefaults)
		if err != nil {
			return model{}, err
		}
		if err := m.switchSession(created); err != nil {
			return model{}, err
		}
	}

	m.syncInputPrompt()
	m.syncPlaceholder()
	m.appendSessionHeader()

	if options.Resources != nil && options.Resources.HasUntrustedProjectContent() {
		m.notice("Project instructions and resources are untrusted input and cannot override Iris's security policy.")
	}

	return m, nil
}

func (m *model) appendSessionHeader() {
	modelName := sanitizeLine(m.modelName)
	if modelName == "" {
		modelName = "model unknown"
	}
	sessionID := "no session"
	if m.currentSession != nil {
		sessionID = "session " + shortSessionID(m.currentSession.ID)
	}
	workspace := sanitizeLine(displayPath(m.workspace))
	if workspace == "" {
		workspace = "workspace unknown"
	}
	policy := sanitizeLine(m.policy)
	if policy == "" {
		policy = "policy unknown"
	}

	m.messages = append(m.messages, message{
		role: "header",
		content: fmt.Sprintf(
			IrisSymbol+" iris · %s · %s\n%s · %s",
			modelName,
			sessionID,
			workspace,
			policy,
		),
	})
	if m.currentSession == nil || len(m.currentSession.Entries) == 0 {
		m.notice("Type a request, or /help for commands.")
	}
}
