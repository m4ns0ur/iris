package tui

import (
	"context"
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
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
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
	InputBorder           = 2
	SpinnerHeight         = 1
	StatusHeight          = 1
	BottomHeight          = InputHeight + InputBorder + SpinnerHeight + StatusHeight
)

type state int

const (
	StateInput state = iota
	StateStreaming
)

type component int

const (
	UserPrompt component = iota
	User
	IrisPrompt
	Iris
	ReasoningPrompt
	Reasoning
	ToolPrompt
	ToolKey
	ToolValue
	ErrorPrompt
	Error
	NoticePrompt
	Notice
	Spinner
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
}

type model struct {
	agent               agent.Agent
	fullReason          *strings.Builder
	fullResponse        *strings.Builder
	toolName            string
	toolArgs            *strings.Builder
	previewPath         string
	previewContent      string
	previewCommand      string
	width               int
	height              int
	state               state
	inputText           textarea.Model
	stream              chan tea.Msg
	spinner             spinner.Model
	messages            []message
	viewport            viewport.Model
	ready               bool
	approvals           chan approvalRequestMsg
	pendingApproval     *approvalRequestMsg
	approvalTimeout     time.Duration
	canceling           bool
	agentErrored        bool
	lastTurnEmpty       bool
	compacting          bool
	totalTokens         int
	lifecycle           *lifecycle
	sessions            session.Manager
	sessionDefaults     session.CreateOptions
	currentSession      *session.Session
	sessionPersisted    int
	sessionDirty        bool
	usagePending        []provider.Usage
	summaryUsagePending provider.Usage
	lease               session.Lease
	recorder            *persist.Recorder
	recording           bool
	resources           *resource.Resources
	pendingSteering     []string
	pendingFollowUps    []string
}

type message struct {
	role           string
	content        string
	tag            string
	toolName       string
	toolArgs       string
	previewPath    string
	previewContent string
	previewCommand string
}

type agentEventMsg struct {
	event agent.Event
}

func (m *model) viewportHeight() int {
	return max(m.height-m.inputText.Height()-InputBorder-SpinnerHeight-StatusHeight, 1)
}

func (m *model) contentWidth() int {
	if m.width < 1 {
		return 80
	}
	return max(m.width-1, 2)
}

func (m *model) buildContent() string {
	var sb strings.Builder
	for _, msg := range m.messages {
		sb.WriteString(m.renderMessage(msg))
	}
	if m.state == StateStreaming {
		if m.fullReason.Len() > 0 {
			sb.WriteString(m.renderFor(ReasoningPrompt, "Reasoning> "))
			sb.WriteString(m.renderFor(Reasoning, m.fullReason.String()))
		}
		if m.fullResponse.Len() > 0 {
			sb.WriteString(m.renderFor(IrisPrompt, "Iris> "))
			sb.WriteString(m.renderFor(Iris, m.fullResponse.String()))
		}
		if m.toolName != "" || m.toolArgs.Len() > 0 {
			t := tree.Root(m.renderFor(ToolPrompt, m.toolName))
			if m.previewPath != "" {
				t.Child(m.renderFor(ToolKey, "Path: ") + m.renderFor(ToolValue, m.previewPath))
			}
			if m.previewContent != "" {
				t.Child(m.renderFor(ToolKey, "Content: ") + m.renderFor(ToolValue, m.previewContent))
			}
			if m.previewCommand != "" {
				t.Child(m.renderFor(ToolKey, "Command: ") + m.renderFor(ToolValue, m.previewCommand))
			}
			sb.WriteString(t.String() + "\n")
		}
	}
	return sb.String()
}

