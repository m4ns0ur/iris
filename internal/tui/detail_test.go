package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/m4ns0ur/iris/agent"
)

// toolLedger finishes one tool call with the given captured output.
func toolLedger(m *model, id, name string, lines []string) {
	m.trackToolStarted(agent.ToolStarted{
		ToolCallID: id,
		ToolName:   name,
		Args:       map[string]any{"command": "echo " + id},
	})
	result := map[string]any{"exit_code": 0}
	if len(lines) > 0 {
		result["output"] = strings.Join(lines, "\n")
	}
	m.finishToolCompleted(agent.ToolCompleted{
		ToolCallID: id,
		ToolName:   name,
		Result:     result,
		Duration:   time.Second,
	})
}

// numberedOutput returns n distinct lines so a test can tell whether the body
// is rendered.
func numberedOutput(prefix string, n int) []string {
	lines := make([]string, 0, n)
	for index := 1; index <= n; index++ {
		lines = append(lines, fmt.Sprintf("%s line %d", prefix, index))
	}
	return lines
}

func ledgerText(m *model) string {
	return ansi.Strip(m.buildContent())
}

func TestCollapsedToolLineShowsOutputMarker(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))
	toolLedger(m, "call-2", "write", nil)
	m.resizeLayout(true)

	content := ledgerText(m)
	if !strings.Contains(content, "12 lines · 1s · ctrl+o") {
		t.Fatalf("collapsed result is missing its marker:\n%s", content)
	}
	if strings.Contains(content, "alpha line 1") {
		t.Fatalf("collapsed result rendered its body:\n%s", content)
	}
	// The write tool has no captured output, so it must not advertise any.
	writes := strings.Count(content, "ctrl+o")
	if writes != 1 {
		t.Fatalf("ctrl+o marker count = %d, want 1 (only the result with output):\n%s", writes, content)
	}
}

func TestCtrlOTogglesTheNewestToolResult(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))
	toolLedger(m, "call-2", "shell", numberedOutput("beta", 12))
	m.resizeLayout(true)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(*model)

	content := ledgerText(m)
	if !strings.Contains(content, "beta line 1") {
		t.Fatalf("ctrl+o did not expand the newest result:\n%s", content)
	}
	if strings.Contains(content, "alpha line 1") {
		t.Fatalf("ctrl+o expanded an older result too:\n%s", content)
	}
	if !m.messages[len(m.messages)-1].expanded {
		t.Fatal("newest result is not marked expanded")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(*model)
	if strings.Contains(ledgerText(m), "beta line 1") {
		t.Fatalf("ctrl+o did not collapse the result again:\n%s", ledgerText(m))
	}
}

// Ctrl+O belongs to the newest result: only that line carries the key hint.
func TestCtrlOHintMarksOnlyTheNewestResult(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))
	toolLedger(m, "call-2", "shell", numberedOutput("beta", 12))
	m.resizeLayout(true)

	content := ledgerText(m)
	if got := strings.Count(content, "· ctrl+o"); got != 1 {
		t.Fatalf("ctrl+o hint count = %d, want 1:\n%s", got, content)
	}
	lines := strings.Split(content, "\n")
	var hinted string
	for _, line := range lines {
		if strings.Contains(line, "· ctrl+o") {
			hinted = line
		}
	}
	if !strings.Contains(hinted, "beta") && !strings.Contains(hinted, "lines") {
		t.Fatalf("hint is not on the newest result line: %q", hinted)
	}
}

// The hint has to move when a newer result arrives, even though the older line
// was already rendered and cached while it owned the hint.
func TestCtrlOHintMovesToANewerResult(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming

	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))
	m.resizeLayout(true)
	first := ledgerText(m)
	if got := strings.Count(first, "· ctrl+o"); got != 1 {
		t.Fatalf("first render hint count = %d, want 1:\n%s", got, first)
	}

	toolLedger(m, "call-2", "shell", numberedOutput("beta", 12))
	m.resizeLayout(true)
	second := ledgerText(m)

	if got := strings.Count(second, "· ctrl+o"); got != 1 {
		t.Fatalf("hint count after a second tool = %d, want 1:\n%s", got, second)
	}
	for _, line := range strings.Split(second, "\n") {
		if !strings.Contains(line, "· ctrl+o") {
			continue
		}
		if !strings.Contains(line, "12 lines") {
			t.Fatalf("hinted line is malformed: %q", line)
		}
	}
	// The older entry is the one without the hint now.
	lines := strings.Split(second, "\n")
	hinted := -1
	for index, line := range lines {
		if strings.Contains(line, "· ctrl+o") {
			hinted = index
		}
	}
	toolLines := []int{}
	for index, line := range lines {
		if strings.Contains(line, "✓ shell") {
			toolLines = append(toolLines, index)
		}
	}
	if len(toolLines) != 2 {
		t.Fatalf("tool lines = %v, want two:\n%s", toolLines, second)
	}
	if hinted != toolLines[len(toolLines)-1] {
		t.Fatalf("hint sits on line %d, want the newest tool line %d:\n%s", hinted, toolLines[len(toolLines)-1], second)
	}
}

func TestVerboseToggleExpandsEveryResult(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))
	toolLedger(m, "call-2", "shell", numberedOutput("beta", 12))
	m.resizeLayout(true)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(*model)

	content := ledgerText(m)
	if !strings.Contains(content, "alpha line 1") || !strings.Contains(content, "beta line 1") {
		t.Fatalf("ctrl+e did not expand every result:\n%s", content)
	}
}

func TestToolDetailHintAppearsOnceAtRunEnd(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))

	updated, _ := m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)
	if got := strings.Count(ledgerText(m), "Tool output is hidden"); got != 1 {
		t.Fatalf("detail hint count = %d, want 1:\n%s", got, ledgerText(m))
	}

	m.state = StateStreaming
	toolLedger(m, "call-2", "shell", numberedOutput("beta", 12))
	updated, _ = m.Update(agentEventMsg{event: agent.AgentFinished{At: time.Now()}})
	m = updated.(*model)
	if got := strings.Count(ledgerText(m), "Tool output is hidden"); got != 1 {
		t.Fatalf("detail hint repeated: count = %d, want 1", got)
	}
}

// The hint must not split the tight run of tool lines in the ledger.
func TestToolDetailHintDoesNotSplitTheLedger(t *testing.T) {
	m := sizedApprovalModel(t, 100, 30)
	m.state = StateStreaming
	toolLedger(m, "call-1", "shell", numberedOutput("alpha", 12))
	toolLedger(m, "call-2", "shell", numberedOutput("beta", 4))
	m.resizeLayout(true)

	content := ledgerText(m)
	if strings.Contains(content, "Tool output is hidden") {
		t.Fatalf("hint rendered inside the ledger:\n%s", content)
	}

	lines := strings.Split(content, "\n")
	first, second := -1, -1
	for index, line := range lines {
		switch {
		case first < 0 && strings.Contains(line, "✓ shell"):
			first = index
		case first >= 0 && second < 0 && strings.Contains(line, "✓ shell"):
			second = index
		}
	}
	if first < 0 || second < 0 {
		t.Fatalf("tool lines missing from the ledger:\n%s", content)
	}
	if second != first+1 {
		t.Fatalf("tool lines are not adjacent (%d, %d):\n%s", first, second, content)
	}
}
