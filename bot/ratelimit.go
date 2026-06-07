package bot

import (
	"sync"
	"time"
)

// RateLimiter — простой токен-бакет на пользователя (in-memory)
// Позволяет не более `limit` запросов за `window`
type RateLimiter struct {
	mu     sync.Mutex
	users  map[int64][]time.Time
	limit  int
	window time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		users:  make(map[int64][]time.Time),
		limit:  limit,
		window: window,
	}
	// Чистим старые записи каждые 5 минут
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

// Allow возвращает true если запрос разрешён
func (rl *RateLimiter) Allow(userID int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Оставляем только свежие метки
	fresh := rl.users[userID][:0]
	for _, t := range rl.users[userID] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}

	if len(fresh) >= rl.limit {
		rl.users[userID] = fresh
		return false
	}

	rl.users[userID] = append(fresh, now)
	return true
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-rl.window)
	for id, times := range rl.users {
		fresh := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			delete(rl.users, id)
		} else {
			rl.users[id] = fresh
		}
	}
}
