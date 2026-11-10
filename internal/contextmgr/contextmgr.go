// Package contextmgr builds the prompt view sent to the model and compacts
// older conversation history when the prompt would exceed the model window.
package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	publicagent "github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
)

const (
	// DefaultWindow is used when the model context size is unknown.
	DefaultWindow = 32768

	// DefaultReserveTokens is reserved for the next assistant response.
	DefaultReserveTokens = 8192

	// DefaultCompactAt is the fraction of the prompt budget at which proactive
	// compaction starts.
	DefaultCompactAt = 0.85

	// DefaultKeepRecentTokens is the amount of recent conversation kept
	// verbatim when compacting.
	DefaultKeepRecentTokens = 16384

	// DefaultSummaryMaxTokens bounds the generated summary.
	DefaultSummaryMaxTokens = 2048

	minTranscriptTokens = 512
)

// ErrContextOverflow is returned when even the compacted prompt cannot fit in
// the model window. It is the public agent sentinel so embedders can classify
// the error without importing internal packages.
var ErrContextOverflow = publicagent.ErrContextOverflow

// Config configures context-window management.
type Config struct {
	Window           int
	ReserveTokens    int
	CompactAt        float64
	KeepRecentTokens int
	SummaryMaxTokens int
}

// SummarizeResult is the output of a summarization call.
type SummarizeResult struct {
	Summary string
	Usage   provider.Usage
}

// Summarizer compacts a transcript into a concise summary.
type Summarizer func(ctx context.Context, transcript string, maxTokens int) (SummarizeResult, error)

// Compaction describes one context reduction.
type Compaction struct {
	BeforeTokens            int
	AfterTokens             int
	SummarizedMessages      int
	TrimmedToolResults      int
	SummaryPromptTokens     int
	SummaryCompletionTokens int
	SummaryFallback         bool
	Forced                  bool
}

// StartInfo describes the beginning of a compaction pass.
type StartInfo struct {
	Window        int
	ReserveTokens int
	TriggerTokens int
	BeforeTokens  int
	Forced        bool
}

// FinishInfo describes the end of a compaction pass. Compaction may be nil
// when a pass was started but made no changes before an error.
type FinishInfo struct {
	Window       int
	BeforeTokens int
	AfterTokens  int
	Forced       bool
	Compaction   *Compaction
}

// Observer receives compaction progress callbacks. Callbacks run
// synchronously in the agent run goroutine.
type Observer interface {
	CompactionStarted(StartInfo)
	CompactionFinished(FinishInfo)
}

// Stats reports current context accounting.
type Stats struct {
	Window                   int
	ReserveTokens            int
	PromptBudget             int
	TriggerTokens            int
	EstimatedPromptTokens    int
	ReportedPromptTokens     int
	ReportedCompletionTokens int
	SummaryTokens            int
	SummaryPromptTokens      int
	SummaryCompletionTokens  int
	CompactedThrough         int
	LastCompaction           *Compaction
}

// Manager builds prompt views and compacts older history.
type Manager struct {
	cfg Config

	summary          string
	compactedThrough int

	estimateRatio   float64
	lastRawEstimate int
	lastEstimate    int

	reportedPrompt     int
	reportedCompletion int
	summaryPrompt      int
	summaryCompletion  int
	lastCompaction     *Compaction
	observer           Observer
}

// New creates a context manager.
func New(cfg Config) *Manager {
	cfg = cfg.normalized()
	return &Manager{
		cfg:           cfg,
		estimateRatio: 1,
	}
}

// SetObserver installs compaction progress callbacks.
func (m *Manager) SetObserver(observer Observer) {
	m.observer = observer
}

// Reset clears compaction state, for example after loading a new session.
func (m *Manager) Reset() {
	m.summary = ""
	m.compactedThrough = 0
	m.estimateRatio = 1
	m.lastRawEstimate = 0
	m.lastEstimate = 0
	m.reportedPrompt = 0
	m.reportedCompletion = 0
	m.summaryPrompt = 0
	m.summaryCompletion = 0
	m.lastCompaction = nil
}

// Window returns the configured context window.
func (m *Manager) Window() int { return m.cfg.Window }

// ReserveTokens returns the output reservation.
func (m *Manager) ReserveTokens() int { return m.cfg.ReserveTokens }