func (m *model) renderMessage(msg message) string {
	var sb strings.Builder
	switch msg.role {
	case "user":
		label := "You> "
		if msg.tag != "" {
			label = "You (" + msg.tag + ")> "
		}
		sb.WriteString(m.renderFor(UserPrompt, label))
		sb.WriteString(m.renderFor(User, msg.content))
	case "reasoning":
		sb.WriteString(m.renderFor(ReasoningPrompt, "Reasoning> "))
		sb.WriteString(m.renderFor(Reasoning, msg.content))
	case "assistant":
		sb.WriteString(m.renderFor(IrisPrompt, "Iris> "))
		sb.WriteString(m.renderFor(Iris, msg.content))
	case "tool":
		t := tree.Root(m.renderFor(ToolPrompt, msg.toolName))
		if msg.previewPath != "" {
			t.Child(m.renderFor(ToolKey, "Path: ") + m.renderFor(ToolValue, msg.previewPath))
		}
		if msg.previewContent != "" {
			t.Child(m.renderFor(ToolKey, "Content: ") + m.renderFor(ToolValue, msg.previewContent))
		}
		if msg.previewCommand != "" {
			t.Child(m.renderFor(ToolKey, "Command: ") + m.renderFor(ToolValue, msg.previewCommand))
		}
		sb.WriteString(t.String() + "\n")
	case "error":
		sb.WriteString(m.renderFor(ErrorPrompt, "Error> "))
		sb.WriteString(m.renderFor(Error, msg.content) + "\n")
	case "notice":
		sb.WriteString(m.renderFor(NoticePrompt, "Notice> "))
		sb.WriteString(m.renderFor(Notice, msg.content) + "\n")
	}
	return sb.String()
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

		m.inputText.SetWidth(max(m.width-4, 1))

		if !m.ready {
			m.viewport = viewport.New(
				viewport.WithWidth(m.contentWidth()),
				viewport.WithHeight(m.viewportHeight()),
			)
			m.viewport.SoftWrap = true
			m.viewport.SetContent(m.buildContent())
			m.viewport.GotoBottom()
			m.viewport.FillHeight = true
			m.viewport.KeyMap = viewport.KeyMap{
				PageDown: key.NewBinding(key.WithKeys("pgdown")),
				PageUp:   key.NewBinding(key.WithKeys("pgup")),
			}
			m.ready = true
		} else {
			m.viewport.SetWidth(m.contentWidth())
			m.viewport.SetHeight(m.viewportHeight())
			m.resizeLayout()
			m.viewport.GotoBottom()
		}

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
		m.resizeLayout()
		m.viewport.GotoBottom()

		// The policy has its own approval deadline. Mirror it in the UI so a
		// timed-out prompt disappears instead of blocking the next turn.
		return m, tea.Tick(m.approvalTimeout, func(time.Time) tea.Msg {
			return approvalTimeoutMsg{reply: msg.reply}
		})

	case approvalTimeoutMsg:
		if m.pendingApproval != nil && m.pendingApproval.reply == msg.reply {
			m.pendingApproval = nil
			m.resizeLayout()
			m.viewport.GotoBottom()
			return m, m.waitApproval()
		}
		return m, nil

	case agentEventMsg:
		return m.handleAgentEvent(msg.event)

	case instructionResultMsg:
		return m.handleInstructionResult(msg)

	case abortResultMsg:
		return m.handleAbortResult(msg)
	}

	if key, ok := msg.(tea.KeyPressMsg); ok && m.pendingApproval != nil {
		return m.handleApprovalKey(key)
	}

	switch m.state {
	case StateInput:
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "enter":
				input := strings.TrimSpace(m.inputText.Value())
				if input == "" {
					return m, nil
				}

				m.inputText.Reset()

				if strings.HasPrefix(input, "/") {
					return m.handleCommand(input)
				}

				m.messages = append(m.messages, message{
					role:    "user",
					content: input,
				})

				m.fullReason.Reset()
				m.fullResponse.Reset()
				m.toolName = ""
				m.toolArgs.Reset()
				m.previewPath = ""
				m.previewContent = ""
				m.previewCommand = ""
				m.canceling = false
				m.agentErrored = false
				m.lastTurnEmpty = false
				m.compacting = false
				m.totalTokens = 0
				m.pendingSteering = nil
				m.pendingFollowUps = nil
				m.inputText.Placeholder = "Type to steer Iris..."
				m.state = StateStreaming
				m.resizeLayout()
				m.viewport.GotoBottom()

				return m, tea.Batch(
					m.spinner.Tick,
					runAgentCommand(m.agent, input, m.done()),
				)

			case "ctrl+c":
				return m, m.quit()

			case "pgup", "ctrl+u", "pgdown", "ctrl+d":
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		}

		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)

		var taCmd tea.Cmd
		oldHeight := m.inputText.Height()

		m.inputText, taCmd = m.inputText.Update(msg)
		cmds = append(cmds, taCmd)

		if m.inputText.Height() != oldHeight {
			if m.inputText.Height() > InputHeightMax {
				m.inputText.SetHeight(InputHeightMax)
			}
			m.viewport.SetHeight(m.viewportHeight())
			m.viewport.GotoBottom()
		}

		return m, tea.Batch(cmds...)

	case StateStreaming:
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "ctrl+c":
				if m.canceling {
					return m, m.quit()
				}
				m.canceling = true
				m.resizeLayout()
				m.viewport.GotoBottom()
				return m, abortRunCommand(m.agent)
			case "enter":
				return m.submitStreamingInstruction(instructionSteer)
			case "alt+enter":
				return m.submitStreamingInstruction(instructionFollowUp)
			case "pgup", "ctrl+u":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil
			case "pgdown", "ctrl+d":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil
			}

		case tea.MouseWheelMsg:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd

		case spinner.TickMsg:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

		// Keep the prompt editable during a run so the user can type a
		// steering instruction without aborting first. Editing keys (including
		// shift+enter / ctrl+j for a newline) are handled by the textarea.
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)

		oldHeight := m.inputText.Height()
		var taCmd tea.Cmd
		m.inputText, taCmd = m.inputText.Update(msg)
		cmds = append(cmds, taCmd)

		if m.inputText.Height() != oldHeight {
			if m.inputText.Height() > InputHeightMax {
				m.inputText.SetHeight(InputHeightMax)
			}
			m.viewport.SetHeight(m.viewportHeight())
			m.viewport.GotoBottom()
		}

		return m, tea.Batch(cmds...)
	}

	return m, nil
}

