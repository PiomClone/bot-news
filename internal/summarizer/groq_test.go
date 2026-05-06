package summarizer

import (
	"context"
	"errors"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"bot-news/internal/storage"
)

type mockChatClient struct {
	resp openai.ChatCompletionResponse
	err  error
}

func (m *mockChatClient) CreateChatCompletion(
	_ context.Context,
	_ openai.ChatCompletionRequest,
) (openai.ChatCompletionResponse, error) {
	return m.resp, m.err
}

func TestGroqSummarizer_Summarize(t *testing.T) {
	articles := []storage.Article{
		{Title: "Title 1", Link: "Link 1", Description: "Desc 1"},
	}

	t.Run("Success", func(t *testing.T) {
		mock := &mockChatClient{
			resp: openai.ChatCompletionResponse{
				Choices: []openai.ChatCompletionChoice{
					{Message: openai.ChatCompletionMessage{Content: "Summary text"}},
				},
			},
		}
		g := &GroqSummarizer{client: mock, model: "test-model"}
		res, err := g.Summarize(context.Background(), articles)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != "Summary text" {
			t.Errorf("got %q, want %q", res, "Summary text")
		}
	})

	t.Run("Error from API", func(t *testing.T) {
		mock := &mockChatClient{
			err: errors.New("api error"),
		}
		g := &GroqSummarizer{client: mock, model: "test-model"}
		_, err := g.Summarize(context.Background(), articles)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("Empty Response", func(t *testing.T) {
		mock := &mockChatClient{
			resp: openai.ChatCompletionResponse{
				Choices: []openai.ChatCompletionChoice{},
			},
		}
		g := &GroqSummarizer{client: mock, model: "test-model"}
		_, err := g.Summarize(context.Background(), articles)
		if err == nil {
			t.Fatal("expected error for empty choices, got nil")
		}
	})

	t.Run("Empty Articles", func(t *testing.T) {
		g := &GroqSummarizer{}
		res, err := g.Summarize(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != "" {
			t.Errorf("expected empty string for empty articles, got %q", res)
		}
	})
}