// Stats returns a snapshot of context accounting.
func (m *Manager) Stats() Stats {
	return Stats{
		Window:                   m.cfg.Window,
		ReserveTokens:            m.cfg.ReserveTokens,
		PromptBudget:             m.cfg.Window - m.cfg.ReserveTokens,
		TriggerTokens:            m.triggerTokens(),
		EstimatedPromptTokens:    m.lastEstimate,
		ReportedPromptTokens:     m.reportedPrompt,
		ReportedCompletionTokens: m.reportedCompletion,
		SummaryTokens:            estimateText(m.summary),
		SummaryPromptTokens:      m.summaryPrompt,
		SummaryCompletionTokens:  m.summaryCompletion,
		CompactedThrough:         m.compactedThrough,
		LastCompaction:           cloneCompaction(m.lastCompaction),
	}
}

func (m *Manager) triggerTokens() int {
	promptBudget := m.cfg.Window - m.cfg.ReserveTokens
	return max(1, int(float64(promptBudget)*m.cfg.CompactAt))
}

// ObserveUsage records reported provider usage and calibrates estimation.
func (m *Manager) ObserveUsage(usage provider.Usage) {
	if usage.PromptTokens > 0 {
		m.reportedPrompt = usage.PromptTokens
		if m.lastRawEstimate > 0 {
			ratio := float64(usage.PromptTokens) / float64(m.lastRawEstimate)
			if ratio < 0.5 {
				ratio = 0.5
			}
			if ratio > 2.0 {
				ratio = 2.0
			}
			m.estimateRatio = 0.7*m.estimateRatio + 0.3*ratio
		}
	}
	m.reportedCompletion = usage.CompletionTokens
	if m.lastRawEstimate > 0 {
		m.lastEstimate = m.calibrate(m.lastRawEstimate)
	}
}

// Build returns the messages to send for this request, compacting older
// history if necessary.
func (m *Manager) Build(
	ctx context.Context,
	history []provider.Message,
	tools []provider.Tool,
	summarize Summarizer,
) ([]provider.Message, *Compaction, error) {
	return m.build(ctx, history, tools, summarize, false)
}

// Force aggressively compacts after the provider rejected a prompt for
// exceeding its context window.
func (m *Manager) Force(
	ctx context.Context,
	history []provider.Message,
	tools []provider.Tool,
	summarize Summarizer,
) ([]provider.Message, *Compaction, error) {
	return m.build(ctx, history, tools, summarize, true)
}