func (m *model) View() tea.View {
	var v tea.View
	v.MouseMode = tea.MouseModeCellMotion

	if !m.ready {
		v.SetContent("Initializing Iris...")
		return v
	}

	v.SetContent(strings.Join([]string{
		m.viewport.View(),
		m.spinnerBar(),
		m.inputBox(),
		m.statusLine(),
	}, "\n"))
	return v
}

func (m *model) spinnerBar() string {
	w := m.contentWidth()

	if m.pendingApproval != nil {
		return m.approvalBar(w)
	}

	if m.state == StateStreaming {
		var color, label string
		switch {
		case m.canceling:
			color, label = ColorRedSoft, " canceling..."
		case m.compacting:
			color, label = ColorYellow, " Compacting context..."
		case m.fullReason.Len() > 0 && m.fullResponse.Len() == 0:
			color, label = ColorYellow, " Iris reasoning..."
		case m.toolName != "" && m.fullResponse.Len() == 0:
			color, label = ColorPurple, " Iris tooling..."
		default:
			color, label = ColorOrange, " Iris thinking..."
		}
		m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		left := m.spinner.View() + lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(label)

		if summary := m.pendingSummary(); summary != "" {
			left += lipgloss.NewStyle().
				Foreground(lipgloss.Color(ColorMuted)).
				Render(" · " + summary)
		}

		if !m.viewport.AtBottom() {
			hint := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorMuted)).Render("↓ pgdn")
			gap := w - lipgloss.Width(left) - lipgloss.Width(hint)
			if gap > 0 {
				left += strings.Repeat(" ", gap) + hint
			}
		}
		return lipgloss.NewStyle().Width(w).Render(left)
	}

	if !m.viewport.AtBottom() {
		left := lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorMuted)).
			Render("pgup/pgdn: scroll")

		right := lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorMuted)).
			Render(
				fmt.Sprintf(
					"↓ %02.0f%%",
					m.viewport.ScrollPercent()*100,
				),
			)

		gap := max(
			w-lipgloss.Width(left)-lipgloss.Width(right),
			1,
		)

		return left + strings.Repeat(" ", gap) + right
	}

	return lipgloss.NewStyle().Width(w).Render("")
}

