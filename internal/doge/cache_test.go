package doge

import (
	"testing"
	"time"
)

func TestTokenCacheCleanupExpired(t *testing.T) {
	cache := NewTokenCache(nil, 7200, time.Minute, 0)
	defer cache.Close()

	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	cache.SetClock(func() time.Time { return now })
	cache.entries.Store("valid", TmpTokenData{ExpiredAt: now.Add(10 * time.Minute).Unix()})
	cache.entries.Store("expired", TmpTokenData{ExpiredAt: now.Add(-time.Minute).Unix()})
	cache.entries.Store("invalid", "not-a-token")

	cache.CleanupExpired()

	if _, ok := cache.entries.Load("valid"); !ok {
		t.Fatal("valid token was removed")
	}
	if _, ok := cache.entries.Load("expired"); ok {
		t.Fatal("expired token was kept")
	}
	if _, ok := cache.entries.Load("invalid"); ok {
		t.Fatal("invalid token was kept")
	}
}
