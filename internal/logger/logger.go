package logger

import (
	"log/slog"
	"os"
)

type LLMMessage struct {
	Role    any `json:"role"`
	Content any `json:"content"`
}

type LLMChoice struct {
	Message any `json:"message"`
}

var Logger *slog.Logger
var levelVar = new(slog.LevelVar)

func init() {
	levelVar.Set(slog.LevelDebug)

	// consoleHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	// 	Level: levelVar,
	// })

	file, err := os.OpenFile("iris_debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		panic(err)
	}
	fileHandler := slog.NewJSONHandler(file, &slog.HandlerOptions{
		Level: levelVar,
	})

	Logger = slog.New(slog.NewMultiHandler( /*consoleHandler,*/ fileHandler))
}

func SetDebug(enable bool) {
	if enable {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
	Logger.Debug("Debug mode", "enabled", enable)
}