func (m *model) inputBox() string {
	body := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, true, false).BorderForeground(lipgloss.Color(ColorBorderFocus)).Padding(0, 1).Width(m.width).Render(m.inputText.View())
	b := lipgloss.NormalBorder()
	bs := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorBorderFocus))

	var status string
	if n := m.inputText.ScrollYOffset(); n > 0 {
		status = bs.Render(b.Top) + lipgloss.NewStyle().Foreground(lipgloss.Color(ColorDim)).Render(fmt.Sprintf(" ↑ %d ", n)) + bs.Render(b.Top)
	}

	inner := m.width
	fillRight := 1
	fillLeft := max(inner-lipgloss.Width(status)-fillRight, 0)
	top := bs.Render(strings.Repeat(b.Top, fillLeft)) + status + bs.Render(strings.Repeat(b.Top, fillRight))
	return top + "\n" + body
}

func (m *model) statusLine() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorDim))
	help := style.Render("enter: send · shift+enter: newline · /help: commands · ctrl+c: quit")
	if m.state == StateStreaming {
		help = style.Render("enter: steer · alt+enter: follow-up · ctrl+c: abort")
	}
	if m.canceling {
		help = style.Render("canceling… · ctrl+c: quit")
	}
	if m.compacting && !m.canceling {
		help = style.Render("compacting context… · ctrl+c: abort")
	}
	if m.pendingApproval != nil {
		help = style.Render("y: allow once · n: deny · ctrl+c: abort")
	}
	pctText := fmt.Sprintf(" %3.0f%% ", m.viewport.ScrollPercent()*100)
	if m.totalTokens > 0 {
		pctText = fmt.Sprintf(" %3.0f%% · %d tok ", m.viewport.ScrollPercent()*100, m.totalTokens)
	}
	pct := style.Render(pctText)
	gap := max(m.width-lipgloss.Width(help)-lipgloss.Width(pct), 1)
	return help + strings.Repeat(" ", gap) + pct
}

func (m *model) renderMarkdown(content string) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(m.contentWidth()-2, 2)),
		glamour.WithStylePath("dark"),
	)
	if err != nil {
		return content
	}

	out, err := r.Render(content)
	if err != nil {
		return content
	}

	return out
}