func (m *Manager) build(
	ctx context.Context,
	history []provider.Message,
	tools []provider.Tool,
	summarize Summarizer,
	force bool,
) ([]provider.Message, *Compaction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(history) == 0 {
		return nil, nil, nil
	}

	systemCount := leadingSystemMessages(history)
	if m.compactedThrough < systemCount || m.compactedThrough > len(history) {
		m.summary = ""
		m.compactedThrough = systemCount
		m.lastCompaction = nil
	}
	if m.summary != "" &&
		(m.compactedThrough >= len(history) || history[m.compactedThrough].Role != "user") {
		m.summary = ""
		m.compactedThrough = systemCount
		m.lastCompaction = nil
	}
	if m.compactedThrough < systemCount {
		m.compactedThrough = systemCount
	}

	tailStart := m.compactedThrough
	if tailStart < systemCount {
		tailStart = systemCount
	}

	view := m.view(history, systemCount, tailStart)
	rawBefore := m.estimateRaw(view, tools)
	before := m.calibrate(rawBefore)
	m.lastEstimate = before

	budget := m.cfg.Window - m.cfg.ReserveTokens
	trigger := m.triggerTokens()

	var compaction *Compaction
	started := false

	ensureStarted := func() {
		if started {
			return
		}
		started = true
		m.observeStart(StartInfo{
			Window:        m.cfg.Window,
			ReserveTokens: m.cfg.ReserveTokens,
			TriggerTokens: trigger,
			BeforeTokens:  before,
			Forced:        force,
		})
	}

	defer func() {
		if !started {
			return
		}

		after := m.calibrate(m.estimateRaw(view, tools))
		if compaction != nil {
			compaction.BeforeTokens = before
			compaction.AfterTokens = after
			compaction.Forced = force
			m.lastCompaction = cloneCompaction(compaction)
		}
		m.observeFinish(FinishInfo{
			Window:       m.cfg.Window,
			BeforeTokens: before,
			AfterTokens:  after,
			Forced:       force,
			Compaction:   cloneCompaction(compaction),
		})
	}()

	merge := func(next *Compaction) {
		if next == nil {
			return
		}
		compaction = mergeCompaction(compaction, next)
	}

	if force || before > trigger {
		if !force {
			if cutoff := m.chooseCutoff(history, tailStart, tools); cutoff > tailStart {
				ensureStarted()
				next, err := m.compact(ctx, history, tailStart, cutoff, summarize)
				if err != nil {
					return nil, nil, err
				}
				merge(next)
				tailStart = cutoff
				view = m.view(history, systemCount, tailStart)
			}
		}

		current := m.calibrate(m.estimateRaw(view, tools))
		if force || current > budget {
			if cutoff := lastUserIndex(history); cutoff > tailStart {
				ensureStarted()
				next, err := m.compact(ctx, history, tailStart, cutoff, summarize)
				if err != nil {
					return nil, nil, err
				}
				merge(next)
				tailStart = cutoff
				view = m.view(history, systemCount, tailStart)
			}
		}

		if m.calibrate(m.estimateRaw(view, tools)) > trigger &&
			m.hasTrimmableToolResults(view) {
			ensureStarted()
			var trimmed int
			view, trimmed = m.trimToolResults(view, tools, trigger)
			if trimmed > 0 {
				merge(&Compaction{TrimmedToolResults: trimmed, Forced: force})
			}
		}
	}

	rawAfter := m.estimateRaw(view, tools)
	after := m.calibrate(rawAfter)
	m.lastRawEstimate = rawAfter
	m.lastEstimate = after

	if after > budget {
		return view, compaction, fmt.Errorf(
			"%w: estimated prompt=%d, budget=%d, window=%d, reserved=%d",
			ErrContextOverflow,
			after,
			budget,
			m.cfg.Window,
			m.cfg.ReserveTokens,
		)
	}

	return view, compaction, nil
}

func (m *Manager) observeStart(info StartInfo) {
	if m.observer != nil {
		m.observer.CompactionStarted(info)
	}
}

func (m *Manager) observeFinish(info FinishInfo) {
	if m.observer != nil {
		m.observer.CompactionFinished(info)
	}
}

func (m *Manager) hasTrimmableToolResults(view []provider.Message) bool {
	for _, message := range view {
		if message.Role != "tool" {
			continue
		}
		text, ok := message.Content.(string)
		if ok && len(text) > 400 {
			return true
		}
	}
	return false
}

func (m *Manager) compact(
	ctx context.Context,
	history []provider.Message,
	from, to int,
	summarize Summarizer,
) (*Compaction, error) {
	if to <= from {
		return nil, nil
	}

	transcript := formatTranscript(m.summary, history[from:to], m.transcriptBudget())
	result := SummarizeResult{}
	fallback := false

	if summarize != nil {
		produced, err := summarize(ctx, transcript, m.cfg.SummaryMaxTokens)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			fallback = true
		} else {
			result = produced
		}
	} else {
		fallback = true
	}

	summary := strings.TrimSpace(result.Summary)
	if summary == "" {
		fallback = true
		summary = fallbackSummary(m.summary, history[from:to], m.cfg.SummaryMaxTokens)
	}
	if strings.TrimSpace(summary) == "" {
		summary = "Earlier conversation was compacted; no summary could be generated."
		fallback = true
	}

	m.summary = summary
	m.compactedThrough = to
	m.summaryPrompt += result.Usage.PromptTokens
	m.summaryCompletion += result.Usage.CompletionTokens

	return &Compaction{
		SummarizedMessages:      to - from,
		SummaryPromptTokens:     result.Usage.PromptTokens,
		SummaryCompletionTokens: result.Usage.CompletionTokens,
		SummaryFallback:         fallback,
	}, nil
}

func (m *Manager) chooseCutoff(
	history []provider.Message,
	tailStart int,
	tools []provider.Tool,
) int {
	lastUser := lastUserIndex(history)
	if lastUser < tailStart {
		return tailStart
	}

	cutoff := lastUser
	for i := lastUser; i >= tailStart; i-- {
		if history[i].Role != "user" {
			continue
		}
		if m.calibrate(m.estimateRaw(history[i:], tools)) > m.cfg.KeepRecentTokens {
			break
		}
		cutoff = i
	}
	if cutoff <= tailStart {
		return tailStart
	}
	return cutoff
}

