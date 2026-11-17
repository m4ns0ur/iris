package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// IrisMark is the session-header mark: a three-row eye in the brand color.
// Every row of every frame is the same width, so the header text keeps its
// column while the mark blinks.
var IrisMark = [][3]string{
	{" ▄▀▀▀▄ ", "▐░▓█▓░▌", " ▀▄▄▄▀ "}, // open
	{" ▄▀▀▀▄ ", "▀▄░▓░▄▀", " ▀▀▀▀▀ "}, // half closed
	{"       ", "▀▀▀▀▀▀▀", "       "}, // shut
}

// markBlinkSeq is one blink after the resting open frame: open, half, shut,
// half, open. The final frame is the resting state, so the sequence ends where
// the mark started.
var markBlinkSeq = []int{1, 2, 1, 0}

const (
	// markBlinkDelay is the pause before the blink starts, so the mark is on
	// screen at its resting shape long enough to be seen first.
	markBlinkDelay = 350 * time.Millisecond

	// markBlinkInterval is how long each blink frame stays on screen.
	markBlinkInterval = 80 * time.Millisecond

	// markMinTextWidth is the narrowest header line that is still worth reading
	// beside the mark. Below it the header falls back to the one-cell symbol.
	markMinTextWidth = 24
)

// markBlinkMsg advances the one-shot startup blink.
type markBlinkMsg struct{}

// markBlinkTick schedules the next blink frame after delay.
func markBlinkTick(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return markBlinkMsg{}
	})
}

// blinkHeaderMark advances the mark to the next blink frame and reports whether
// another frame is due. It is one-shot: once the resting frame is reached the
// mark stays open and no timer remains.
func (m *model) blinkHeaderMark() bool {
	if m.markStep >= len(markBlinkSeq) {
		return false
	}
	m.markFrame = markBlinkSeq[m.markStep]
	m.markStep++
	return m.markStep < len(markBlinkSeq)
}

// currentMark returns the blink frame in use, guarding a zero-valued model.
func (m *model) currentMark() [3]string {
	if m.markFrame < 0 || m.markFrame >= len(IrisMark) {
		return IrisMark[0]
	}
	return IrisMark[m.markFrame]
}
