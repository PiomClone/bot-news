package retry

import (
	"context"
	"time"
)

// Do вызывает fn до attempts раз с экспоненциальным backoff (1s, 2s, 4s...).
// Возвращает nil при первом успехе или последнюю ошибку.
// Немедленно завершается если ctx отменён.
func Do(ctx context.Context, attempts int, fn func() error) error {
	var err error
	for i := range attempts {
		if err = fn(); err == nil {
			return nil
		}
		if i == attempts-1 {
			break
		}
		wait := time.Duration(1<<uint(i)) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}
