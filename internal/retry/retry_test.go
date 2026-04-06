package retry_test

import (
	"context"
	"errors"
	"testing"

	"bot-news/internal/retry"
)

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	err := retry.Do(context.Background(), 3, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("ожидали nil, получили %v", err)
	}
	if calls != 1 {
		t.Fatalf("ожидали 1 вызов, получили %d", calls)
	}
}

func TestDo_SuccessAfterRetry(t *testing.T) {
	calls := 0
	err := retry.Do(context.Background(), 3, func() error {
		calls++
		if calls < 3 {
			return errors.New("временная ошибка")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ожидали nil после retry, получили %v", err)
	}
	if calls != 3 {
		t.Fatalf("ожидали 3 вызова, получили %d", calls)
	}
}

func TestDo_AllAttemptsFail(t *testing.T) {
	sentinel := errors.New("постоянная ошибка")
	calls := 0
	err := retry.Do(context.Background(), 3, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ожидали sentinel ошибку, получили %v", err)
	}
	if calls != 3 {
		t.Fatalf("ожидали 3 вызова, получили %d", calls)
	}
}

func TestDo_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // сразу отменяем

	calls := 0
	err := retry.Do(ctx, 3, func() error {
		calls++
		return errors.New("ошибка")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидали context.Canceled, получили %v", err)
	}
	// первый вызов всегда происходит до проверки контекста
	if calls != 1 {
		t.Fatalf("ожидали 1 вызов, получили %d", calls)
	}
}
