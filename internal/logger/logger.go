package logger

import (
	"encoding/json"
	"log/slog"
	"os"
)

type ExtendedLogger struct {
	*slog.Logger
}

type LLMMessage struct {
	Role    any `json:"role"`
	Content any `json:"content"`
}

type LLMChoice struct {
	Message any `json:"message"`
}

var Logger *ExtendedLogger
var levelVar = new(slog.LevelVar)

func init() {
	levelVar.Set(slog.LevelDebug)

	consoleHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelVar,
	})

	file, err := os.OpenFile("iris_debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		panic(err)
	}
	fileHandler := slog.NewJSONHandler(file, &slog.HandlerOptions{
		Level: levelVar,
	})

	Logger = &ExtendedLogger{slog.New(slog.NewMultiHandler(consoleHandler, fileHandler))}
}

func SetDebug(enable bool) {
	if enable {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
	Logger.Debug("Debug mode", "enabled", enable)
}

func (l *ExtendedLogger) LogLLMPayload(payload map[string]any) {
	l.Debug("===== LLM REQUEST =====>")

	l.Debug("payload", "payload", payload)

	l.Debug("meta",
		"model", payload["model"],
		"temperature", payload["temperature"],
		"max_tokens", payload["max_tokens"],
	)

	if messagesRaw, ok := payload["messages"].([]any); ok {
		for i, msg := range messagesRaw {
			msgMap, ok := msg.(map[string]any)
			if !ok {
				continue
			}

			role := msgMap["role"]
			content := msgMap["content"]

			Logger.Debug("Message", "index", i, "role", role)

			if str, ok := content.(string); ok {
				var parsed any
				if err := json.Unmarshal([]byte(str), &parsed); err == nil {
					Logger.Debug("Parsed JSON content", "content", parsed)
				} else {
					Logger.Debug("Content preview", "content", str)
				}
			} else {
				Logger.Debug("Non-string content", "content", content)
			}
		}
	}

	Logger.Debug("========================")
}

func (l *ExtendedLogger) LogLLMResponse(response string) {
	l.Debug("<==== LLM RESPONSE =====")

	var parsed any
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		l.Debug("Raw response (non-JSON)", "response", response)
		l.Debug("========================")
		return
	}

	l.Debug("parsed", "parsed", parsed)

	root, ok := parsed.(map[string]any)
	if !ok {
		l.Debug("response", "response", response)
		l.Debug("========================")
		return
	}

	if choicesRaw, ok := root["choices"].([]any); ok {
		for i, choice := range choicesRaw {
			choiceMap, ok := choice.(map[string]any)
			if !ok {
				continue
			}

			msg, ok := choiceMap["message"].(map[string]any)
			if !ok {
				continue
			}

			role := msg["role"]
			content := msg["content"]

			Logger.Debug("Choice", "index", i, "role", role)

			if str, ok := content.(string); ok {
				var parsed any
				if err := json.Unmarshal([]byte(str), &parsed); err == nil {
					Logger.Debug("Parsed JSON content", "content", parsed)
				} else {
					Logger.Debug("Content preview", "content", str)
				}
			} else {
				Logger.Debug("Non-string content", "content", content)
			}
		}
	}

	Logger.Info("Usage", "usage", root["usage"])

	if timings, ok := root["timings"].(map[string]any); ok {
		if tps, ok := timings["predicted_per_second"].(float64); ok {
			Logger.Info("Speed (tokens/sec)", "tps", float64(int(tps*100))/100)
		}
	}

	Logger.Debug("========================")
}
