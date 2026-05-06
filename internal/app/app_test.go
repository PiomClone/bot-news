package app

import (
	"testing"

	"bot-news/internal/config"
)

func TestNewApp_Validation(t *testing.T) {
	// Мы не можем легко протестировать NewApp полностью без реальной БД или моков,
	// но мы можем проверить, что он падает на невалидном конфиге (пути к БД).
	cfg := config.Config{
		DBPath: "/non-existent/path/to/db.db",
	}

	_, err := NewApp(cfg)
	if err == nil {
		t.Error("ожидали ошибку при невалидном пути к БД, получили nil")
	}
}
