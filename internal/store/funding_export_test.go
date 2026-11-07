package store

import (
	"context"
	"time"
)

func (s *Store) RefillMonthlyAtForTest(ctx context.Context, now time.Time, batch int) (int, error) {
	return s.refillMonthlyAt(ctx, now, batch)
}
