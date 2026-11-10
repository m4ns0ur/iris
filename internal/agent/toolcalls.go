package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/m4ns0ur/iris/internal/tool"
	"github.com/m4ns0ur/iris/provider"
)

// Tool failure codes emitted in ToolFailed.Code and in the JSON tool result
// returned to the model.
const (
	toolCodeInvalidCall        = "invalid_tool_call"
	toolCodeInvalidArguments   = "invalid_tool_arguments"
	toolCodeTruncatedArguments = "truncated_tool_arguments"
	toolCodeUnknownTool        = "unknown_tool"
	toolCodeExecutionFailed    = "tool_execution_failed"
	toolCodeTimeout            = "tool_timeout"
	toolCodeCanceled           = "run_canceled"
	toolCodeBudgetExhausted    = "tool_budget_exhausted"
	toolCodeEncodeFailed       = "tool_result_encode_failed"
)

// PendingToolCall is one tool call prepared from a model response. SkipCode is
// non-empty when the call must not be executed (invalid, truncated, unknown, or
// otherwise rejected before execution).
type PendingToolCall struct {
	ID          string
	Name        string
	Args        map[string]any
	SkipCode    string
	SkipMessage string
}

// prepareToolCalls validates a model response's tool calls and returns the
// sanitized calls to store in assistant history plus the calls to execute or
// report as failed.
//
// The assistant history must remain provider-valid even when the model emitted
// broken arguments, so invalid or truncated arguments are replaced with "{}".
// The structured tool result carries the original error. Truncated responses
// (finish_reason "length") never execute any tool.
func prepareToolCalls(
	response provider.Response,
	registry *tool.ToolRegistry,
	turn int,
) ([]provider.ToolCall, []PendingToolCall) {
	truncated := provider.IsTruncatedFinish(response.FinishReason)
	blocked := strings.EqualFold(strings.TrimSpace(response.FinishReason), "content_filter")
	usedIDs := make(map[string]struct{}, len(response.ToolCalls))
	assistantCalls := make([]provider.ToolCall, 0, len(response.ToolCalls))
	pending := make([]PendingToolCall, 0, len(response.ToolCalls))

	for index, raw := range response.ToolCalls {
		id := normalizeToolCallID(raw.ID, usedIDs, turn, index)
		name, nameOK := normalizeToolName(raw.Function.Name, registry)

		args, argsOK, argsErr := parseToolArguments(raw.Function.Arguments)
		canonicalArgs := "{}"
		if argsOK {
			if encoded, err := json.Marshal(args); err == nil {
				canonicalArgs = string(encoded)
			}
		}

		call := PendingToolCall{
			ID:   id,
			Name: name,
			Args: args,
		}
		assistantCall := provider.ToolCall{
			ID:   id,
			Type: "function",
			Function: provider.FunctionCall{
				Name:      name,
				Arguments: canonicalArgs,
			},
		}

		switch {
		case truncated:
			markSkipped(&call, &assistantCall, toolCodeTruncatedArguments,
				"tool arguments were truncated because the response hit the output token limit; re-issue the complete tool call")
		case blocked:
			markSkipped(&call, &assistantCall, toolCodeInvalidCall,
				"tool calls were blocked by the provider content filter")
		case !nameOK:
			call.Name = "invalid_tool"
			assistantCall.Function.Name = "invalid_tool"
			markSkipped(&call, &assistantCall, toolCodeInvalidCall,
				fmt.Sprintf("tool call %d has no usable function name %q", index, raw.Function.Name))
		case !argsOK:
			markSkipped(&call, &assistantCall, toolCodeInvalidArguments,
				fmt.Sprintf("invalid JSON arguments: %v", argsErr))
		case registry == nil || registry.Get(name) == nil:
			// Keep valid arguments in history so the model can see what it
			// tried; the tool itself is never invoked.
			call.SkipCode = toolCodeUnknownTool
			call.SkipMessage = fmt.Sprintf("unknown tool %q", name)
		default:
			if err := validateToolArguments(registry.Get(name).Parameters(), args); err != nil {
				call.SkipCode = toolCodeInvalidArguments
				call.SkipMessage = err.Error()
			}
		}

		assistantCalls = append(assistantCalls, assistantCall)
		pending = append(pending, call)
	}

	return assistantCalls, pending
}

