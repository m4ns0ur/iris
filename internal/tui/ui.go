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
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/policy"
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

type model struct {
	agent           *agent.Agent
	fresh           bool
	fullReason      *strings.Builder
	fullResponse    *strings.Builder
	toolName        string
	toolArgs        *strings.Builder
	previewPath     string
	previewContent  string
	previewCommand  string
	width           int
	height          int
	state           state
	inputText       textarea.Model
	stream          chan tea.Msg
	spinner         spinner.Model
	messages        []message
	viewport        viewport.Model
	ready           bool
	approvals       chan approvalRequestMsg
	pendingApproval *approvalRequestMsg
	approvalTimeout time.Duration
	canceling       bool
	lifecycle       *lifecycle
}

type toolCallDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type message struct {
	role           string
	content        string
	toolName       string
	toolArgs       string
	previewPath    string
	previewContent string
	previewCommand string
}

type chunkMsg struct {
	chunkType string
	content   string
}

type doneMsg struct{}

type canceledMsg struct{}

type errMsg struct{ error }

type fatalErrMsg struct{ error }

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
		if m.toolArgs.Len() > 0 {
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
		sb.WriteString(m.renderFor(UserPrompt, "You> "))
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
	cmds := []tea.Cmd{m.inputText.Focus()}
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
			case msg.reply <- policy.ApprovalDecision{
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
				m.state = StateStreaming
				m.resizeLayout()
				m.viewport.GotoBottom()

				if m.fresh {
					m.fresh = false
					return m, tea.Batch(
						m.spinner.Tick,
						runFreshAgenticLoopCommand(m.agent, input, m.stream, m.done()),
						waitForStream(m.stream),
					)
				} else {
					return m, tea.Batch(
						m.spinner.Tick,
						runAgenticLoopCommand(m.agent, input, m.stream, m.done()),
						waitForStream(m.stream),
					)
				}

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
				if m.agent != nil {
					m.agent.Cancel()
				}
				m.resizeLayout()
				m.viewport.GotoBottom()
				return m, nil
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

		case chunkMsg:
			switch msg.chunkType {
			case "next":
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
				if m.toolArgs.Len() > 0 {
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
			case "reason":
				m.fullReason.WriteString(msg.content)
			case "tool":
				var delta toolCallDelta
				if err := json.Unmarshal([]byte(msg.content), &delta); err != nil {
					break
				}
				if delta.Name != "" {
					m.toolName = delta.Name
				}
				if delta.Arguments != "" {
					m.toolArgs.WriteString(delta.Arguments)
				}
				toolArgs := m.toolArgs.String()
				if path, ok := partialJSONField(toolArgs, "path"); ok {
					m.previewPath = path
				}
				if content, ok := partialJSONField(toolArgs, "content"); ok {
					m.previewContent = content
				}
				if command, ok := partialJSONField(toolArgs, "command"); ok {
					m.previewCommand = command
				}
			case "response":
				m.fullResponse.WriteString(msg.content)
			}

			m.resizeLayout()
			m.viewport.GotoBottom()
			cmds = append(cmds, waitForStream(m.stream))
			return m, tea.Batch(cmds...)

		case doneMsg:
			rearm := false
			if m.pendingApproval != nil {
				m.resolveApproval(policy.ApprovalDecision{
					Allow:  false,
					Reason: "agent finished before the approval was answered",
				})
				rearm = true
			}
			if m.fullReason.Len() > 0 {
				m.messages = append(m.messages, message{
					role:    "reasoning",
					content: m.fullReason.String(),
				})
				m.fullReason.Reset()
			}
			resp := m.fullResponse.String()
			m.fullResponse.Reset()
			m.messages = append(m.messages, message{role: "assistant", content: resp})
			Logger.Debug("Agentic loop completed", "response", resp)
			Logger.Debug(">>>>>", "messages", fmt.Sprintf("%+v", m.messages))
			m.canceling = false
			m.state = StateInput
			m.resizeLayout()
			m.viewport.GotoBottom()
			if rearm {
				return m, m.waitApproval()
			}
			return m, nil

		case canceledMsg:
			rearm := false
			if m.pendingApproval != nil {
				m.resolveApproval(policy.ApprovalDecision{
					Allow:  false,
					Reason: "run canceled",
				})
				rearm = true
			}

			m.flushPartialStream()
			m.messages = append(m.messages, message{role: "notice", content: "Canceled."})
			m.canceling = false
			m.state = StateInput
			m.resizeLayout()
			m.viewport.GotoBottom()

			if rearm {
				return m, m.waitApproval()
			}
			return m, nil

		case errMsg:
			m.messages = append(m.messages, message{role: "error", content: msg.Error()})
			m.resizeLayout()
			m.viewport.GotoBottom()
			return m, waitForStream(m.stream)

		case fatalErrMsg:
			rearm := false
			if m.pendingApproval != nil {
				m.resolveApproval(policy.ApprovalDecision{
					Allow:  false,
					Reason: "agent failed before the approval was answered",
				})
				rearm = true
			}
			m.flushPartialStream()
			m.messages = append(m.messages, message{role: "error", content: msg.Error()})
			m.canceling = false
			m.state = StateInput
			m.resizeLayout()
			m.viewport.GotoBottom()
			if rearm {
				return m, m.waitApproval()
			}
			return m, nil
		}

		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
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
		case m.fullReason.Len() > 0 && m.fullResponse.Len() == 0:
			color, label = ColorYellow, " Iris reasoning..."
		case m.toolArgs.Len() > 0 && m.fullResponse.Len() == 0:
			color, label = ColorPurple, " Iris tooling..."
		default:
			color, label = ColorOrange, " Iris thinking..."
		}
		m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		left := m.spinner.View() + lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(label)

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
	help := style.Render("enter: send · shift+enter: newline · pgup/pgdn: scroll · ctrl+c: quit")
	if m.state == StateStreaming {
		help = style.Render("pgup/pgdn: scroll · ctrl+c: cancel")
	}
	if m.canceling {
		help = style.Render("canceling… · ctrl+c: quit")
	}
	if m.pendingApproval != nil {
		help = style.Render("y: allow once · n: deny · ctrl+c: cancel")
	}
	pct := style.Render(fmt.Sprintf(" %3.0f%% ", m.viewport.ScrollPercent()*100))
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

func Run(ctx context.Context, agent *agent.Agent, pol *policy.Policy) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m := initialModel(agent, pol)
	prg := tea.NewProgram(&m, tea.WithContext(ctx))
	_, err := prg.Run()

	// Unblock any agent callback that is trying to deliver a message after the
	// program has stopped. cmd calls Agent.Close next, which cancels the run
	// and waits for cleanup.
	m.closeDone()

	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

func initialModel(agent *agent.Agent, pol *policy.Policy) model {
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

	var approvals chan approvalRequestMsg
	approvalTimeout := 5 * time.Minute
	if pol != nil {
		if configured := pol.ShellOptions().ApprovalTimeout; configured > 0 {
			approvalTimeout = configured
		}
		if pol.Approver() == nil {
			approver := newTUIApprover()
			pol.SetApprover(approver)
			approvals = approver.requests
		}
	}

	return model{
		agent:           agent,
		fresh:           true,
		fullReason:      &strings.Builder{},
		fullResponse:    &strings.Builder{},
		toolArgs:        &strings.Builder{},
		state:           StateInput,
		inputText:       ta,
		stream:          make(chan tea.Msg),
		spinner:         spin,
		approvals:       approvals,
		approvalTimeout: approvalTimeout,
		lifecycle:       newLifecycle(),
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

	if m.toolArgs.Len() > 0 {
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
