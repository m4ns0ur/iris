package logger

import (
	"log/slog"
	"os"
)

var (
	Logger   *slog.Logger
	levelVar = new(slog.LevelVar)
)

type LLMMessage struct {
	Role    any `json:"role"`
	Content any `json:"content"`
}

type LLMChoice struct {
	Message any `json:"message"`
}

func init() {
	levelVar.Set(slog.LevelDebug)

	// Logs include prompts and tool output, so keep them owner-only.
	file, err := os.OpenFile("iris_debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		panic(err)
	}
	_ = file.Chmod(0o600)
	fileHandler := slog.NewJSONHandler(file, &slog.HandlerOptions{Level: levelVar})

	Logger = slog.New(slog.NewMultiHandler(fileHandler))
}

func SetDebug(enable bool) {
	if enable {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
	Logger.Debug("Debug mode enabled")
}
