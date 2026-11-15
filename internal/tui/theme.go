package tui

import (
	"charm.land/lipgloss/v2"
)

const (
	ColorBlue        = "#58A6FF"
	ColorOrange      = "#E3963E"
	ColorYellow      = "#E3B341"
	ColorPurple      = "#BC8CFF"
	ColorGreen       = "#7EE787"
	ColorMuted       = "#8B949E"
	ColorDim         = "#6E7681"
	ColorFaint       = "#484F58"
	ColorRed         = "#F85149"
	ColorRedSoft     = "#FF7B72"
	ColorBorder      = "#30363D"
	ColorBorderFocus = "#58A6FF"
)

const IrisSymbol = "◉"

var (
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorMuted))
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorDim))
	styleFaint  = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorFaint))
	styleBlue   = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorBlue))
	styleOrange = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOrange))
	styleYellow = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow))
	stylePurple = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorPurple))
	styleGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen))
	styleRed    = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRedSoft))
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRedSoft)).Bold(true)
	styleBorder = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorBorder))
)
