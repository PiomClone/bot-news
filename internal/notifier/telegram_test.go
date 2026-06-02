package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	telebot "gopkg.in/telebot.v3"

	"bot-news/internal/storage"
)

func TestParseRecipient(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"@channel", "@channel", false},
		{"123456", "123456", false},
		{"-100123456", "-100123456", false},
		{"bad", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		got, err := parseRecipient(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseRecipient(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got.Recipient() != tt.want {
			t.Errorf("parseRecipient(%q) = %v, want %v", tt.input, got.Recipient(), tt.want)
		}
	}
}

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		maxLen  int
		wantLen int
	}{
		{"Short", "hello", 10, 1},
		{"Exact", "hello", 5, 1},
		{"Split on newline", "line1\nline2", 6, 2},
		{"Split middle of line", "1234567890", 5, 2},
		{"Many lines", "l1\nl2\nl3\nl4", 3, 4},
		{
			name:    "Keeps HTML balanced across chunks",
			text:    "<b>Тема</b>\n- <a href=\"https://example.com/1\">первая ссылка</a>\n- <a href=\"https://example.com/2\">вторая ссылка</a>\n- <a href=\"https://example.com/3\">третья ссылка</a>",
			maxLen:  70,
			wantLen: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitMessage(tt.text, tt.maxLen)
			if len(got) != tt.wantLen {
				t.Errorf("splitMessage() len = %d, want %d. Result: %v", len(got), tt.wantLen, got)
			}
			for _, chunk := range got {
				if len([]rune(chunk)) > tt.maxLen {
					t.Errorf("chunk too long: %q", chunk)
				}
			}
			if tt.name == "Keeps HTML balanced across chunks" {
				for _, chunk := range got {
					if strings.Count(chunk, "<a ") != strings.Count(chunk, "</a>") {
						t.Errorf("unbalanced link tag in chunk %q", chunk)
					}
					if strings.Count(chunk, "<b>") != strings.Count(chunk, "</b>") {
						t.Errorf("unbalanced bold tag in chunk %q", chunk)
					}
				}
			}
		})
	}
}

func TestSplitMessage_ReopensHTMLTags(t *testing.T) {
	text := "<b>" + strings.Repeat("Очень длинный заголовок ", 8) + "</b>"

	got := splitMessage(text, 40)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %v", got)
	}

	if !strings.HasPrefix(got[1], "<b>") {
		t.Fatalf("second chunk should reopen <b>: %q", got[1])
	}
	if !strings.Contains(got[0], "</b>") {
		t.Fatalf("first chunk should close <b>: %q", got[0])
	}
}

func TestSourceLabel(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://t.me/s/channel", "@channel"},
		{"https://rsshub.app/telegram/channel/name", "@name"},
		{"http://example.com/feed.xml", "@feed.xml"},
		{"", "@unknown"},
		{"invalid-url", "@invalid-url"},
	}

	for _, tt := range tests {
		if got := sourceLabel(tt.url); got != tt.want {
			t.Errorf("sourceLabel(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestMakeFeedsKeyboard(t *testing.T) {
	tg := &Telegram{feedMap: make(map[string]string)}
	feeds := []storage.Feed{
		{URL: "http://f1", Title: "Feed 1", Active: true},
		{URL: "http://f2", Title: "", Active: false},
	}

	kb := tg.makeFeedsKeyboard(feeds)
	if kb == nil {
		t.Fatal("keyboard is nil")
	}

	if len(kb.InlineKeyboard) != 2 {
		t.Errorf("ожидали 2 строки кнопок, получили %d", len(kb.InlineKeyboard))
	}

	// Проверяем текст первой кнопки
	btn1 := kb.InlineKeyboard[0][0]
	if !strings.Contains(btn1.Text, "✅") || !strings.Contains(btn1.Text, "Feed 1") {
		t.Errorf("неверный текст кнопки 1: %q", btn1.Text)
	}

	// Проверяем текст второй кнопки (должен использовать sourceLabel)
	btn2 := kb.InlineKeyboard[1][0]
	if !strings.Contains(btn2.Text, "❌") || !strings.Contains(btn2.Text, "@f2") {
		t.Errorf("неверный текст кнопки 2: %q", btn2.Text)
	}
}

func TestTelegram_Send(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	// Мокаем эндпоинт sendMessage
	mux.HandleFunc("/bot123/sendMessage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 1,
			},
		})
	})

	bot, err := telebot.NewBot(telebot.Settings{
		Token:   "123",
		URL:     server.URL,
		Offline: true,
	})
	if err != nil {
		t.Fatalf("не удалось создать бота: %v", err)
	}

	tg := &Telegram{
		bot:  bot,
		chat: numericRecipient(12345),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	t.Run("Send success", func(t *testing.T) {
		if err := tg.Send(ctx, "test message"); err != nil {
			t.Errorf("Send: %v", err)
		}
	})

	t.Run("SendToAdmin", func(t *testing.T) {
		if err := tg.SendToAdmin(ctx, 54321, "admin message"); err != nil {
			t.Errorf("SendToAdmin: %v", err)
		}
	})

	t.Run("SendToAdmin skip", func(t *testing.T) {
		if err := tg.SendToAdmin(ctx, 0, "admin message"); err != nil {
			t.Errorf("SendToAdmin(0): %v", err)
		}
	})
}

func TestTelegram_ListenCommands(_ *testing.T) {
	// Мы не можем легко протестировать polling, но можем протестировать инициализацию команд.
	bot, _ := telebot.NewBot(telebot.Settings{
		Token:   "123",
		Offline: true,
	})
	tg := &Telegram{bot: bot}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Сразу отменяем, чтобы ListenCommands завершился после старта

	tg.ListenCommands(ctx, 123,
		func(string) {}, func() {},
		func() string { return "" }, func() string { return "" },
		func() int { return 0 },
		func() ([]storage.Feed, error) { return nil, nil },
		func(string) error { return nil },
	)
}

func TestNewTelegram_Error(t *testing.T) {
	_, err := NewTelegram("", "bad")
	if err == nil {
		t.Error("ожидали ошибку при пустом токене")
	}
}
