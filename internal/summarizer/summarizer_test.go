package summarizer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot-news/internal/storage"
	"bot-news/internal/summarizer"
)

func TestSimpleSummarizer_Empty(t *testing.T) {
	s := summarizer.NewSimple()
	text, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if text != "" {
		t.Fatalf("ожидали пустую строку для пустого списка, получили %q", text)
	}
}

func TestSimpleSummarizer_ContainsTitlesAndLinks(t *testing.T) {
	s := summarizer.NewSimple()
	articles := []storage.Article{
		{Title: "Заголовок первый", Link: "https://example.com/1", FetchedAt: time.Now()},
		{Title: "Заголовок второй", Link: "https://example.com/2", FetchedAt: time.Now()},
	}

	text, err := s.Summarize(context.Background(), articles)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if !strings.Contains(text, "Заголовок первый") {
		t.Errorf("текст не содержит первый заголовок: %s", text)
	}
	if !strings.Contains(text, "https://example.com/2") {
		t.Errorf("текст не содержит вторую ссылку: %s", text)
	}
	if !strings.Contains(text, "2") {
		t.Errorf("текст не содержит количество статей: %s", text)
	}
}

func TestSimpleSummarizer_ArticleWithoutLink(t *testing.T) {
	s := summarizer.NewSimple()
	articles := []storage.Article{
		{Title: "Статья без ссылки", FetchedAt: time.Now()},
	}

	text, err := s.Summarize(context.Background(), articles)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if !strings.Contains(text, "Статья без ссылки") {
		t.Errorf("текст не содержит заголовок: %s", text)
	}
}
