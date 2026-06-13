package llm

import (
	"encoding/json"
)

func LogLLMPayload(payload Payload) {
	Logger.Debug("payload", "payload", payload)

	Logger.Debug(
		"Model parameters",
		"model", payload.Model,
		"temperature", payload.Temperature,
		"maxTokens", payload.MaxTokens,
	)

	for i, msg := range payload.Messages {
		role := msg.Role
		Logger.Debug("Message", "index", i, "role", role)

		var contentStr string

		switch v := msg.Content.(type) {
		case string:
			contentStr = v
		case []Content:
			if len(v) > 0 {
				contentStr = v[0].Text
			}
		}

		Logger.Debug("Content preview", "content", contentStr)
	}
}

func LogLLMResponse(response LLMResponse) {
	Logger.Debug("LLM Response", "response", response)

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
	Logger.Info("Speed", "tps", float64(int(tps*100))/100)
}