func markSkipped(
	call *PendingToolCall,
	assistantCall *provider.ToolCall,
	code string,
	message string,
) {
	call.SkipCode = code
	call.SkipMessage = message
	// Keep assistant history valid for the next provider request: a tool call
	// with truncated or malformed JSON arguments can be rejected outright.
	assistantCall.Function.Arguments = "{}"
	call.Args = map[string]any{}
}

func normalizeToolCallID(id string, used map[string]struct{}, turn, index int) string {
	id = strings.TrimSpace(id)
	if id != "" {
		if _, exists := used[id]; !exists {
			used[id] = struct{}{}
			return id
		}
	}

	base := fmt.Sprintf("call_%d_%d", turn, index)
	candidate := base
	for suffix := 1; ; suffix++ {
		if _, exists := used[candidate]; !exists {
			break
		}
		candidate = fmt.Sprintf("%s_%d", base, suffix)
	}
	used[candidate] = struct{}{}
	return candidate
}

func normalizeToolName(name string, registry *tool.ToolRegistry) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "invalid_tool", false
	}
	// A registered tool is authoritative even if its name uses characters the
	// OpenAI schema would not normally allow. Unknown names must be safe to
	// place in assistant history for the next request.
	if registry == nil || registry.Get(trimmed) == nil {
		if !isFunctionName(trimmed) {
			return "invalid_tool", false
		}
	}
	return trimmed, true
}

func isFunctionName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '_' || character == '-':
		default:
			return false
		}
	}
	return true
}

func parseToolArguments(raw string) (map[string]any, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, true, nil
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return map[string]any{}, false, err
	}
	if args == nil {
		return map[string]any{}, false, fmt.Errorf("arguments must be a JSON object")
	}
	return args, true, nil
}

// validateToolArguments applies the top-level required/type checks declared by
// a tool schema. Tools still validate nested values themselves.
func validateToolArguments(params provider.Parameters, args map[string]any) error {
	for _, name := range params.Required {
		value, ok := args[name]
		if !ok || value == nil {
			return fmt.Errorf("missing required argument %q", name)
		}
	}

	for name, property := range params.Properties {
		value, ok := args[name]
		if !ok || value == nil {
			continue
		}
		if err := validateArgumentType(name, property.Type, value); err != nil {
			return err
		}
	}
	return nil
}

func validateArgumentType(name, want string, value any) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return nil
	}

	matches := false
	switch want {
	case "string":
		_, matches = value.(string)
	case "boolean":
		_, matches = value.(bool)
	case "number":
		matches = isNumber(value)
	case "integer":
		matches = isNumber(value) && isIntegralNumber(value)
	case "array":
		matches = value != nil && reflect.ValueOf(value).Kind() == reflect.Slice
	case "object":
		_, matches = value.(map[string]any)
	default:
		// Unknown schema keywords are not enforced by this best-effort check.
		return nil
	}

	if !matches {
		return fmt.Errorf("argument %q must be a %s, got %T", name, want, value)
	}
	return nil
}

func isNumber(value any) bool {
	switch value.(type) {
	case float32, float64, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func isIntegralNumber(value any) bool {
	switch number := value.(type) {
	case float32:
		converted := float64(number)
		return !math.IsInf(converted, 0) && !math.IsNaN(converted) && converted == math.Trunc(converted)
	case float64:
		return !math.IsInf(number, 0) && !math.IsNaN(number) && number == math.Trunc(number)
	default:
		return true
	}
}

// toolErrorContent is the structured result appended to conversation history
// when a tool call fails or is rejected. The model sees whether the tool ran,
// whether retrying is safe, and a stable error code.
func toolErrorContent(call PendingToolCall, code, message string, executed, retryable bool) string {
	payload := struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			Tool       string `json:"tool,omitempty"`
			ToolCallID string `json:"tool_call_id,omitempty"`
			Executed   bool   `json:"executed"`
			Retryable  bool   `json:"retryable"`
		} `json:"error"`
	}{
		OK: false,
	}
	payload.Error.Code = code
	payload.Error.Message = message
	payload.Error.Tool = call.Name
	payload.Error.ToolCallID = call.ID
	payload.Error.Executed = executed
	payload.Error.Retryable = retryable

	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"error":{"code":"tool_result_encode_failed","message":"could not encode the tool error","executed":false,"retryable":false}}`
	}
	return string(encoded)
}

func retryableSkip(code string) bool {
	return code == toolCodeInvalidArguments || code == toolCodeTruncatedArguments
}
