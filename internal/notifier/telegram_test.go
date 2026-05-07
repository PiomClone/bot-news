package notifier

import (
	"strings"
	"testing"

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
		})
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
		{URL: "http://f1", Title: "Feed 1", Enabled: true},
		{URL: "http://f2", Title: "", Enabled: false},
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
