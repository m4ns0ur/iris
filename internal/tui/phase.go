package tui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
)

// phase selects the animated indicator for the current kind of work. The
// spinner family carries the phase; the status line does not need a word such
// as "thinking" or "tooling". The one exception is waiting for a server that
// has not answered at all: that state names itself, because "nothing has
// happened yet" is exactly what a spinner cannot say.
type phase int

const (
	phaseIdle phase = iota
	phaseThinking
	phaseReasoning
	phaseResponding
	phaseTooling
	phaseCompacting
	phaseCanceling

	// phaseWaiting and phaseWaitingSlow describe a dispatched request that the
	// server has not answered yet. Nothing is generating: the server is
	// connecting, queueing, loading a model, or prefilling the prompt.
	phaseWaiting
	phaseWaitingSlow
)

const (
	// responseWaitHintAfter is how long a request may go unanswered before the
	// status line names the wait. Shorter gaps are normal network latency.
	responseWaitHintAfter = time.Second

	// responseWaitSlowAfter is when the wait is long enough that an on-demand
	// model load is the likely cause. It matches the engine's warning
	// threshold, which logs the same wait.
	responseWaitSlowAfter = 10 * time.Second
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
	// Non-circular families for special background states. MiniDot's braille
	// cells are one wide, so an unanswered request does not move the layout.
	// Every Hamburger frame is two cells wide, so compaction widens the spinner
	// by one column for the whole phase instead of jittering per frame.
	waitingSpinner    = spinner.MiniDot
	compactingSpinner = spinner.Hamburger
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
	case m.awaitingResponse:
		if m.waitingForServerSlowly() {
			return phaseWaitingSlow
		}
		return phaseWaiting
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
	case phaseWaiting, phaseWaitingSlow:
		return waitingSpinner
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
	case phaseWaiting:
		return ColorMuted
	case phaseWaitingSlow:
		return ColorYellow
	default:
		return ColorOrange
	}
}

// waitingForServerSlowly reports that a dispatched request has gone
// unanswered long enough for on-demand model loading or prompt prefill to be
// the likely explanation.
func (m *model) waitingForServerSlowly() bool {
	if m.responseWaitSince.IsZero() {
		return false
	}
	return time.Since(m.responseWaitSince) >= responseWaitSlowAfter
}

// serverWaitLabel describes the current wait for a dispatched request, or
// returns an empty string when the wait is short or already answered.
func (m *model) serverWaitLabel() string {
	if !m.awaitingResponse || m.responseWaitSince.IsZero() {
		return ""
	}
	waited := time.Since(m.responseWaitSince)
	if waited < responseWaitHintAfter {
		return ""
	}
	label := "waiting for server " + elapsedLabel(waited)
	if waited >= responseWaitSlowAfter {
		label += " · model may be loading"
	}
	return label
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
