// Copyright 2025 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: MIT

package ratelimit

import (
	"context"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// visitor tracks rate limit state for a single key
type visitor struct {
	limiter  *rate.Limiter
	tier     Tier
	lastSeen time.Time
}

// Limiter implements token bucket rate limiting with tier support
type Limiter struct {
	config         Config
	visitors       map[string]*visitor
	mu             sync.RWMutex
	cleanupStarted sync.Once
}

func New(config Config) *Limiter {
	return &Limiter{
		config:   config,
		visitors: make(map[string]*visitor),
	}
}

func (l *Limiter) Allow(r *http.Request) (bool, Event) {
	key, keyType, err := l.config.KeyExtractor(r)
	if err != nil {
		// on extraction error, fall back to ip
		key = ExtractIP(r)
		keyType = "ip"
	}

	tier := l.config.TierResolver(key, keyType)
	limiter := l.getOrCreateLimiter(key, tier)

	tierCfg := l.config.Tiers[tier]
	limit := tierCfg.Burst

	allowed := limiter.Allow()

	// get token count after consumption
	tokens := limiter.Tokens()
	remaining := int(tokens)
	if remaining < 0 {
		remaining = 0
	}

	// calculate reset time based on refill rate
	resetAt := time.Now().Add(time.Duration(float64(time.Second) / float64(tierCfg.Rate)))

	event := NewEvent(r, key, keyType, tier, allowed, remaining, limit, resetAt)
	return allowed, event
}

func (l *Limiter) getOrCreateLimiter(key string, tier Tier) *rate.Limiter {
	l.mu.RLock()
	v, exists := l.visitors[key]
	if exists && v.tier == tier {
		// fast path: visitor exists with same tier, no updates needed
		l.mu.RUnlock()
		return v.limiter
	}
	l.mu.RUnlock()

	// slow path: need to create or update visitor
	l.mu.Lock()
	defer l.mu.Unlock()

	// double-check after acquiring write lock
	v, exists = l.visitors[key]
	if exists {
		v.lastSeen = time.Now()
		// check if tier changed (user upgraded/downgraded)
		if v.tier != tier {
			tierCfg := l.config.Tiers[tier]
			v.limiter = rate.NewLimiter(tierCfg.Rate, tierCfg.Burst)
			v.tier = tier
		}
		return v.limiter
	}

	// create new visitor
	tierCfg := l.config.Tiers[tier]
	limiter := rate.NewLimiter(tierCfg.Rate, tierCfg.Burst)

	l.visitors[key] = &visitor{
		limiter:  limiter,
		tier:     tier,
		lastSeen: time.Now(),
	}

	return limiter
}

// starts background goroutine to remove stale visitors. safe to call multiple times.
func (l *Limiter) StartCleanup(ctx context.Context) {
	l.cleanupStarted.Do(func() {
		ticker := time.NewTicker(l.config.CleanupInterval)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					l.cleanup()
				}
			}
		}()
	})
}

func (l *Limiter) cleanup() {
	threshold := time.Now().Add(-l.config.CleanupInterval * 2)

	l.mu.Lock()
	defer l.mu.Unlock()

	for key, v := range l.visitors {
		if v.lastSeen.Before(threshold) {
			delete(l.visitors, key)
		}
	}
}

func (l *Limiter) VisitorCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.visitors)
}
