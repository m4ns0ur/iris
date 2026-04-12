package tui

import (
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/logger"
)

const (
	stateInput state = iota
	stateStreaming
)

const InputHeight = 2

var Logger = logger.Logger

type model struct {
	agent        *agent.Agent
	fullResponse *strings.Builder
	width        int
	state        state
	inputText    textarea.Model
	stream       chan tea.Msg
	spinner      spinner.Model
	messages     []message
}

type state int

type message struct {
	role    string // "user" | "assistant" | "error"
	content string
}

type chunkMsg string

type doneMsg struct{}

type errMsg struct {
	error
}

type fatalErrMsg struct {
	error
}

func (m model) Init() tea.Cmd {
	return m.inputText.Focus()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.inputText.SetWidth(msg.Width - 2)
	}

	switch m.state {
	case stateInput:
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "shift+enter":
				m.inputText.InsertRune('\n')
				m.inputText.SetHeight(m.inputText.Height() + 1)

			case "enter":
				input := m.inputText.Value()
				if strings.TrimSpace(input) == "" {
					return m, nil
				}

				m.inputText.SetHeight(InputHeight)
				m.inputText.Reset()

				m.messages = append(m.messages, message{
					role:    "user",
					content: input,
				})

				m.fullResponse.Reset()
				m.state = stateStreaming

				return m, tea.Batch(
					m.spinner.Tick,
					runAgenticLoopCommand(m.agent, input, m.stream),
					waitForStream(m.stream),
				)

			case "ctrl+c":
				return m, tea.Quit
			}
		}

		var cmd tea.Cmd
		m.inputText, cmd = m.inputText.Update(msg)
		return m, cmd

	case stateStreaming:
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			}

		case spinner.TickMsg:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd

		case chunkMsg:
			m.fullResponse.WriteString(string(msg))
			return m, waitForStream(m.stream)

		case doneMsg:
			resp := m.fullResponse.String()
			m.messages = append(m.messages, message{
				role:    "assistant",
				content: resp,
			})

			Logger.Debug("Agentic loop completed", "response", resp)

			m.state = stateInput
			return m, nil

		case errMsg:
			m.messages = append(m.messages, message{
				role:    "error",
				content: msg.Error(),
			})
			return m, waitForStream(m.stream)

		case fatalErrMsg:
			m.messages = append(m.messages, message{
				role:    "error",
				content: msg.Error(),
			})
			m.state = stateInput
			return m, nil
		}
	}

	return m, nil
}

func (m model) View() tea.View {
	var sb strings.Builder

	// Render history
	for _, msg := range m.messages {
		switch msg.role {
		case "user":
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("212")).
					Render("You> "),
			)
			sb.WriteString(msg.content + "\n")

		case "assistant":
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Iris> "),
			)
			sb.WriteString(
				lipgloss.NewStyle().
					Width(m.width).
					Render(msg.content) + "\n",
			)

		case "error":
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("196")).
					Bold(true).
					MarginTop(2).
					MarginBottom(1).
					Render(msg.content) + "\n",
			)
		}
	}

	// Render current streaming response (live)
	if m.state == stateStreaming {
		if m.fullResponse.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is thinking...\n")
		} else {
			content := lipgloss.NewStyle().
				Width(m.width).
				Render(m.fullResponse.String())

			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Iris> "),
			)
			sb.WriteString(content + "\n")
		}
	}

	// Always show input text area at bottom
	sb.WriteString("\n" + m.inputText.View() + "\n")

	return tea.NewView(sb.String())
}

func waitForStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func Run(agent *agent.Agent) error {
	model := initialModel(agent)
	prg := tea.NewProgram(model)
	_, err := prg.Run()
	return err
}

func initialModel(agent *agent.Agent) model {
	text := textarea.New()
	text.Placeholder = "Ask Iris..."
	text.Focus()
	text.SetHeight(InputHeight)
	text.ShowLineNumbers = false

	spin := spinner.New()
	spin.Spinner = spinner.Meter
	spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return model{
		agent:        agent,
		fullResponse: &strings.Builder{},
		state:        stateInput,
		inputText:    text,
		stream:       make(chan tea.Msg),
		spinner:      spin,
	}
}
