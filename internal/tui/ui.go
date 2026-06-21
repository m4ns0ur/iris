package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/logger"
)

const (
	InputHeight    = 1
	InputHeightMax = 14
	InputBorder    = 2
	SpinnerHeight  = 1
	StatusHeight   = 1
	BottomHeight   = InputHeight + InputBorder + SpinnerHeight + StatusHeight
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
	Spinner
)

var Logger = logger.Logger

type model struct {
	agent          *agent.Agent
	fullReason     *strings.Builder
	fullResponse   *strings.Builder
	toolName       string
	toolArgs       *strings.Builder
	previewPath    string
	previewContent string
	previewCommand string
	width          int
	height         int
	state          state
	inputText      textarea.Model
	stream         chan tea.Msg
	spinner        spinner.Model
	messages       []message
	viewport       viewport.Model
	ready          bool
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

type errMsg struct{ error }

type fatalErrMsg struct{ error }

func (m model) viewportHeight() int {
	return max(m.height-m.inputText.Height()-InputBorder-SpinnerHeight-StatusHeight, 1)
}

func (m model) contentWidth() int {
	if m.width < 1 {
		return 80
	}
	return max(m.width-1, 20)
}

func (m model) buildContent() string {
	var sb strings.Builder
	for _, msg := range m.messages {
		sb.WriteString(m.renderMessage(msg))
	}
	if m.state == StateStreaming {
		if m.fullReason.Len() > 0 {
			sb.WriteString(m.renderFor(ReasoningPrompt, "Reasoning> "))
			sb.WriteString(m.renderFor(Reasoning, m.fullReason.String()) + "\n")
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
		if m.fullResponse.Len() > 0 {
			sb.WriteString(m.renderFor(IrisPrompt, "Iris> "))
			sb.WriteString(m.renderFor(Iris, m.fullResponse.String()))
		}
	}
	return sb.String()
}

func (m model) renderMessage(msg message) string {
	var sb strings.Builder
	switch msg.role {
	case "user":
		sb.WriteString(m.renderFor(UserPrompt, "You> "))
		sb.WriteString(m.renderFor(User, msg.content))
	case "assistant":
		sb.WriteString(m.renderFor(IrisPrompt, "Iris> "))
		sb.WriteString(m.renderFor(Iris, msg.content))
	case "reasoning":
		sb.WriteString(m.renderFor(ReasoningPrompt, "Reasoning> "))
		sb.WriteString(m.renderFor(Reasoning, msg.content) + "\n")
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
	}
	return sb.String()
}

func (m *model) syncViewport() {
	atBottom := m.viewport.AtBottom()

	m.viewport.SetWidth(m.contentWidth())
	m.viewport.SetContent(m.buildContent())

	if atBottom {
		m.viewport.GotoBottom()
	}
}

func (m model) Init() tea.Cmd {
	return m.inputText.Focus()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.viewport.SetContent(m.buildContent())
			m.viewport.GotoBottom()
			m.ready = true
		} else {
			m.viewport.SetWidth(m.contentWidth())
			m.viewport.SetHeight(m.viewportHeight())
			m.syncViewport()
		}
	}

	switch m.state {
	case StateInput:
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "shift+enter":
				m.inputText.InsertRune('\n')

				if m.inputText.Height() < InputHeightMax {
					m.inputText.SetHeight(m.inputText.Height() + 1)
				}

				m.viewport.SetHeight(m.viewportHeight())
				return m, nil

			case "enter":
				input := m.inputText.Value()
				if strings.TrimSpace(input) == "" {
					return m, nil
				}

				m.inputText.SetHeight(InputHeight)
				m.inputText.Reset()
				m.viewport.SetHeight(m.viewportHeight())

				m.messages = append(m.messages, message{
					role:    "user",
					content: input,
				})

				m.fullReason.Reset()
				m.fullResponse.Reset()
				m.state = StateStreaming
				m.syncViewport()

				return m, tea.Batch(
					m.spinner.Tick,
					runAgenticLoopCommand(m.agent, input, m.stream),
					waitForStream(m.stream),
				)

			case "ctrl+c":
				return m, tea.Quit

			case "pgup", "ctrl+u":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil

			case "pgdown", "ctrl+d":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil
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
		}

		return m, tea.Batch(cmds...)

	case StateStreaming:
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "pgup", "ctrl+u":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil
			case "pgdown", "ctrl+d":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil
			}

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

			m.syncViewport()
			cmds = append(cmds, waitForStream(m.stream))
			return m, tea.Batch(cmds...)

		case doneMsg:
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
			m.state = StateInput
			m.syncViewport()
			return m, nil

		case errMsg:
			m.messages = append(m.messages, message{role: "error", content: msg.Error()})
			m.syncViewport()
			return m, waitForStream(m.stream)

		case fatalErrMsg:
			m.messages = append(m.messages, message{role: "error", content: msg.Error()})
			m.state = StateInput
			m.syncViewport()
			return m, nil
		}

		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
		return m, tea.Batch(cmds...)
	}

	return m, nil
}

