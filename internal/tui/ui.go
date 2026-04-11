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

var Logger = logger.Logger

type model struct {
	agent         *agent.Agent
	fullResponse  *strings.Builder
	streaming     bool
	inAgenticLoop bool

	width    int
	state    state
	input    textarea.Model
	stream   chan tea.Msg
	spinner  spinner.Model
	messages []message
}

type state int

type message struct {
	role    string // "user" | "assistant"
	content string
}

type chunkMsg string

type doneMsg struct{}

type errMsg error

func (m model) Init() tea.Cmd {
	return m.input.Focus()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.SetWidth(msg.Width - 2)
	}

	switch m.state {

	case stateInput:
		switch msg := msg.(type) {

		case tea.KeyPressMsg:
			switch msg.String() {

			case "shift+enter":
				m.input.InsertRune('\n')
				// auto-resize AFTER update
				m.input.SetHeight(min(10, strings.Count(m.input.Value(), "\n")+1))

			case "enter":
				input := m.input.Value()
				if strings.TrimSpace(input) == "" {
					return m, nil
				}

				m.input.SetHeight(2)
				m.input.Reset()

				m.messages = append(m.messages, message{
					role:    "user",
					content: input,
				})

				m.fullResponse.Reset()
				m.streaming = true
				m.state = stateStreaming

				if m.inAgenticLoop {
					// Use agentic loop command
					return m, tea.Batch(
						m.spinner.Tick,
						runAgenticLoopCommand(m.agent, input, m.stream),
						waitForStream(m.stream),
					)
				} else {
					// Use simple streaming command
					return m, tea.Batch(
						m.spinner.Tick,
						streamCommand(m.agent, input, m.stream),
						waitForStream(m.stream),
					)
				}

			case "ctrl+c":
				return m, tea.Quit
			}
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
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
			if m.inAgenticLoop {
				// During agentic loop, accumulate chunks for each step
				m.fullResponse.WriteString(string(msg))
			} else {
				// Simple streaming mode
				m.streaming = false
				m.fullResponse.WriteString(string(msg))
			}
			return m, waitForStream(m.stream)

		case doneMsg:
			resp := m.fullResponse.String()
			m.messages = append(m.messages, message{
				role:    "assistant",
				content: resp,
			})

			if m.inAgenticLoop {
				Logger.Info("Agentic loop completed", "response", resp)
			} else {
				Logger.Info("Streamed full response", "response", resp)
			}

			m.state = stateInput
			return m, nil

		case errMsg:
			m.messages = append(m.messages, message{
				role:    "assistant",
				content: "Error: " + msg.Error(),
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
			// Show error message if there was an error
			if strings.HasPrefix(msg.content, "Error: ") {
				errorMsg := lipgloss.NewStyle().
					Foreground(lipgloss.Color("196")).
					Bold(true).
					MarginTop(2).
					MarginBottom(1).
					Render(msg.content)
				sb.WriteString(errorMsg + "\n")
			} else {
				content := lipgloss.NewStyle().
					Width(m.width).
					Render(msg.content)

				sb.WriteString(
					lipgloss.NewStyle().
						Foreground(lipgloss.Color("86")).
						Render("Iris> "),
				)
				sb.WriteString(content + "\n")
			}
		}
	}

	// Render current streaming response (live)
	if m.state == stateStreaming {
		if m.inAgenticLoop {
			// During agentic loop, show progress
			sb.WriteString("\n" + m.spinner.View() + " Agent is working...\n")
		} else if m.streaming && m.fullResponse.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is thinking...\n")
		} else {
			if strings.HasPrefix(m.fullResponse.String(), "Error: ") {
				errorMsg := lipgloss.NewStyle().
					Foreground(lipgloss.Color("196")).
					Bold(true).
					MarginTop(2).
					MarginBottom(1).
					Render(m.fullResponse.String())
				sb.WriteString(errorMsg + "\n")
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
	}

	// Always show input at bottom
	sb.WriteString("\n" + m.input.View() + "\n")

	return tea.NewView(sb.String())
}

func waitForStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func Run(agent *agent.Agent) error {
	m := initialModel(agent)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func initialModel(agent *agent.Agent) model {
	ta := textarea.New()
	ta.Placeholder = "Ask Iris..."
	ta.Focus()
	ta.SetHeight(2)
	ta.ShowLineNumbers = false

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return model{
		agent:        agent,
		fullResponse: &strings.Builder{},
		state:        stateInput,
		input:        ta,
		stream:       make(chan tea.Msg),
		spinner:      sp,
		streaming:    true,
	}
}
