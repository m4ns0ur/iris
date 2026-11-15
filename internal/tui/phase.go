package tui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
)

// phase selects the animated indicator for the current kind of work. The
// spinner family carries the phase; the status line no longer needs a word
// such as "thinking" or "tooling".
type phase int

const (
	phaseIdle phase = iota
	phaseThinking
	phaseReasoning
	phaseResponding
	phaseTooling
	phaseCompacting
	phaseCanceling
)

var (
	// Circular families for model work. Each family has a different silhouette
	// so the phase is readable at a glance, and all frames are one cell.
	thinkingSpinner = spinner.Spinner{
		Frames: []string{"◐", "◓", "◑", "◒"},
		FPS:    time.Second / 8,
	}
	reasoningSpinner = spinner.Spinner{
		Frames: []string{"◴", "◷", "◶", "◵"},
		FPS:    time.Second / 8,
	}
	respondingSpinner = spinner.Spinner{
		Frames: []string{"◉", "◎", "◍", "◌", "○", "◌", "◍", "◎"},
		FPS:    time.Second / 8,
	}
	toolingSpinner = spinner.Spinner{
		Frames: []string{"◜", "◝", "◞", "◟"},
		FPS:    time.Second / 8,
	}

	// Non-circular families for special background states. MiniDot and Pulse
	// are one cell; Hamburger's trigram frames are double-width in lipgloss
	// and would shift the status line.
	compactingSpinner = spinner.MiniDot
	cancelingSpinner  = spinner.Pulse
)

func (m *model) currentPhase() phase {
	if m.state != StateStreaming {
		return phaseIdle
	}
	switch {
	case m.canceling:
		return phaseCanceling
	case m.compacting:
		return phaseCompacting
	case len(m.activeTools) > 0:
		return phaseTooling
	case m.fullReason.Len() > 0 && m.fullResponse.Len() == 0:
		return phaseReasoning
	case m.fullResponse.Len() > 0:
		return phaseResponding
	default:
		return phaseThinking
	}
}

func phaseSpinner(p phase) spinner.Spinner {
	switch p {
	case phaseThinking:
		return thinkingSpinner
	case phaseReasoning:
		return reasoningSpinner
	case phaseResponding:
		return respondingSpinner
	case phaseTooling:
		return toolingSpinner
	case phaseCompacting:
		return compactingSpinner
	case phaseCanceling:
		return cancelingSpinner
	default:
		return spinner.Spinner{Frames: []string{" "}, FPS: time.Second / 8}
	}
}

func phaseColor(p phase) string {
	switch p {
	case phaseCanceling:
		return ColorRedSoft
	case phaseCompacting, phaseReasoning:
		return ColorYellow
	case phaseTooling:
		return ColorPurple
	default:
		return ColorOrange
	}
}

// spinnerFrame derives the current frame from elapsed run time. This keeps the
// animation stable as phases change: no frame index has to be reset when the
// spinner family changes, and a stale tick cannot index past a shorter frame
// set.
func (m *model) spinnerFrame(p phase) string {
	sp := phaseSpinner(p)
	if len(sp.Frames) == 0 {
		return ""
	}
	interval := sp.FPS
	if interval <= 0 {
		interval = time.Second / 8
	}
	elapsed := time.Since(m.runStartedAt)
	if m.runStartedAt.IsZero() || elapsed < 0 {
		elapsed = 0
	}
	return sp.Frames[int(elapsed/interval)%len(sp.Frames)]
}
