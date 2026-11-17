package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/m4ns0ur/iris/agent"
)

// renderKey identifies the inputs that change how a stored message is drawn.
// Width changes on resize, the two detail toggles are global, and a tool result
// can expand on its own, so the flag is part of the key and only that message
// re-renders when it flips.
type renderKey struct {
	width     int
	verbose   bool
	reasoning bool
	// expanded is the message's own result expansion. expandable is whether
	// this entry currently carries the Ctrl+O hint: it moves to the newest
	// result, so a cached line would otherwise keep advertising the key after
	// the next tool call arrived.
	expanded   bool
	expandable bool
}

func (m *model) currentRenderKey() renderKey {
	return renderKey{
		width:     m.contentWidth(),
		verbose:   m.showToolOutput,
		reasoning: m.showReasoning,
	}
}

// renderCached renders a stored message once per width/verbosity combination.
// The transcript can grow large; re-rendering every message on every streamed
// token is by far the most expensive thing the old TUI did.
func (m *model) renderCached(msg *message) string {
	// The header carries the animated mark, so it is rendered freshly each
	// frame; it is three short rows, while invalidating the cache for one blink
	// frame would re-render the whole transcript.
	if msg.role == "header" {
		return m.renderHeader(*msg)
	}
	key := m.currentRenderKey()
	key.expanded = msg.expanded
	key.expandable = msg.role == "tool" && msg.toolCallID != "" && msg.toolCallID == m.expandableTool
	if msg.rendered != "" && msg.renderKey == key {
		return msg.rendered
	}
	msg.rendered = m.renderMessage(*msg)
	msg.renderKey = key
	return msg.rendered
}

func (m *model) renderMessage(msg message) string {
	switch msg.role {
	case "header":
		return m.renderHeader(msg)
	case "user":
		return m.renderUser(msg)
	case "assistant":
		return m.renderAssistant(msg)
	case "reasoning":
		return m.renderReasoning(msg)
	case "tool":
		return m.renderTool(msg)
	case "summary":
		return m.renderSummary(msg)
	case "help":
		return m.renderHelp(msg)
	case "error":
		return m.renderError(msg)
	default:
		return m.renderNotice(msg)
	}
}

// renderHeader draws the session mark beside the two header lines. The mark is
// three rows tall, so the text keeps its left column across every blink frame;
// on a narrow terminal the one-cell symbol replaces it and the header stays two
// lines.
func (m *model) renderHeader(msg message) string {
	lines := strings.Split(strings.TrimSpace(msg.content), "\n")
	for index, line := range lines {
		lines[index] = sanitizeLine(line)
	}

	width := m.contentWidth()
	mark := m.currentMark()
	markWidth := lipgloss.Width(mark[0])

	if width-markWidth-2 < markMinTextWidth {
		return renderHeaderSymbol(lines, width)
	}

	rows := make([]string, 0, len(mark))
	for index, art := range mark {
		art += strings.Repeat(" ", max(markWidth-lipgloss.Width(art), 0))
		text := ""
		if index < len(lines) {
			text = "  " + truncateMiddleDisplay(lines[index], max(width-markWidth-2, 10))
		}
		rows = append(rows, styleOrange.Render(art)+styleDim.Render(text))
	}
	return strings.Join(rows, "\n") + "\n"
}

