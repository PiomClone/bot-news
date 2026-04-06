package logger

import (
	"log/slog"
	"os"
)

// Init настраивает глобальный slog-логгер с JSON-форматом.
// Уровень задаётся через переменную окружения LOG_LEVEL (debug/info/warn/error).
func Init(service string) {
	level := parseLevel(os.Getenv("LOG_LEVEL"))

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	logger := slog.New(handler).With(
		slog.String("service", service),
	)
	slog.SetDefault(logger)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
