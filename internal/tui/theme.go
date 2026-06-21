package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	ColorBlue        = "#58A6FF"
	ColorOrange      = "#E3963E"
	ColorYellow      = "#FFBF00"
	ColorPurple      = "#C678DD"
	ColorGreen       = "#9ECE6A"
	ColorMuted       = "#8B949E"
	ColorDim         = "#3D4554"
	ColorRed         = "#CC310E"
	ColorRedSoft     = "#FF6E64"
	ColorBorder      = "#2D3448"
	ColorBorderFocus = "#58A6FF"
)

var SpinnerFrames = []string{
	"◎", "◉", // open and focus
	"◑", "◑", "◉", // glance left
	"◕", "◕", "◉", // glance right
	"◔", "◔", "◉", // look up
	"◒", "◒", "◉", // look down
	"◕", "⌒", "⌒", "◕", "◉", // wink
	"◕", "◔", "◑", "◒", "◈", "✺", "⊛", "◈", "◉", // dizzy spin
	"◕", "●", "◕", "◔", "◯", "—", "◯", "◔", "◕", "◉", "◎", // full blink
}

func (m model) renderFor(c component, text string) string {
	style := lipgloss.NewStyle()
	w := m.contentWidth()

	switch c {
	case UserPrompt:
		return style.Foreground(lipgloss.Color(ColorBlue)).Render(text)
	case User:
		return m.renderMarkdown(text)
	case IrisPrompt:
		return style.Foreground(lipgloss.Color(ColorOrange)).PaddingTop(1).Render(text)
	case Iris:
		return m.renderMarkdown(text)
	case ReasoningPrompt:
		return style.Foreground(lipgloss.Color(ColorYellow)).PaddingTop(1).Render(text)
	case Reasoning:
		return style.Foreground(lipgloss.Color(ColorMuted)).Italic(true).Width(w).Render(text)
	case ToolPrompt:
		return style.Foreground(lipgloss.Color(ColorPurple)).PaddingTop(1).Render(text)
	case ToolKey:
		return style.Foreground(lipgloss.Color(ColorGreen)).Render(text)
	case ToolValue:
		return style.Foreground(lipgloss.Color(ColorMuted)).Width(w).Render(text)
	case ErrorPrompt:
		return style.Foreground(lipgloss.Color(ColorRed)).Bold(true).Render(text)
	case Error:
		return style.Foreground(lipgloss.Color(ColorRedSoft)).Width(w).Render(text)
	case Spinner:
		var color string
		switch {
		case strings.Contains(text, "reasoning"):
			color = ColorYellow
		case strings.Contains(text, "tooling"):
			color = ColorPurple
		default:
			color = ColorOrange
		}
		m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		return m.spinner.View() + m.spinner.Style.Render(text)
	default:
		return style.Width(w).Render(text)
	}
}
