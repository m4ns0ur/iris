package tool

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/m4ns0ur/iris/internal/policy"
)

// outputPolicy bounds the model-visible text returned by any tool. The full
// output is stored separately so the model can retrieve omitted content.
type outputPolicy struct {
	maxBytes        int
	maxLines        int
	maxCaptureBytes int
}

func newOutputPolicy(options policy.ShellPolicy) outputPolicy {
	normalized := outputPolicy{
		maxBytes:        options.MaxOutputBytes,
		maxLines:        options.MaxOutputLines,
		maxCaptureBytes: options.MaxCaptureBytes,
	}
	if normalized.maxBytes <= 0 {
		normalized.maxBytes = 25 * 1024
	}
	if normalized.maxLines <= 0 {
		normalized.maxLines = 1000
	}
	if normalized.maxCaptureBytes <= 0 {
		normalized.maxCaptureBytes = 10 * 1024 * 1024
	}
	return normalized
}

// applyOutput bounds every tool result, not just shell output. A tool may
// already provide a full output file (shell) and its own line metadata; in
// that case the common layer adds the standard notice without creating a
// second copy.
func (r *ToolRegistry) applyOutput(name string, result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	// Read is the retrieval mechanism: it has its own line/byte paging and a
	// source path. Applying the common cap here would make it impossible to
	// page through the full output it is meant to retrieve.
	if name == "read" {
		return result
	}
	if notice, _ := result["truncation_notice"].(string); strings.TrimSpace(notice) != "" {
		return r.limitSerializedResult(name, result)
	}

	key, text := primaryOutput(result)
	if key == "" {
		return r.limitSerializedResult(name, result)
	}

	if path, _ := result["full_output_file"].(string); path != "" {
		if truncated, _ := result["truncated"].(bool); truncated {
			result["truncation_notice"] = noticeForExistingTruncation(result, path)
			return r.limitSerializedResult(name, result)
		}
	}

	if !exceedsOutput(text, r.output) {
		return r.limitSerializedResult(name, result)
	}

	path := ""
	fullBytes := int64(0)
	fullTruncated := false
	if r.temp != nil {
		savedPath, written, truncated, err := r.temp.SaveText(name, []byte(text), r.output.maxCaptureBytes)
		if err == nil {
			path = savedPath
			fullBytes = written
			fullTruncated = truncated
		}
	}

	preview, notice, metadata := truncateText(text, r.output, path, fullTruncated)
	result[key] = preview
	result["truncated"] = true
	result["truncation_notice"] = notice
	if path != "" {
		result["full_output_file"] = path
		result["full_output_bytes"] = fullBytes
		result["full_output_truncated"] = fullTruncated
	}
	for metadataKey, metadataValue := range metadata {
		result[metadataKey] = metadataValue
	}
	return r.limitSerializedResult(name, result)
}

// limitSerializedResult is a final safety net for results that are large even
// without one oversized text field (for example, a huge array). It stores the
// pretty-printed result and returns a bounded preview.
func (r *ToolRegistry) limitSerializedResult(name string, result map[string]any) map[string]any {
	if result == nil || r.output.maxBytes <= 0 {
		return result
	}
	serialized, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result
	}
	tolerance := r.output.maxBytes / 8
	if tolerance < 1024 {
		tolerance = 1024
	}
	if len(serialized) <= r.output.maxBytes+tolerance {
		return result
	}

	path := ""
	fullBytes := int64(0)
	fullTruncated := false
	if r.temp != nil {
		savedPath, written, truncated, saveErr := r.temp.SaveText(name+"-result", serialized, r.output.maxCaptureBytes)
		if saveErr == nil {
			path = savedPath
			fullBytes = written
			fullTruncated = truncated
		}
	}
	preview, notice, metadata := truncateText(string(serialized), r.output, path, fullTruncated)
	capped := map[string]any{
		"output":            preview,
		"truncated":         true,
		"truncation_notice": "Tool result was too large to include in full.\n" + notice,
	}
	if path != "" {
		capped["full_output_file"] = path
		capped["full_output_bytes"] = fullBytes
		capped["full_output_truncated"] = fullTruncated
	}
	for metadataKey, metadataValue := range metadata {
		capped[metadataKey] = metadataValue
	}
	return capped
}

