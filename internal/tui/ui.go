package tui

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/logger"
)

const InputHeight = 2

type state int

const (
	stateInput state = iota
	stateStreaming
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
	state          state
	inputText      textarea.Model
	stream         chan tea.Msg
	spinner        spinner.Model
	messages       []message
}

type toolCallDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type message struct {
	role           string // "user" | "assistant" | "reasoning" | "error"
	content        string
	toolName       string
	toolArgs       string
	previewPath    string
	previewContent string
	previewCommand string
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

				args := m.toolArgs.String()

				if path, ok := extractStringField(args, "path"); ok {
					m.previewPath = path
				}

				if content, ok := extractStringField(args, "content"); ok {
					m.previewContent = content
				}

				if command, ok := extractStringField(args, "command"); ok {
					m.previewCommand = command
				}
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
			sb.WriteString(m.renderFor(UserPrompt, "You> "))
			sb.WriteString(m.renderFor(User, msg.content))

		case "assistant":
			sb.WriteString(m.renderFor(IrisPrompt, "Iris> "))
			sb.WriteString(m.renderFor(Iris, msg.content))

		case "reasoning":
			sb.WriteString(m.renderFor(ReasoningPrompt, "Reasoning> "))
			sb.WriteString(m.renderFor(Reasoning, msg.content) + "\n")

		case "tool":
			sb.WriteString(m.renderFor(ToolPrompt, msg.toolName) + "\n")

			if msg.previewPath != "" {
				sb.WriteString(m.renderFor(ToolKey, "Path: "))
				sb.WriteString(m.renderFor(ToolValue, msg.previewPath) + "\n")
			}

			if msg.previewContent != "" {
				sb.WriteString(m.renderFor(ToolKey, "Content: "))
				sb.WriteString(m.renderFor(ToolValue, msg.previewContent) + "\n")
			}

			if msg.previewCommand != "" {
				sb.WriteString(m.renderFor(ToolKey, "Command: "))
				sb.WriteString(m.renderFor(ToolValue, msg.previewCommand) + "\n")
			}

		case "error":
			sb.WriteString(m.renderFor(ErrorPrompt, "Error> "))
			sb.WriteString(m.renderFor(Error, msg.content) + "\n")
		}
	}

	// Render current streaming response (live)
	if m.state == stateStreaming {
		if m.fullReason.Len() > 0 {
			sb.WriteString(m.renderFor(ReasoningPrompt, "Reasoning> "))
			sb.WriteString(m.renderFor(Reasoning, m.fullReason.String()) + "\n")
		}

		if m.fullResponse.Len() > 0 {
			sb.WriteString(m.renderFor(IrisPrompt, "Iris> "))
			sb.WriteString(m.renderFor(Iris, m.fullResponse.String()))
		}

		if m.toolArgs.Len() > 0 {
			sb.WriteString(m.renderFor(ToolPrompt, m.toolName) + "\n")

			if m.previewPath != "" {
				sb.WriteString(m.renderFor(ToolKey, "Path: "))
				sb.WriteString(m.renderFor(ToolValue, m.previewPath) + "\n")
			}

			if m.previewContent != "" {
				sb.WriteString(m.renderFor(ToolKey, "Content: "))
				sb.WriteString(m.renderFor(ToolValue, m.previewContent) + "\n")
			}

			if m.previewCommand != "" {
				sb.WriteString(m.renderFor(ToolKey, "Command: "))
				sb.WriteString(m.renderFor(ToolValue, m.previewCommand) + "\n")
			}
		}

		if m.fullReason.Len() == 0 && m.fullResponse.Len() == 0 && m.toolArgs.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is thinking...\n")
		} else if m.fullReason.Len() > 0 && m.fullResponse.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is reasoning...\n")
		} else if m.toolArgs.Len() > 0 && m.fullResponse.Len() == 0 {
			sb.WriteString("\n" + m.spinner.View() + " Iris is calling a tool...\n")
		}
	}

	// Always show input text area at bottom
	sb.WriteString("\n" + m.inputText.View() + "\n")

	return tea.NewView(sb.String())
}

func (m model) renderMarkdown(content string) string {
	// Themes: dark, light, notty, dracula, tokyo-night
	r, err := glamour.NewTermRenderer(glamour.WithWordWrap(m.width), glamour.WithStylePath("dark"))
	if err != nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return out
}

func (m model) renderFor(component component, text string) string {
	style := lipgloss.NewStyle()

	switch component {
	case UserPrompt:
		return style.Foreground(lipgloss.Color("#BB9AF7")).Render(text)
	case User:
		return m.renderMarkdown(text)
	case IrisPrompt:
		return style.Foreground(lipgloss.Color("#7DCFFF")).Render(text)
	case Iris:
		return m.renderMarkdown(text)
	case ReasoningPrompt:
		return style.Foreground(lipgloss.Color("#2AC3DE")).Render(text)
	case Reasoning:
		return style.Foreground(lipgloss.Color("#565F89")).Italic(true).Width(m.width).Render(text)
	case ToolPrompt:
		return style.Foreground(lipgloss.Color("#9ECE6A")).Render(text)
	case ToolKey:
		return style.Foreground(lipgloss.Color("#E0AF68")).Render(text)
	case ToolValue:
		return style.Foreground(lipgloss.Color("#C0CAF5")).Width(m.width).Render(text)
	case ErrorPrompt:
		return style.Foreground(lipgloss.Color("#FF6E64")).Bold(true).Render(text)
	case Error:
		return style.Foreground(lipgloss.Color("#FF6E64")).Width(m.width).Render(text)
	default:
		return style.Width(m.width).Render(text)
	}
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
	spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9E64"))

	return model{
		agent:        agent,
		fullReason:   &strings.Builder{},
		fullResponse: &strings.Builder{},
		toolArgs:     &strings.Builder{},
		state:        stateInput,
		inputText:    text,
		stream:       make(chan tea.Msg),
		spinner:      spin,
	}
}

func extractStringField(partialJSON, field string) (string, bool) {
	// Both patterns share the same core: an escape sequence (\\.) takes
	// priority over a plain non-quote char ([^"]), so escaped quotes inside
	// the value are consumed rather than terminating the match.
	//
	// Complete: requires an unescaped closing quote at the end.
	// We validate with Unquote because the greedy engine can still produce a
	// spurious match when the buffer ends mid-escape (e.g. content ends with \\").
	completeRe := regexp.MustCompile(
		`"` + regexp.QuoteMeta(field) + `"` + `\s*:\s*"((?:\\.|[^"])*)"`,
	)
	for _, m := range completeRe.FindAllStringSubmatch(partialJSON, -1) {
		if s, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
			return s, s != ""
		}
	}

	// Partial: value still open — no unescaped closing quote yet.
	// [^"\\] excludes both quote and backslash so we never cross an escape
	// boundary. The trailing \\? absorbs a lone \\ at end-of-buffer (the
	// second byte of an escape pair hasn't arrived yet).
	partialRe := regexp.MustCompile(
		`"` + regexp.QuoteMeta(field) + `"` + `\s*:\s*"((?:\\.|[^"\\])*)\\?$`,
	)
	if m := partialRe.FindStringSubmatch(partialJSON); len(m) == 2 && m[1] != "" {
		if s, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
			return s, s != ""
		}
		return m[1], true
	}

	return "", false
}

// | Background | Base | `#1a1b26` |
// | Title bar | Darker base | `#16161e` |