// renderHeaderSymbol is the narrow-terminal header: the one-cell mark on the
// first line and the remaining lines aligned under its text.
func renderHeaderSymbol(lines []string, width int) string {
	indent := strings.Repeat(" ", lipgloss.Width(IrisSymbol)+1)
	bodyWidth := max(width-lipgloss.Width(indent), 10)

	var b strings.Builder
	for index, line := range lines {
		line = truncateMiddleDisplay(line, bodyWidth)
		if index == 0 {
			b.WriteString(styleOrange.Render(IrisSymbol) + styleDim.Render(" "+line))
		} else {
			b.WriteString(styleDim.Render(indent + line))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) renderUser(msg message) string {
	label := "you › "
	if msg.tag != "" {
		label = "you (" + msg.tag + ") › "
	}
	return renderPrefixed(label, styleBlue, msg.content, m.contentWidth(), lipgloss.NewStyle())
}

// renderPrefixed lays out a plain-text block with an inline label and a
// hanging indent. Explicit newlines in the content are preserved.
func renderPrefixed(label string, labelStyle lipgloss.Style, content string, width int, bodyStyle lipgloss.Style) string {
	content = sanitizeDisplayText(content)
	content = strings.Trim(content, "\n")
	if strings.TrimSpace(content) == "" {
		return labelStyle.Render(label) + "\n"
	}

	labelWidth := lipgloss.Width(label)
	bodyWidth := max(width-labelWidth, 10)
	wrapped := ansi.Wordwrap(content, bodyWidth, "")
	lines := strings.Split(wrapped, "\n")

	var b strings.Builder
	b.WriteString(labelStyle.Render(label))
	b.WriteString(bodyStyle.Render(lines[0]))
	b.WriteByte('\n')

	indent := strings.Repeat(" ", labelWidth)
	for _, line := range lines[1:] {
		b.WriteString(indent)
		b.WriteString(bodyStyle.Render(line))
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) renderAssistant(msg message) string {
	if strings.TrimSpace(msg.content) == "" {
		return ""
	}

	label := "iris › "
	labelWidth := lipgloss.Width(label)
	body := m.renderMarkdown(sanitizeDisplayText(msg.content), max(m.contentWidth()-labelWidth-2, 10))
	if body == "" {
		return styleOrange.Render(label) + "\n"
	}

	lines := strings.Split(body, "\n")
	var b strings.Builder
	b.WriteString(styleOrange.Render(label))
	b.WriteString(lines[0])
	b.WriteByte('\n')
	indent := strings.Repeat(" ", labelWidth)
	for _, line := range lines[1:] {
		b.WriteString(indent)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// renderLive renders the in-progress turn. Streaming text is kept as plain
// wrapped text: re-parsing partial markdown on every delta is expensive and
// makes the layout jump whenever a fence or list is still incomplete.
func (m *model) renderLive() string {
	if m.state != StateStreaming {
		return ""
	}

	var b strings.Builder

	if m.showReasoning && m.fullReason.Len() > 0 {
		b.WriteString(renderPrefixed(
			"reasoning › ",
			styleDim,
			m.fullReason.String(),
			m.contentWidth(),
			styleMuted,
		))
	}

	if m.fullResponse.Len() > 0 {
		if hasOpenCodeFence(m.fullResponse.String()) {
			// A partially streamed code fence has no stable rendered form;
			// keep it plain until the closing fence arrives.
			b.WriteString(renderPrefixed(
				"iris › ",
				styleOrange,
				m.fullResponse.String(),
				m.contentWidth(),
				lipgloss.NewStyle(),
			))
		} else {
			b.WriteString(m.renderAssistant(message{
				role:    "assistant",
				content: m.fullResponse.String(),
			}))
		}
	}

	for _, id := range m.toolOrder {
		tool := m.activeTools[id]
		if tool == nil {
			continue
		}
		b.WriteString(m.renderActiveTool(tool))
	}

	return b.String()
}

func hasOpenCodeFence(text string) bool {
	return strings.Count(text, "```")%2 == 1
}

func (m *model) renderActiveTool(tool *activeTool) string {
	glyph := stylePurple.Render("⋯")
	name := stylePurple.Render(sanitizeLine(tool.name))
	target := styleMuted.Render(truncateMiddleDisplay(sanitizeLine(tool.target), max(m.contentWidth()-24, 12)))
	line := "  " + glyph + " " + name
	if target != "" {
		line += "  " + target
	}
	return line + "\n"
}

func (m *model) renderReasoning(msg message) string {
	if !m.showReasoning {
		return styleDim.Render(fmt.Sprintf("  ⋯ reasoning · ~%s tok", formatTokens(estimateTokens(msg.content)))) + "\n"
	}
	return renderPrefixed("reasoning › ", styleDim, msg.content, m.contentWidth(), styleMuted)
}

func (m *model) renderTool(msg message) string {
	status := msg.toolStatus
	glyph := "✓"
	glyphStyle := styleGreen
	switch status {
	case "error", "interrupted", "denied":
		glyph = "✗"
		glyphStyle = styleError
	case "running":
		glyph = "⋯"
		glyphStyle = stylePurple
	}

	name := sanitizeLine(msg.toolName)
	if name == "" {
		name = "tool"
	}

	visible := m.toolOutputVisible(msg, status)

	meta := make([]string, 0, 6)
	if msg.toolMeta != "" {
		meta = append(meta, sanitizeLine(msg.toolMeta))
	}
	if msg.toolDuration > 0 {
		meta = append(meta, shortDuration(msg.toolDuration))
	}
	if msg.toolExit != 0 {
		meta = append(meta, fmt.Sprintf("exit %d", msg.toolExit))
	}
	if status == "interrupted" {
		meta = append(meta, "interrupted")
	}
	meta = append(meta, m.toolOutputHint(msg, visible)...)

	line := "  " + glyphStyle.Render(glyph) + " " + stylePurple.Render(name)
	target := sanitizeLine(msg.toolTarget)
	if target != "" {
		reserved := 8 + len(name) + sumLengths(meta) + 3*len(meta)
		target = truncateMiddleDisplay(target, max(m.contentWidth()-reserved, 12))
		line += "  " + styleMuted.Render(target)
	}
	if len(meta) > 0 {
		line += styleDim.Render(" · " + strings.Join(meta, " · "))
	}
	line += "\n"

	if detail := sanitizeLine(msg.toolDetail); detail != "" && detail != firstLine(msg.toolOutput) {
		line += "    " + styleRed.Render(detail) + "\n"
	}

	if visible {
		line += m.renderToolOutput(msg)
	}

	return line
}

// toolOutputLimit is how many output lines a ledger entry shows.
func (m *model) toolOutputLimit(msg message) int {
	if m.showToolOutput || msg.expanded {
		return 16
	}
	return 8
}

// toolOutputVisible reports whether the result body is rendered for this entry.
// Failures always show their output so the cause is visible without a toggle.
func (m *model) toolOutputVisible(msg message, status string) bool {
	if msg.toolOutput == "" {
		return false
	}
	return m.showToolOutput || msg.expanded || status == "error"
}

// toolOutputHint names the per-result toggle on the ledger line whose output is
// hidden. The line count itself is already part of the tool metadata, so the
// hint adds only the key that opens this entry.
func (m *model) toolOutputHint(msg message, visible bool) []string {
	if msg.toolOutput == "" {
		return nil
	}
	if visible && countLines(msg.toolOutput) <= m.toolOutputLimit(msg) {
		return nil
	}
	if msg.toolCallID == "" || msg.toolCallID != m.expandableTool {
		return nil
	}
	return []string{"ctrl+o"}
}

func (m *model) renderToolOutput(msg message) string {
	limit := m.toolOutputLimit(msg)
	output := strings.TrimRight(msg.toolOutput, "\n")
	if output == "" {
		return ""
	}

	lines := strings.Split(output, "\n")
	shown := lines
	omitted := 0
	if len(lines) > limit {
		head := limit / 2
		tail := limit - head
		shown = append(append([]string{}, lines[:head]...), lines[len(lines)-tail:]...)
		omitted = len(lines) - len(shown)
	}

	var b strings.Builder
	for index, line := range shown {
		if omitted > 0 && index == limit/2 {
			b.WriteString(styleDim.Render(fmt.Sprintf("    … %d lines omitted", omitted)) + "\n")
		}
		b.WriteString("    " + styleMuted.Render(sanitizeLine(line)) + "\n")
	}
	if msg.toolOutputPath != "" {
		b.WriteString("    " + styleDim.Render("full output: "+sanitizeLine(msg.toolOutputPath)) + "\n")
	}
	return b.String()
}

// renderHelp styles the preformatted help columns at render time. The stored
// message is plain text, so the notice sanitizer never sees (or mangles) ANSI.
func (m *model) renderHelp(msg message) string {
	content := sanitizeDisplayText(msg.content)
	content = strings.Trim(content, "\n")
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	var b strings.Builder
	for index, line := range lines {
		prefix := "    "
		if index == 0 {
			prefix = "  · "
		}
		switch {
		case strings.TrimSpace(line) == "":
			b.WriteByte('\n')
			continue
		case strings.HasPrefix(line, "  "):
			b.WriteString(styleDim.Render(prefix))
			b.WriteString(renderHelpRow(line[2:]))
		default:
			b.WriteString(styleDim.Render(prefix))
			b.WriteString(styleMuted.Render(sanitizeLine(line)))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func renderHelpRow(row string) string {
	start, end, ok := helpColumnGap(row)
	if !ok {
		return styleMuted.Render(row)
	}

	label := strings.TrimRight(row[:start], " ")
	description := row[end:]
	padding := strings.Repeat(" ", max(end-len(label), 0))
	line := styleMuted.Render(label) + padding
	if description != "" {
		line += styleDim.Render(description)
	}
	return line
}

func helpColumnGap(row string) (int, int, bool) {
	for index := 0; index+1 < len(row); index++ {
		if row[index] != ' ' || row[index+1] != ' ' {
			continue
		}
		end := index
		for end < len(row) && row[end] == ' ' {
			end++
		}
		return index, end, true
	}
	return 0, 0, false
}

func (m *model) renderNotice(msg message) string {
	return renderBlock("  · ", styleDim, msg.content, m.contentWidth())
}

func (m *model) renderError(msg message) string {
	content := sanitizeDisplayText(msg.content)
	content = strings.Trim(content, "\n")
	if content == "" {
		return styleError.Render("  ✗") + "\n"
	}

	wrapped := ansi.Wordwrap(content, max(m.contentWidth()-4, 10), "")
	lines := strings.Split(wrapped, "\n")
	var b strings.Builder
	b.WriteString(styleError.Render("  ✗ ") + styleRed.Render(lines[0]) + "\n")
	for _, line := range lines[1:] {
		b.WriteString("    " + styleRed.Render(line) + "\n")
	}
	return b.String()
}

// renderBlock renders a pre-formatted notice. It intentionally does not
// reflow lines: command help and status blocks rely on their own alignment.
func renderBlock(prefix string, prefixStyle lipgloss.Style, content string, width int) string {
	content = sanitizeDisplayText(content)
	content = strings.Trim(content, "\n")
	if content == "" {
		return prefixStyle.Render(strings.TrimRight(prefix, " ")) + "\n"
	}
	if !strings.Contains(content, "\n") {
		content = ansi.Wordwrap(content, max(width-lipgloss.Width(prefix), 10), "")
	}

	lines := strings.Split(content, "\n")
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	var b strings.Builder
	b.WriteString(prefixStyle.Render(prefix))
	b.WriteString(lines[0])
	b.WriteByte('\n')
	for _, line := range lines[1:] {
		b.WriteString(styleDim.Render(indent))
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) renderSummary(msg message) string {
	outcome := msg.outcome
	if outcome == "" {
		outcome = "Task completed"
	}

	glyph := "✓"
	glyphStyle := styleGreen
	label := "done"
	switch outcome {
	case "Run canceled":
		glyph = "·"
		glyphStyle = styleYellow
		label = "canceled"
	case "Run failed":
		glyph = "✗"
		glyphStyle = styleError
		label = "failed"
	}

	parts := make([]string, 0, 4)
	summary := msg.runSummary
	if summary != nil {
		if summary.ToolExecutions > 0 {
			parts = append(parts, plural(summary.ToolExecutions, "tool"))
		}
		if summary.LLMRequests > 0 {
			parts = append(parts, fmt.Sprintf("%d req", summary.LLMRequests))
		}
		if tokens := summaryTokens(summary); tokens > 0 {
			parts = append(parts, formatPreciseTokens(tokens)+" tok")
		}
		if duration := summary.Duration(); duration > 0 {
			parts = append(parts, shortDuration(duration))
		}
	}

	line := "  " + glyphStyle.Render(glyph) + " " + label
	if len(parts) > 0 {
		line += styleDim.Render(" · " + strings.Join(parts, " · "))
	}
	line += "\n"

	if summary != nil && summary.GitRepository {
		count := len(summary.FilesChanged)
		switch {
		case count == 0:
			// A clean tree is worth one short, quiet line only in verbose mode.
			if m.showToolOutput {
				line += "    " + styleDim.Render("no files changed") + "\n"
			}
		case m.showToolOutput:
			line += "    " + styleDim.Render(fmt.Sprintf("Δ %s changed", plural(count, "file"))) + "\n"
			const maxFiles = 12
			for index, change := range summary.FilesChanged {
				if index >= maxFiles {
					line += "    " + styleDim.Render(fmt.Sprintf("… %d more", count-maxFiles)) + "\n"
					break
				}
				line += "    " + styleMuted.Render(sanitizeLine(change.Status+" "+change.Path)) + "\n"
			}
		default:
			line += "    " + styleDim.Render(fmt.Sprintf("Δ %s changed · /status", plural(count, "file"))) + "\n"
		}
	}

	return line
}

func (m *model) renderMarkdown(content string, width int) string {
	options := []glamour.TermRendererOption{glamour.WithWordWrap(max(width, 10))}
	if style := strings.TrimSpace(os.Getenv("GLAMOUR_STYLE")); style != "" {
		options = append(options, glamour.WithStylePath(style))
	} else {
		options = append(options, glamour.WithStylePath("dark"))
	}
	renderer, err := glamour.NewTermRenderer(options...)
	if err != nil {
		return sanitizeDisplayText(content)
	}

	out, err := renderer.Render(content)
	if err != nil {
		return sanitizeDisplayText(content)
	}

	out = strings.Trim(out, "\n")
	lines := strings.Split(out, "\n")
	for index, line := range lines {
		lines[index] = trimStyledRight(line)
	}
	out = strings.Trim(strings.Join(lines, "\n"), "\n")
	return dedent(out, 2)
}

// trimStyledRight removes glamour's trailing padding, which is emitted as a
// long run of individually styled spaces. Stripping it keeps the viewport
// content small and the transcript visually light.
func trimStyledRight(line string) string {
	end := len(line)
	for end > 0 {
		switch line[end-1] {
		case ' ':
			end--
			continue
		case 'm':
			start := strings.LastIndex(line[:end], "\x1b[")
			if start >= 0 && isANSISequence(line[start:end]) {
				end = start
				continue
			}
		}
		break
	}
	if end == len(line) {
		return line
	}
	trimmed := strings.TrimRight(line[:end], " ")
	if ansi.StringWidth(trimmed) == 0 {
		return ""
	}
	return trimmed + "\x1b[0m"
}

func isANSISequence(sequence string) bool {
	if !strings.HasPrefix(sequence, "\x1b[") || !strings.HasSuffix(sequence, "m") {
		return false
	}
	for _, r := range sequence[2 : len(sequence)-1] {
		if (r < '0' || r > '9') && r != ';' && r != '?' {
			return false
		}
	}
	return true
}

// dedent removes up to n leading spaces from every non-empty line. Glamour
// indents the whole document, which otherwise makes messages look like they
// are floating away from their labels.
func dedent(text string, n int) string {
	if n <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		remove := 0
		for remove < n && remove < len(line) && line[remove] == ' ' {
			remove++
		}
		lines[index] = line[remove:]
	}
	return strings.Join(lines, "\n")
}

// sanitizeDisplayText removes control characters, including escape sequences,
// from model- and tool-provided text before it reaches the terminal. Newlines
// and tabs are preserved.
func sanitizeDisplayText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n':
			b.WriteByte('\n')
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f:
			continue
		case r >= 0x80 && r <= 0x9f:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeLine(text string) string {
	return sanitizeDisplayText(strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " "))
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return max(len([]rune(text))/4, 1)
}

func summaryTokens(summary *agent.RunSummary) int {
	if summary == nil {
		return 0
	}
	if summary.Usage.TotalTokens > 0 {
		return summary.Usage.TotalTokens
	}
	return summary.Usage.PromptTokens + summary.Usage.CompletionTokens
}

func elapsedLabel(duration time.Duration) string {
	if duration < time.Second {
		return "0s"
	}
	return shortDuration(duration)
}

func shortDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	if duration < time.Minute {
		seconds := duration.Seconds()
		if seconds >= 10 || seconds == float64(int(seconds)) {
			return fmt.Sprintf("%.0fs", seconds)
		}
		return fmt.Sprintf("%.1fs", seconds)
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(duration.Minutes()), int(duration.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(duration.Hours()), int(duration.Minutes())%60)
}

// formatPreciseTokens keeps exact counts for small local-model runs so two short
// replies do not collapse to the same rounded "1.4k".
func formatPreciseTokens(tokens int) string {
	if tokens < 10_000 {
		return formatInt(tokens)
	}
	return formatTokens(tokens)
}

func formatTokens(tokens int) string {
	switch {
	case tokens < 0:
		return "0"
	case tokens < 1000:
		return fmt.Sprintf("%d", tokens)
	case tokens < 100_000:
		return formatUnit(float64(tokens)/1000, "k")
	case tokens < 1_000_000:
		return fmt.Sprintf("%.0fk", float64(tokens)/1000)
	default:
		return formatUnit(float64(tokens)/1_000_000, "M")
	}
}

func formatUnit(value float64, unit string) string {
	text := fmt.Sprintf("%.1f", value)
	text = strings.TrimSuffix(text, ".0")
	return text + unit
}

func plural(count int, singular string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %ss", count, singular)
}

func sumLengths(values []string) int {
	total := 0
	for _, value := range values {
		total += lipgloss.Width(value)
	}
	return total
}

// truncateMiddleDisplay keeps both ends of a command or path visible. The
// important argument is as likely to be at the end as at the start.
func truncateMiddleDisplay(value string, maxWidth int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= maxWidth {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxWidth {
		return value
	}
	if maxWidth <= 1 {
		return "…"
	}
	head := (maxWidth - 1) / 2
	tail := maxWidth - 1 - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

func countLines(text string) int {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func displayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home, err := userHomeDir()
	if err == nil && home != "" {
		if path == home {
			return "~"
		}
		prefix := home + string(filepath.Separator)
		if strings.HasPrefix(path, prefix) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}

func displayWidthString(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	return truncateMiddleDisplay(text, width)
}