func (m *Manager) view(history []provider.Message, systemCount, tailStart int) []provider.Message {
	view := make([]provider.Message, 0, systemCount+1+len(history)-tailStart)
	view = append(view, history[:systemCount]...)

	if m.summary != "" {
		identifier := "[Compacted conversation summary of earlier turns]\n" + m.summary
		if len(view) > 0 && view[len(view)-1].Role == "system" {
			last := view[len(view)-1]
			text := textContent(last.Content)
			if text != "" {
				text += "\n\n"
			}
			last.Content = text + identifier
			view[len(view)-1] = last
		} else {
			view = append(view, provider.Message{Role: "system", Content: identifier})
		}
	}

	view = append(view, history[tailStart:]...)
	return view
}

func (m *Manager) trimToolResults(
	view []provider.Message,
	tools []provider.Tool,
	target int,
) ([]provider.Message, int) {
	trimmed := 0
	for m.calibrate(m.estimateRaw(view, tools)) > target {
		largest := -1
		largestLen := 0
		for i := range view {
			if view[i].Role != "tool" {
				continue
			}
			text, ok := view[i].Content.(string)
			if !ok || len(text) <= 400 {
				continue
			}
			if len(text) > largestLen {
				largest = i
				largestLen = len(text)
			}
		}
		if largest < 0 {
			break
		}

		text := view[largest].Content.(string)
		keep := len(text) / 2
		if keep < 200 {
			keep = 200
		}
		if keep >= len(text) {
			break
		}
		view[largest].Content = truncateText(text, keep) +
			"\n[...tool result truncated by context manager...]"
		trimmed++
	}
	return view, trimmed
}

func (m *Manager) transcriptBudget() int {
	budget := m.cfg.Window - m.cfg.ReserveTokens - m.cfg.SummaryMaxTokens - 1024
	if budget < minTranscriptTokens {
		budget = minTranscriptTokens
	}
	return budget
}

func (m *Manager) estimateRaw(messages []provider.Message, tools []provider.Tool) int {
	total := 0
	for _, message := range messages {
		total += 4
		total += estimateText(textContent(message.Content))
		total += estimateText(message.ReasoningContent)
		total += estimateText(message.ToolCallID)
		for _, call := range message.ToolCalls {
			total += 8
			total += estimateText(call.ID)
			total += estimateText(call.Function.Name)
			total += estimateText(call.Function.Arguments)
		}
	}

	for _, tool := range tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			total += 32
			continue
		}
		total += 8 + estimateText(string(encoded))
	}

	return total
}

func (m *Manager) calibrate(raw int) int {
	ratio := m.estimateRatio
	if ratio <= 0 {
		ratio = 1
	}
	estimate := int(float64(raw) * ratio)
	if estimate < 1 {
		estimate = 1
	}
	return estimate
}

func (c Config) normalized() Config {
	if c.Window <= 0 {
		c.Window = DefaultWindow
	}
	if c.Window < 1024 {
		c.Window = 1024
	}
	if c.ReserveTokens <= 0 {
		c.ReserveTokens = DefaultReserveTokens
	}
	if c.ReserveTokens >= c.Window {
		c.ReserveTokens = c.Window / 4
	}
	if c.ReserveTokens < 128 {
		c.ReserveTokens = 128
	}
	if c.CompactAt <= 0 || c.CompactAt > 1 {
		c.CompactAt = DefaultCompactAt
	}
	if c.CompactAt < 0.5 {
		c.CompactAt = 0.5
	}
	if c.CompactAt > 0.95 {
		c.CompactAt = 0.95
	}

	promptBudget := c.Window - c.ReserveTokens
	if c.KeepRecentTokens <= 0 {
		c.KeepRecentTokens = min(DefaultKeepRecentTokens, promptBudget)
	}
	if c.KeepRecentTokens > promptBudget {
		c.KeepRecentTokens = promptBudget
	}
	if c.KeepRecentTokens < promptBudget/4 {
		c.KeepRecentTokens = promptBudget / 4
	}
	if c.SummaryMaxTokens <= 0 {
		c.SummaryMaxTokens = DefaultSummaryMaxTokens
	}
	if c.SummaryMaxTokens > promptBudget/2 {
		c.SummaryMaxTokens = promptBudget / 2
	}
	if c.SummaryMaxTokens < 128 {
		c.SummaryMaxTokens = 128
	}

	return c
}

