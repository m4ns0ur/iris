package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/logger"
)

const InputHeight = 2

const (
	stateInput state = iota
	stateStreaming
)

var Logger = logger.Logger

type model struct {
	agent        *agent.Agent
	fullReason   *strings.Builder
	fullToolCall *strings.Builder
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
	role    string // "user" | "assistant" | "reasoning" | "error"
	content string
}

type chunkMsg struct {
	chunkType string // "reason" | "response"
	content   string
}

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

				m.fullReason.Reset()
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
			switch msg.chunkType {
			case "next":
				if m.fullReason.Len() > 0 {
					m.messages = append(m.messages, message{
						role:    "reasoning",
						content: m.fullReason.String(),
					})
					m.fullReason.Reset()
				}
				if m.fullToolCall.Len() > 0 {
					m.messages = append(m.messages, message{
						role:    "tool",
						content: m.fullToolCall.String(),
					})
					m.fullToolCall.Reset()
				}
				if m.fullResponse.Len() > 0 {
					m.messages = append(m.messages, message{
						role:    "assistant",
						content: m.fullResponse.String(),
					})
					m.fullResponse.Reset()
				}
			case "reason":
				m.fullReason.WriteString(msg.content)
			case "tool":
				m.fullToolCall.WriteString(msg.content)
			case "response":
				m.fullResponse.WriteString(msg.content)
			}
			return m, waitForStream(m.stream)

		case doneMsg:
			reason := m.fullReason.String()
			if reason != "" {
				m.messages = append(m.messages, message{
					role:    "reasoning",
					content: reason,
				})
			}

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

		case "reasoning":
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Reasoning> "),
			)

			reasonContent := lipgloss.NewStyle().
				Width(m.width).
				Italic(true).
				Foreground(lipgloss.Color("241")).
				Render(msg.content)
			sb.WriteString(reasonContent + "\n")

		case "tool":
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Tool> "),
			)

			toolContent := lipgloss.NewStyle().
				Width(m.width).
				Italic(true).
				Foreground(lipgloss.Color("241")).
				Render(msg.content)
			sb.WriteString(toolContent + "\n")

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
		if m.fullReason.Len() > 0 {
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Reasoning> "),
			)

			reasonContent := lipgloss.NewStyle().
				Width(m.width).
				Italic(true).
				Foreground(lipgloss.Color("241")).
				Render(m.fullReason.String())
			sb.WriteString(reasonContent + "\n")
		}

		if m.fullToolCall.Len() > 0 {
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Tool> "),
			)

			toolContent := lipgloss.NewStyle().
				Width(m.width).
				Italic(true).
				Foreground(lipgloss.Color("241")).
				Render(m.fullToolCall.String())
			sb.WriteString(toolContent + "\n")
		}

		if m.fullResponse.Len() > 0 {
			sb.WriteString(
				lipgloss.NewStyle().
					Foreground(lipgloss.Color("86")).
					Render("Iris> "),
			)

			content := lipgloss.NewStyle().
				Width(m.width).
				Render(m.fullResponse.String())
			sb.WriteString(content + "\n")
		}

		if m.fullReason.Len() == 0 && m.fullResponse.Len() == 0 && m.fullToolCall.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is thinking...\n")
		} else if m.fullReason.Len() > 0 && m.fullResponse.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is reasoning...\n")
		} else if m.fullToolCall.Len() > 0 && m.fullResponse.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is calling a tool...\n")
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
	spin.Spinner = spinner.Spinner{
		Frames: []string{
			"◎", "◉", // open and focus
			"◑", "◑", "◉", // glance left
			"◕", "◕", "◉", // glance right
			"◔", "◔", "◉", // look up
			"◒", "◒", "◉", // look down
			"◕", "⌒", "⌒", "◕", "◉", // wink
			"◕", "◔", "◑", "◒", "◈", "✺", "⊛", "◈", "◉", // dizzy spin
			"◕", "●", "◕", "◔", "◯", "—", "◯", "◔", "◕", "◉", "◎", // full blink
		},
		FPS: time.Second / 10,
	}
	spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return model{
		agent:        agent,
		fullReason:   &strings.Builder{},
		fullToolCall: &strings.Builder{},
		fullResponse: &strings.Builder{},
		state:        stateInput,
		inputText:    text,
		stream:       make(chan tea.Msg),
		spinner:      spin,
	}
}