func (m *model) resizeLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}

	m.inputText.SetWidth(m.width - 4)

	inputHeight := lipgloss.Height(m.inputBox())
	vpHeight := max(m.height-inputHeight-SpinnerHeight-StatusHeight, 1)
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(vpHeight)
	m.viewport.SetContent(m.buildContent())
	if m.viewport.AtBottom() {
		m.viewport.GotoBottom()
	}
}

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
	ta.Placeholder = "Ask Iris..."
	ta.SetHeight(InputHeight)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.DynamicHeight = true
	ta.MinHeight = InputHeight
	ta.MaxHeight = InputHeightMax
	ta.MaxContentHeight = InputContentHeightMax
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	ta.Focus()

	spin := spinner.New()
	spin.Spinner = spinner.Spinner{
		Frames: SpinnerFrames,
		FPS:    SpinnerFPS,
	}

	stream := make(chan tea.Msg)
	lifecycle := newLifecycle()
	instance := options.Agent

	approvalTimeout := 5 * time.Minute
	var approvals chan approvalRequestMsg
	if instance != nil {
		if configured := instance.ApprovalTimeout(); configured > 0 {
			approvalTimeout = configured
		}
		if instance.Approver() == nil {
			approver := newTUIApprover()
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

	m := model{
		agent:           instance,
		fullReason:      &strings.Builder{},
		fullResponse:    &strings.Builder{},
		toolArgs:        &strings.Builder{},
		state:           StateInput,
		inputText:       ta,
		stream:          stream,
		spinner:         spin,
		approvals:       approvals,
		approvalTimeout: approvalTimeout,
		lifecycle:       lifecycle,
		sessions:        options.Sessions,
		sessionDefaults: options.SessionDefaults,
		resources:       options.Resources,
		messages:        initialMessages,
	}

	if options.Sessions != nil {
		created, err := options.Sessions.Create(options.SessionDefaults)
		if err != nil {
			return model{}, err
		}
		if err := m.switchSession(created); err != nil {
			return model{}, err
		}
		m.notice("Session " + created.ID)
	}

	if options.Resources != nil && options.Resources.HasUntrustedProjectContent() {
		m.notice("Project instructions and resources are untrusted input and cannot override Iris's security policy.")
	}

	return m, nil
}

func (m *model) handleAgentEvent(event agent.Event) (tea.Model, tea.Cmd) {
	rearmApproval := false

	switch e := event.(type) {
	case agent.AgentStarted:
		// State is already streaming; no action.

	case agent.TurnStarted:
		m.flushPartialStream()

	case agent.TurnRetried:
		// The failed attempt's partial output is not part of the retried turn.
		m.fullReason.Reset()
		m.fullResponse.Reset()
		m.toolName = ""
		m.toolArgs.Reset()
		m.previewPath = ""
		m.previewContent = ""
		m.previewCommand = ""
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
		if e.ToolName != "" {
			m.toolName = e.ToolName
		}
		if e.ArgumentsDelta != "" {
			m.toolArgs.WriteString(e.ArgumentsDelta)
			m.updateToolPreview(m.toolArgs.String())
		}

	case agent.ToolStarted:
		if e.ToolName != "" {
			m.toolName = e.ToolName
		}
		m.applyToolArgs(e.Args)

	case agent.ToolCompleted:
		// Tool results are fed back to the model; the preview is already shown.

	case agent.ToolFailed:
		Logger.Debug("Tool failed", "tool", e.ToolName, "error", e.Message)

	case agent.Usage:
		m.totalTokens = e.TotalTokens
		if !m.recording {
			m.usagePending = append(m.usagePending, provider.Usage{
				PromptTokens:     e.PromptTokens,
				CompletionTokens: e.CompletionTokens,
				TotalTokens:      e.TotalTokens,
			})
		}

	case agent.ContextCompactionStarted:
		m.compacting = true

	case agent.ContextCompactionFinished:
		m.compacting = false
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
		m.summaryUsagePending.PromptTokens += e.SummaryPromptTokens
		m.summaryUsagePending.CompletionTokens += e.SummaryCompletionTokens
		m.summaryUsagePending.TotalTokens += e.SummaryPromptTokens + e.SummaryCompletionTokens

	case agent.QueueUpdated:
		m.pendingSteering = append([]string(nil), e.Steering...)
		m.pendingFollowUps = append([]string(nil), e.FollowUps...)

	case agent.InstructionDelivered:
		tag := "steer"
		if e.Kind == agent.InstructionFollowUp {
			tag = "follow-up"
		}
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
			m.messages = append(m.messages, message{role: "notice", content: "Canceled."})
		} else if e.Err != "" && !m.agentErrored {
			m.messages = append(m.messages, message{role: "error", content: e.Err})
		} else if m.lastTurnEmpty {
			m.messages = append(m.messages, message{role: "notice", content: "Iris returned an empty response."})
		}
		m.lastTurnEmpty = false

		if e.Summary != nil {
			outcome := "Task completed"
			if e.Canceled {
				outcome = "Run canceled"
			} else if e.Err != "" {
				outcome = "Run failed"
			}
			m.notice(e.Summary.Format(outcome))
		}

		m.canceling = false
		m.compacting = false
		m.pendingSteering = nil
		m.pendingFollowUps = nil
		m.inputText.Placeholder = "Ask Iris..."
		m.inputText.Focus()
		m.state = StateInput
		m.persistSession()
	}

	m.resizeLayout()
	m.viewport.GotoBottom()

	cmds := []tea.Cmd{waitForStream(m.stream)}
	if rearmApproval {
		cmds = append(cmds, m.waitApproval())
	}
	return m, tea.Batch(cmds...)
}

