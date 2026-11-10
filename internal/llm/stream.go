package llm

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

// maxRetryDelay bounds the exponential backoff between HTTP attempts.
const maxRetryDelay = 30 * time.Second

// defaultRetryBaseDelay is the first retry delay when LLMClient.RetryBaseDelay
// is not set.
const defaultRetryBaseDelay = time.Second

// toolCallAccumulator merges streamed tool-call deltas. OpenAI-compatible
// servers are inconsistent: most send an index for every delta, but some send
// full calls with only IDs, and others send no index or ID at all. The
// accumulator keeps calls separate when it can and appends argument fragments
// when a delta is clearly a continuation.
type toolCallAccumulator struct {
	calls []*toolCallAccum
}

type toolCallAccum struct {
	index    int
	hasIndex bool
	call     provider.ToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{}
}

// add merges one streaming tool-call delta.
func (a *toolCallAccumulator) add(delta provider.ToolCall) {
	call := a.find(delta)
	if call == nil {
		call = &toolCallAccum{}
		if delta.Index != nil {
			call.hasIndex = true
			call.index = *delta.Index
		}
		a.calls = append(a.calls, call)
	}

	if delta.ID != "" {
		call.call.ID = delta.ID
	}
	if delta.Type != "" {
		call.call.Type = delta.Type
	}
	if delta.Function.Name != "" {
		call.call.Function.Name = delta.Function.Name
	}
	call.call.Function.Arguments += delta.Function.Arguments
}

func (a *toolCallAccumulator) find(delta provider.ToolCall) *toolCallAccum {
	if delta.Index != nil {
		for _, call := range a.calls {
			if call.hasIndex && call.index == *delta.Index {
				return call
			}
		}
		// A server may send the first chunk with an ID only and later chunks
		// with an index. Adopt the indexed position for the existing call, but
		// do not merge a duplicate ID that is already bound to another index.
		if delta.ID != "" {
			for _, call := range a.calls {
				if call.call.ID == delta.ID && (!call.hasIndex || call.index == *delta.Index) {
					call.hasIndex = true
					call.index = *delta.Index
					return call
				}
			}
		}
		return nil
	}

	if delta.ID != "" {
		for _, call := range a.calls {
			if call.call.ID == delta.ID {
				return call
			}
		}
		return nil
	}

	if len(a.calls) == 0 {
		return nil
	}
	last := a.calls[len(a.calls)-1]

	// Arguments-only deltas continue the previous call. A repeated function
	// name also continues it while the accumulated arguments are still
	// incomplete, which is common in servers that resend the name on each
	// delta.
	if delta.Function.Name == "" {
		return last
	}
	if last.call.Function.Name == delta.Function.Name &&
		!json.Valid([]byte(last.call.Function.Arguments)) {
		return last
	}
	return nil
}

// calls returns the assembled calls in first-seen order. Streaming indexes are
// cleared because assembled responses do not use them.
func (a *toolCallAccumulator) callsResult() []provider.ToolCall {
	calls := make([]provider.ToolCall, 0, len(a.calls))
	for _, entry := range a.calls {
		call := entry.call
		call.Index = nil
		calls = append(calls, call)
	}
	return calls
}

// retryableStatus reports whether an HTTP status is transient. 4xx responses
// other than 408, 425, and 429 are deterministic request errors and must not be
// retried.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return status >= 500
	}
}

// parseRetryAfter parses a Retry-After header value expressed as seconds or an
// HTTP date.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := when.Sub(now)
		if delay <= 0 {
			return 0
		}
		return delay
	}
	return 0
}
