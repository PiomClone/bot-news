package logger

import (
	"log/slog"
	"os"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}

	for _, tt := range tests {
		if got := parseLevel(tt.input); got != tt.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestInit(_ *testing.T) {
	// Сохраним текущий дефолтный логгер
	old := slog.Default()
	defer slog.SetDefault(old)

	os.Setenv("LOG_LEVEL", "debug")
	defer os.Unsetenv("LOG_LEVEL")

	Init("test-service")

	// Мы не можем легко проверить внутренности SetDefault,
	// но мы можем проверить, что вызов не падает.
	slog.Info("test log after init")
}