func primaryOutput(result map[string]any) (string, string) {
	for _, key := range []string{"output", "content"} {
		if value, ok := result[key].(string); ok && value != "" {
			return key, value
		}
	}

	key := ""
	text := ""
	for candidate, value := range result {
		valueText, ok := value.(string)
		if !ok || len(valueText) <= len(text) {
			continue
		}
		key = candidate
		text = valueText
	}
	return key, text
}

func exceedsOutput(text string, policy outputPolicy) bool {
	if policy.maxBytes > 0 && len(text) > policy.maxBytes {
		return true
	}
	return policy.maxLines > 0 && countLines(text) > policy.maxLines
}

func noticeForExistingTruncation(result map[string]any, path string) string {
	totalLines := intValue(result, "total_output_lines")
	if totalLines == 0 {
		return truncationNotice(0, 0, 0, 0, 0, path, false)
	}
	startLine := intValue(result, "output_start_line")
	endLine := intValue(result, "output_end_line")
	omittedLines := intValue(result, "output_omitted_lines")
	omittedBytes := int64(intValue(result, "output_omitted_bytes"))
	fullTruncated, _ := result["full_output_truncated"].(bool)
	return truncationNotice(startLine, endLine, totalLines, omittedLines, omittedBytes, path, fullTruncated)
}

func truncateText(text string, policy outputPolicy, path string, fullTruncated bool) (string, string, map[string]any) {
	totalLines := countLines(text)
	headCount, tailCount := outputLineCounts(totalLines, policy.maxLines)

	lines := splitOutputLines(text)
	head := strings.Join(lines[:headCount], "")
	tail := strings.Join(lines[len(lines)-tailCount:], "")
	if tailCount == 0 {
		tail = ""
	}
	if headCount == 0 {
		head = ""
	}

	if policy.maxBytes > 0 && len(head)+len(tail) > policy.maxBytes {
		headBudget := policy.maxBytes / 5
		tailBudget := policy.maxBytes - headBudget
		head = trimHeadBytes(head, headBudget)
		tail = trimTailBytes(tail, tailBudget)
	}

	shownHeadLines := countLines(head)
	shownTailLines := countLines(tail)
	shownLines := shownHeadLines + shownTailLines
	omittedLines := totalLines - shownLines
	if omittedLines < 0 {
		omittedLines = 0
	}
	omittedBytes := int64(len(text) - len(head) - len(tail))
	if omittedBytes < 0 {
		omittedBytes = 0
	}

	marker := "[... output truncated"
	if omittedLines > 0 {
		marker += fmt.Sprintf(": %s lines omitted", formatCount(omittedLines))
	} else if omittedBytes > 0 {
		marker += fmt.Sprintf(": %s bytes omitted", formatCount64(omittedBytes))
	}
	if path != "" {
		marker += "; full output: " + path
	}
	marker += " ...]"

	var preview strings.Builder
	if head != "" {
		preview.WriteString(head)
		if !strings.HasSuffix(head, "\n") {
			preview.WriteByte('\n')
		}
	}
	preview.WriteString(marker)
	preview.WriteByte('\n')
	preview.WriteString(tail)

	startLine := 0
	endLine := 0
	tailStartLine := 0
	if shownHeadLines > 0 {
		startLine = 1
		endLine = shownHeadLines
	}
	if shownTailLines > 0 {
		tailStartLine = totalLines - shownTailLines + 1
		if startLine == 0 {
			startLine = tailStartLine
		}
		endLine = totalLines
	}
	notice := truncationNotice(
		startLine,
		endLine,
		totalLines,
		omittedLines,
		omittedBytes,
		path,
		fullTruncated,
	)
	if shownHeadLines > 0 && shownTailLines > 0 {
		notice = fmt.Sprintf(
			"Output truncated.\nShowing lines %s-%s and %s-%s of %s.\n",
			formatCount(1),
			formatCount(shownHeadLines),
			formatCount(tailStartLine),
			formatCount(totalLines),
			formatCount(totalLines),
		) + truncationDetails(omittedLines, omittedBytes, path, fullTruncated)
	}

	metadata := map[string]any{
		"output_total_lines":     totalLines,
		"output_total_bytes":     len(text),
		"output_shown_lines":     shownLines,
		"output_omitted_lines":   omittedLines,
		"output_omitted_bytes":   omittedBytes,
		"output_head_end_line":   shownHeadLines,
		"output_tail_start_line": tailStartLine,
		"output_tail_end_line":   totalLines,
	}
	return preview.String(), notice, metadata
}