func leadingSystemMessages(history []provider.Message) int {
	count := 0
	for count < len(history) && history[count].Role == "system" {
		count++
	}
	return count
}

func lastUserIndex(history []provider.Message) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return i
		}
	}
	return -1
}

func estimateText(text string) int {
	if text == "" {
		return 0
	}
	bytes := len(text)
	runes := utf8.RuneCountInString(text)
	estimate := (bytes + 3) / 4
	// Non-ASCII text packs more bytes per rune; the byte heuristic would
	// underestimate those tokens, so fall back to roughly one token per rune.
	if bytes > runes*2 && runes > estimate {
		estimate = runes
	}
	if estimate < 1 {
		estimate = 1
	}
	return estimate
}

func textContent(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []provider.Content:
		var builder strings.Builder
		for _, part := range value {
			builder.WriteString(part.Text)
		}
		return builder.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func mergeCompaction(existing, next *Compaction) *Compaction {
	if existing == nil {
		cloned := *next
		return &cloned
	}
	existing.SummarizedMessages += next.SummarizedMessages
	existing.TrimmedToolResults += next.TrimmedToolResults
	existing.SummaryPromptTokens += next.SummaryPromptTokens
	existing.SummaryCompletionTokens += next.SummaryCompletionTokens
	existing.SummaryFallback = existing.SummaryFallback || next.SummaryFallback
	existing.Forced = existing.Forced || next.Forced
	return existing
}

func cloneCompaction(compaction *Compaction) *Compaction {
	if compaction == nil {
		return nil
	}
	cloned := *compaction
	return &cloned
}

func formatTranscript(previousSummary string, messages []provider.Message, budgetTokens int) string {
	var builder strings.Builder
	budgetChars := max(1024, budgetTokens*4)

	if previousSummary != "" {
		summary := previousSummary
		half := budgetChars / 2
		if len(summary) > half {
			summary = truncateText(summary, half)
		}
		builder.WriteString("Existing summary:\n")
		builder.WriteString(summary)
		builder.WriteString("\n\nNew messages to merge:\n")
	}

	for _, message := range messages {
		line := formatMessage(message, 2000)
		if line == "" {
			continue
		}
		if builder.Len()+len(line)+1 > budgetChars {
			builder.WriteString("\n[...older transcript truncated by context manager...]\n")
			break
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}

	if builder.Len() == 0 {
		return "(no older messages)"
	}
	return builder.String()
}

func fallbackSummary(previousSummary string, messages []provider.Message, maxTokens int) string {
	var builder strings.Builder

	if previousSummary != "" {
		builder.WriteString(previousSummary)
		builder.WriteString("\n\n")
	}
	builder.WriteString("Earlier conversation summary (automatic fallback):\n")

	budgetChars := max(512, maxTokens*3)
	for _, message := range messages {
		line := formatMessage(message, 400)
		if line == "" {
			continue
		}
		if builder.Len()+len(line)+1 > budgetChars {
			builder.WriteString("\n[...summary truncated...]")
			break
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}

func formatMessage(message provider.Message, limit int) string {
	var builder strings.Builder
	switch message.Role {
	case "user":
		builder.WriteString("User: ")
		builder.WriteString(truncateText(textContent(message.Content), limit))
	case "assistant":
		builder.WriteString("Assistant: ")
		builder.WriteString(truncateText(textContent(message.Content), limit))
		for _, call := range message.ToolCalls {
			builder.WriteString("\n  Tool call ")
			builder.WriteString(call.Function.Name)
			builder.WriteString("(")
			builder.WriteString(truncateText(call.Function.Arguments, limit/2))
			builder.WriteString(")")
		}
	case "tool":
		builder.WriteString("Tool result")
		if message.ToolCallID != "" {
			builder.WriteString(" ")
			builder.WriteString(message.ToolCallID)
		}
		builder.WriteString(": ")
		builder.WriteString(truncateText(textContent(message.Content), limit))
	default:
		return ""
	}
	return builder.String()
}

func truncateText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "...[truncated]"
}
