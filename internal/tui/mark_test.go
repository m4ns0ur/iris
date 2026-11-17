package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// headerRows renders the session header and returns its lines without styles.
func headerRows(t *testing.T, m *model) []string {
	t.Helper()

	for _, msg := range m.messages {
		if msg.role != "header" {
			continue
		}
		rendered := strings.TrimRight(ansi.Strip(m.renderMessage(msg)), "\n")
		return strings.Split(rendered, "\n")
	}
	t.Fatal("transcript has no session header")
	return nil
}

// textColumn reports the cell offset where needle starts in line.
func textColumn(t *testing.T, line, needle string) int {
	t.Helper()

	index := strings.Index(line, needle)
	if index < 0 {
		t.Fatalf("line %q does not contain %q", line, needle)
	}
	return lipgloss.Width(line[:index])
}

func TestHeaderShowsTheIrisMark(t *testing.T) {
	m, _, _ := newSessionModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)

	rows := headerRows(t, m)
	if len(rows) != len(IrisMark[0]) {
		t.Fatalf("header rows = %d, want the mark height %d:\n%s", len(rows), len(IrisMark[0]), strings.Join(rows, "\n"))
	}
	for index, art := range IrisMark[0] {
		if !strings.HasPrefix(rows[index], strings.TrimRight(art, " ")) {
			t.Fatalf("row %d = %q, want the open mark %q", index, rows[index], art)
		}
	}
	if column, want := textColumn(t, rows[0], "iris ·"), lipgloss.Width(IrisMark[0][0])+2; column != want {
		t.Fatalf("first header line starts at %d, want %d", column, want)
	}
	if first, second := textColumn(t, rows[0], "iris ·"), textColumn(t, rows[1], "/workspace"); first != second {
		t.Fatalf("header text columns = %d and %d, want them aligned", first, second)
	}
	if !strings.Contains(rows[2], strings.TrimSpace(IrisMark[0][2])) {
		t.Fatalf("last mark row missing: %q", rows[2])
	}
}

func TestHeaderFallsBackToTheSymbolWhenNarrow(t *testing.T) {
	m := newApprovalModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = updated.(*model)

	rows := headerRows(t, m)
	if len(rows) != 2 {
		t.Fatalf("narrow header rows = %d, want 2:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[0], IrisSymbol+" ") {
		t.Fatalf("narrow header = %q, want the %q mark", rows[0], IrisSymbol)
	}
	if strings.Contains(strings.Join(rows, "\n"), "▄▀▀▀▄") {
		t.Fatalf("narrow header still draws the tall mark:\n%s", strings.Join(rows, "\n"))
	}
}

// The layout depends on every frame having one shape, or the header text would
// shift sideways while the mark blinks.
func TestIrisMarkFramesShareOneWidth(t *testing.T) {
	width := lipgloss.Width(IrisMark[0][0])
	for frame, rows := range IrisMark {
		for row, art := range rows {
			if got := lipgloss.Width(art); got != width {
				t.Fatalf("frame %d row %d width = %d, want %d", frame, row, got, width)
			}
		}
	}
}

func TestHeaderMarkBlinksOnceAndStops(t *testing.T) {
	m := newApprovalModel(t)
	if m.markFrame != 0 || m.markStep != 0 {
		t.Fatalf("initial mark frame = %d, step = %d", m.markFrame, m.markStep)
	}

	for step, want := range markBlinkSeq {
		updated, cmd := m.Update(markBlinkMsg{})
		m = updated.(*model)
		if m.markFrame != want {
			t.Fatalf("frame %d = %d, want %d", step, m.markFrame, want)
		}
		if step < len(markBlinkSeq)-1 && cmd == nil {
			t.Fatalf("blink stopped at frame %d", step)
		}
		if step == len(markBlinkSeq)-1 && cmd != nil {
			t.Fatal("blink scheduled another frame after settling")
		}
	}
	if m.markFrame != 0 {
		t.Fatalf("mark settled on frame %d, want the open frame", m.markFrame)
	}
	if m.markStep != len(markBlinkSeq) {
		t.Fatalf("mark step = %d, want %d", m.markStep, len(markBlinkSeq))
	}
}

// The header must follow the blink frame; a cached transcript would freeze it.
func TestHeaderFollowsTheBlinkFrame(t *testing.T) {
	m, _, _ := newSessionModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)

	open := ansi.Strip(m.buildContent())
	if strings.Contains(open, "▀▀▀▀▀▀▀") {
		t.Fatalf("open mark already looks shut:\n%s", open)
	}

	m.markFrame = 2
	shut := ansi.Strip(m.buildContent())
	if !strings.Contains(shut, "▀▀▀▀▀▀▀") {
		t.Fatalf("shut mark did not render after the frame changed:\n%s", shut)
	}
	if !strings.Contains(shut, "iris · test-model") {
		t.Fatalf("header text missing while shut:\n%s", shut)
	}
}

// The transcript lives in the viewport, whose content is only rebuilt by the
// layout pass. A blink that advances the model without repainting is invisible.
func TestBlinkRepaintsTheTranscript(t *testing.T) {
	m, _, _ := newSessionModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*model)

	before := ansi.Strip(m.View().Content)
	if !strings.Contains(before, strings.TrimSpace(IrisMark[0][1])) {
		t.Fatalf("open mark missing from the first render:\n%s", before)
	}

	updated, _ = m.Update(markBlinkMsg{})
	m = updated.(*model)
	after := ansi.Strip(m.View().Content)

	if before == after {
		t.Fatal("blink frame did not repaint the transcript")
	}
	if !strings.Contains(after, strings.TrimSpace(IrisMark[1][1])) {
		t.Fatalf("half-closed mark missing after the blink frame:\n%s", after)
	}
}