func truncationNotice(
	startLine int,
	endLine int,
	totalLines int,
	omittedLines int,
	omittedBytes int64,
	path string,
	fullTruncated bool,
) string {
	if totalLines <= 0 {
		return truncationDetails(omittedLines, omittedBytes, path, fullTruncated)
	}
	return fmt.Sprintf(
		"Output truncated.\nShowing lines %s-%s of %s.\n",
		formatCount(startLine),
		formatCount(endLine),
		formatCount(totalLines),
	) + truncationDetails(omittedLines, omittedBytes, path, fullTruncated)
}

func truncationDetails(omittedLines int, omittedBytes int64, path string, fullTruncated bool) string {
	var builder strings.Builder
	if omittedLines > 0 || omittedBytes > 0 {
		if omittedLines > 0 {
			fmt.Fprintf(&builder, "%s lines omitted", formatCount(omittedLines))
		}
		if omittedBytes > 0 {
			if builder.Len() > 0 {
				builder.WriteString("; ")
			}
			fmt.Fprintf(&builder, "%s bytes omitted", formatCount64(omittedBytes))
		}
		builder.WriteByte('\n')
	}
	if path == "" {
		builder.WriteString("Full output was not stored.\n")
		return builder.String()
	}
	builder.WriteString("Full output:\n")
	builder.WriteString(path)
	builder.WriteByte('\n')
	if fullTruncated {
		builder.WriteString("The stored full output is capped at the configured capture limit.\n")
	}
	return builder.String()
}

func outputLineCounts(totalLines int, maxLines int) (int, int) {
	if totalLines <= 0 {
		return 0, 0
	}
	if maxLines <= 0 || totalLines <= maxLines {
		head := totalLines / 5
		return head, totalLines - head
	}
	head := maxLines / 5
	if head < 0 {
		head = 0
	}
	tail := maxLines - head
	if tail < 0 {
		tail = 0
	}
	return head, tail
}

func splitOutputLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	lines := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		lines++
	}
	return lines
}

func trimHeadBytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	head := text[:maxBytes]
	if index := strings.LastIndexByte(head, '\n'); index >= 0 {
		return head[:index+1]
	}
	return head
}

func trimTailBytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	if index := strings.IndexByte(text[start:], '\n'); index >= 0 {
		return text[start+index+1:]
	}
	return text[start:]
}

func intValue(result map[string]any, key string) int {
	switch value := result[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func formatCount(value int) string {
	return formatInt64(int64(value))
}

func formatCount64(value int64) string {
	return formatInt64(value)
}

func formatInt64(value int64) string {
	text := strconv.FormatInt(value, 10)
	if len(text) <= 3 {
		return text
	}
	var builder strings.Builder
	offset := len(text) % 3
	if offset > 0 {
		builder.WriteString(text[:offset])
	}
	for index := offset; index < len(text); index += 3 {
		if builder.Len() > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(text[index : index+3])
	}
	return builder.String()
}