// submitStreamingInstruction turns the current input into a steering message
// or follow-up without leaving the streaming state. The input stays editable
// while the run is active, so this mirrors the idle Enter behavior.
func (m *model) submitStreamingInstruction(kind instructionKind) (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.inputText.Value())
	if input == "" {
		return m, nil
	}

	if strings.HasPrefix(input, "/") {
		switch strings.Fields(input)[0] {
		case "/clear-queue", "/help", "/?":
			m.inputText.Reset()
			m.resizeLayout()
			return m.handleCommand(input)
		default:
			m.notice("Commands are unavailable while Iris is running. Press ctrl+c to abort first.")
			m.resizeLayout()
			m.viewport.GotoBottom()
			return m, nil
		}
	}

	m.inputText.Reset()

	if kind == instructionFollowUp {
		m.pendingFollowUps = append(m.pendingFollowUps, input)
		m.notice("Follow-up queued until the current run stops.")
	} else {
		m.pendingSteering = append(m.pendingSteering, input)
		m.notice("Steering queued for the next turn.")
	}

	m.resizeLayout()
	m.viewport.GotoBottom()

	return m, submitInstructionCommand(m.agent, kind, input)
}

// handleInstructionResult restores text when the run ended before an
// instruction could be accepted.
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

	m.resizeLayout()
	m.viewport.GotoBottom()
	return m, nil
}

// handleAbortResult restores instructions recovered by the atomic abort.
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

	m.resizeLayout()
	m.viewport.GotoBottom()
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
	parts := make([]string, 0, 2)
	if count := len(m.pendingSteering); count > 0 {
		parts = append(parts, fmt.Sprintf("steering: %d", count))
	}
	if count := len(m.pendingFollowUps); count > 0 {
		parts = append(parts, fmt.Sprintf("follow-up: %d", count))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + " queued"
}

func (m *model) updateToolPreview(argsJSON string) {
	if path, ok := partialJSONField(argsJSON, "path"); ok {
		m.previewPath = path
	}
	if content, ok := partialJSONField(argsJSON, "content"); ok {
		m.previewContent = content
	}
	if command, ok := partialJSONField(argsJSON, "command"); ok {
		m.previewCommand = command
	}
}

func (m *model) applyToolArgs(args map[string]any) {
	if path, ok := args["path"].(string); ok {
		m.previewPath = path
	}
	if content, ok := args["content"].(string); ok {
		m.previewContent = content
	}
	if command, ok := args["command"].(string); ok {
		m.previewCommand = command
	}
}

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

// flushPartialStream persists partial output before returning to input.
func (m *model) flushPartialStream() {
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

	if m.toolName != "" || m.toolArgs.Len() > 0 {
		m.messages = append(m.messages, message{
			role:           "tool",
			toolName:       m.toolName,
			toolArgs:       m.toolArgs.String(),
			previewPath:    m.previewPath,
			previewContent: m.previewContent,
			previewCommand: m.previewCommand,
		})
		m.toolName = ""
		m.toolArgs.Reset()
		m.previewPath = ""
		m.previewContent = ""
		m.previewCommand = ""
	}
}
