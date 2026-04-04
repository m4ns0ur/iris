package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

func LogLLMPayload(payload Payload) {
	Logger.Debug("===== LLM REQUEST =====>")

	Logger.Debug("payload", "payload", payload)

	Logger.Debug("meta",
		"model", payload.Model,
		"temperature", payload.Temperature,
		"max_tokens", payload.MaxTokens,
	)

	for i, msg := range payload.Messages {
		role := msg.Role
		content := msg.Content

		Logger.Debug("Message", "index", i, "role", role)

		var parsed any
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			Logger.Debug("Parsed JSON content", "content", parsed)
			fmt.Printf("Content preview:\n%s\n%s\n", strings.Repeat("-", 16), parsed)
		} else {
			Logger.Debug("Content preview", "content", content)
			fmt.Printf("Content preview:\n%s\n%s\n", strings.Repeat("-", 16), content)
		}
	}

	Logger.Debug(strings.Repeat("=", 24))
}

func LogLLMResponse(response LLMResponse) {
	Logger.Debug("<==== LLM RESPONSE =====")

	Logger.Debug("parsed", "parsed", response)

	for i, choice := range response.Choices {
		role := choice.Message.Role
		content := choice.Message.Content

		Logger.Debug("Choice", "index", i, "role", role)

		var parsed any
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			Logger.Debug("Parsed JSON content", "content", parsed)
			fmt.Printf("Content preview:\n%s\n%s\n", strings.Repeat("-", 16), parsed)
		} else {
			Logger.Debug("Content preview", "content", content)
			fmt.Printf("Content preview:\n%s\n%s\n", strings.Repeat("-", 16), content)
		}
	}

	Logger.Info("Usage", "usage", response.Usage)

	tps := response.Timings.PredictedPerSecond
	Logger.Info("Speed (tokens/sec)", "tps", float64(int(tps*100))/100)

	Logger.Debug(strings.Repeat("=", 24))
}