func (m model) View() tea.View {
	var v tea.View
	v.AltScreen = true
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

func (m model) spinnerBar() string {
	w := m.contentWidth()

	if m.state == StateStreaming {
		var color, label string
		switch {
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

func (m model) inputBox() string {
	w := m.contentWidth()
	borderColor := lipgloss.Color(ColorBorder)
	if m.state == StateInput {
		borderColor = lipgloss.Color(ColorBorderFocus)
	}

	bc := lipgloss.NewStyle().Foreground(borderColor)
	innerW := w - 2

	fillW := max(innerW, 0)

	indicator := ""

	if m.inputText.Height() >= InputHeightMax {
		if hidden := m.hiddenInputLines(); hidden > 0 {
			indicator = fmt.Sprintf(" ↑ %d", hidden)
		}
	}

	indicatorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorOrange))

	indicatorW := lipgloss.Width(indicator)

	topBar := bc.Render("╭") +
		bc.Render(strings.Repeat("─", max(fillW-indicatorW, 0))) +
		indicatorStyle.Render(indicator) +
		bc.Render("╮")

	bottomBar := bc.Render("╰" + strings.Repeat("─", innerW) + "╯")

	bodyStyle := lipgloss.NewStyle().Foreground(borderColor)
	taView := m.inputText.View()
	lines := strings.Split(taView, "\n")

	var body strings.Builder
	for _, line := range lines {
		body.WriteString(bodyStyle.Render("│") + " " + line + "\n")
	}

	bodyStr := strings.TrimSuffix(body.String(), "\n")

	bodyLines := strings.Split(bodyStr, "\n")
	var finalBody strings.Builder
	for i, line := range bodyLines {
		visibleW := lipgloss.Width(line)
		pad := max(innerW-visibleW, 0)
		finalBody.WriteString(line + strings.Repeat(" ", pad) + bodyStyle.Render("│"))
		if i < len(bodyLines)-1 {
			finalBody.WriteString("\n")
		}
	}

	return strings.Join([]string{topBar, finalBody.String(), bottomBar}, "\n")
}

func (m model) statusLine() string {
	style := lipgloss.NewStyle().Width(m.contentWidth()).Foreground(lipgloss.Color(ColorDim))
	if m.state == StateStreaming {
		return style.Render("ctrl+c: quit   pgup/pgdn: scroll")
	}
	return style.Render("enter: send   shift+enter: newline   pgup/pgdn: scroll   ctrl+c: quit")
}

func (m model) renderMarkdown(content string) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(m.contentWidth()-2, 20)),
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

func (m model) hiddenInputLines() int {
	lines := strings.Count(m.inputText.Value(), "\n") + 1
	return max(lines-m.inputText.Height(), 0)
}

func waitForStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func Run(agent *agent.Agent) error {
	m := initialModel(agent)
	prg := tea.NewProgram(m)
	_, err := prg.Run()
	return err
}

func initialModel(agent *agent.Agent) model {
	ta := textarea.New()
	ta.Placeholder = "Ask Iris..."
	ta.SetHeight(InputHeight)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()

	spin := spinner.New()
	spin.Spinner = spinner.Spinner{
		Frames: SpinnerFrames,
		FPS:    time.Second / 10,
	}

	return model{
		agent:        agent,
		fullReason:   &strings.Builder{},
		fullResponse: &strings.Builder{},
		toolArgs:     &strings.Builder{},
		state:        StateInput,
		inputText:    ta,
		stream:       make(chan tea.Msg),
		spinner:      spin,
	}
}
