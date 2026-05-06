package notifier

import (
	"testing"
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
