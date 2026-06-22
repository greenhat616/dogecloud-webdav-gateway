package doge

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type TokenCache struct {
	client *Client
	ttl    int
	skew   time.Duration

	clockMu sync.RWMutex
	now     func() time.Time

	entries sync.Map
	stop    chan struct{}
	done    chan struct{}
}

func NewTokenCache(client *Client, ttl int, skew, cleanupInterval time.Duration) *TokenCache {
	cache := &TokenCache{
		client: client,
		ttl:    ttl,
		skew:   skew,
		now:    time.Now,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if cleanupInterval > 0 {
		go cache.cleanupLoop(cleanupInterval)
	} else {
		close(cache.done)
	}
	return cache
}

func (c *TokenCache) SetClock(now func() time.Time) {
	c.clockMu.Lock()
	defer c.clockMu.Unlock()
	c.now = now
}

func (c *TokenCache) Get(ctx context.Context, key APIKey, scopes []string) (TmpTokenData, error) {
	normalized := normalizeScopes(scopes)
	cacheKey := key.AccessKey + "\x00" + strings.Join(normalized, "\x00")

	if value, ok := c.entries.Load(cacheKey); ok {
		token := value.(TmpTokenData)
		if c.valid(token) {
			return token, nil
		}
		c.entries.Delete(cacheKey)
	}

	token, err := c.client.TemporaryToken(ctx, key, TmpTokenRequest{
		Channel: "OSS_FULL",
		Scopes:  normalized,
		TTL:     c.ttl,
	})
	if err != nil {
		return TmpTokenData{}, err
	}

	c.entries.Store(cacheKey, token)
	return token, nil
}

func (c *TokenCache) CleanupExpired() {
	c.entries.Range(func(key, value any) bool {
		token, ok := value.(TmpTokenData)
		if !ok || !c.valid(token) {
			c.entries.Delete(key)
		}
		return true
	})
}

func (c *TokenCache) Close() {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	<-c.done
}

func (c *TokenCache) valid(token TmpTokenData) bool {
	if token.ExpiredAt == 0 {
		return false
	}
	deadline := time.Unix(token.ExpiredAt, 0).Add(-c.skew)
	return c.clock().Before(deadline)
}

func (c *TokenCache) clock() time.Time {
	c.clockMu.RLock()
	defer c.clockMu.RUnlock()
	return c.now()
}

func (c *TokenCache) cleanupLoop(interval time.Duration) {
	defer close(c.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.CleanupExpired()
		case <-c.stop:
			return
		}
	}
}

func normalizeScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	seen := map[string]struct{}{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}
