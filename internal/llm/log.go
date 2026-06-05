package llm

import (
	"encoding/json"
)

func LogLLMPayload(payload Payload) {
	Logger.Debug("payload", "payload", payload)

	Logger.Debug("meta",
		"model", payload.Model,
		"temperature", payload.Temperature,
		"max_tokens", payload.MaxTokens,
	)

	for i, msg := range payload.Messages {
		role := msg.Role
		Logger.Debug("Message", "index", i, "role", role)

		var contentStr string

		switch v := msg.Content.(type) {
		case string:
			contentStr = v
		case []any:
			if len(v) > 0 {
				// Assuming Content[0].Text equivalent structure in an array of maps
				if firstItem, ok := v[0].(map[string]any); ok {
					if textVal, exists := firstItem["text"].(string); exists {
						contentStr = textVal
					}
				}
			}
		case []map[string]any: // Optional specific type variant depending on JSON unmarshaling setup
			if len(v) > 0 {
				if textVal, exists := v[0]["text"].(string); exists {
					contentStr = textVal
				}
			}
		}

		var parsed any
		if err := json.Unmarshal([]byte(contentStr), &parsed); err == nil {
			Logger.Debug("Parsed JSON content", "content", parsed)
		} else {
			Logger.Debug("Content preview", "content", contentStr)
		}
	}
}

func LogLLMResponse(response LLMResponse) {
	Logger.Debug("parsed", "parsed", response)

	for i, choice := range response.Choices {
		role := choice.Message.Role
		content := choice.Message.Content

		Logger.Debug("Choice", "index", i, "role", role)

		var parsed any
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			Logger.Debug("Parsed JSON content", "content", parsed)
		} else {
			Logger.Debug("Content preview", "content", content)
		}
	}

	Logger.Info("Usage", "usage", response.Usage)

	tps := response.Timings.PredictedPerSecond
	Logger.Info("Speed (tokens/sec)", "tps", float64(int(tps*100))/100)
}
