// Copyright 2025 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: MIT

package ratelimit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// mockEventHandler records events for testing
type mockEventHandler struct {
	mu           sync.Mutex
	limitedCalls []Event
	approaching  []Event
	blocked      []Event
}

func (m *mockEventHandler) OnLimited(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limitedCalls = append(m.limitedCalls, event)
}

func (m *mockEventHandler) OnApproaching(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approaching = append(m.approaching, event)
}

func (m *mockEventHandler) OnBlocked(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocked = append(m.blocked, event)
}

func (m *mockEventHandler) LimitedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.limitedCalls)
}

func (m *mockEventHandler) ApproachingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.approaching)
}

// TestDefaultConfig verifies default configuration values
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.NotNil(t, cfg.Tiers)
	assert.Contains(t, cfg.Tiers, TierUnauth)
	assert.Contains(t, cfg.Tiers, TierAuth)
	assert.Contains(t, cfg.Tiers, TierPaid)

	assert.NotNil(t, cfg.KeyExtractor)
	assert.NotNil(t, cfg.TierResolver)

	assert.Equal(t, 5*time.Minute, cfg.CleanupInterval)
	assert.Equal(t, 0.2, cfg.WarningThreshold)
}

// TestExtractIP tests IP extraction from various headers
func TestExtractIP(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		remote   string
		expected string
	}{
		{
			name:     "x-forwarded-for single",
			headers:  map[string]string{"X-Forwarded-For": "192.168.1.1"},
			remote:   "10.0.0.1:1234",
			expected: "192.168.1.1",
		},
		{
			name:     "x-forwarded-for chain",
			headers:  map[string]string{"X-Forwarded-For": "192.168.1.1, 10.0.0.2, 172.16.0.1"},
			remote:   "10.0.0.1:1234",
			expected: "192.168.1.1",
		},
		{
			name:     "x-real-ip",
			headers:  map[string]string{"X-Real-IP": "192.168.1.100"},
			remote:   "10.0.0.1:1234",
			expected: "192.168.1.100",
		},
		{
			name:     "fallback to remote addr",
			headers:  map[string]string{},
			remote:   "10.0.0.1:1234",
			expected: "10.0.0.1:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tt.remote
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			ip := ExtractIP(req)
			assert.Equal(t, tt.expected, ip)
		})
	}
}

// TestLimiterAllow tests basic allow/deny logic
func TestLimiterAllow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tiers[TierUnauth] = TierConfig{
		Rate:  rate.Limit(2), // 2 per second
		Burst: 2,
	}

	limiter := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"

	// first two requests should be allowed (burst)
	allowed1, event1 := limiter.Allow(req)
	assert.True(t, allowed1)
	assert.Equal(t, "192.168.1.1:1234", event1.Key)
	assert.Equal(t, "ip", event1.KeyType)
	assert.Equal(t, TierUnauth, event1.Tier)

	allowed2, _ := limiter.Allow(req)
	assert.True(t, allowed2)

	// third should be denied (burst exhausted)
	allowed3, event3 := limiter.Allow(req)
	assert.False(t, allowed3)
	assert.Equal(t, 0, event3.Remaining)
}

// TestLimiterTiers verifies different tiers have different limits
func TestLimiterTiers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tiers[TierUnauth] = TierConfig{Rate: rate.Limit(1), Burst: 1}
	cfg.Tiers[TierAuth] = TierConfig{Rate: rate.Limit(1), Burst: 5}

	cfg.TierResolver = func(key, keyType string) Tier {
		if key == "auth-user" {
			return TierAuth
		}
		return TierUnauth
	}

	cfg.KeyExtractor = func(r *http.Request) (string, string, error) {
		if user := r.Header.Get("X-User"); user != "" {
			return user, "session", nil
		}
		return ExtractIP(r), "ip", nil
	}

	limiter := New(cfg)

	// unauth user hits limit after 1 request
	unauthReq := httptest.NewRequest(http.MethodGet, "/test", nil)
	unauthReq.RemoteAddr = "10.0.0.1:1234"

	allowed, _ := limiter.Allow(unauthReq)
	assert.True(t, allowed)
	allowed, _ = limiter.Allow(unauthReq)
	assert.False(t, allowed)

	// auth user gets 5 burst
	authReq := httptest.NewRequest(http.MethodGet, "/test", nil)
	authReq.Header.Set("X-User", "auth-user")

	for i := 0; i < 5; i++ {
		allowed, _ = limiter.Allow(authReq)
		assert.True(t, allowed, "auth user request %d should be allowed", i+1)
	}

	// 6th should be denied
	allowed, _ = limiter.Allow(authReq)
	assert.False(t, allowed)
}

// TestLimiterVisitorCount tests visitor tracking
func TestLimiterVisitorCount(t *testing.T) {
	limiter := New(DefaultConfig())

	assert.Equal(t, 0, limiter.VisitorCount())

	// add a visitor
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "192.168.1.1:1234"
	limiter.Allow(req1)
	assert.Equal(t, 1, limiter.VisitorCount())

	// same visitor doesn't increase count
	limiter.Allow(req1)
	assert.Equal(t, 1, limiter.VisitorCount())

	// different visitor increases count
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.2:1234"
	limiter.Allow(req2)
	assert.Equal(t, 2, limiter.VisitorCount())
}

// TestLimiterCleanup tests that stale visitors are removed
func TestLimiterCleanup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CleanupInterval = 10 * time.Millisecond

	limiter := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	limiter.Allow(req)

	assert.Equal(t, 1, limiter.VisitorCount())

	// manually trigger cleanup after threshold
	time.Sleep(30 * time.Millisecond)
	limiter.cleanup()

	assert.Equal(t, 0, limiter.VisitorCount())
}

// TestLimiterStartCleanup tests the background cleanup goroutine
func TestLimiterStartCleanup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CleanupInterval = 50 * time.Millisecond

	limiter := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	limiter.StartCleanup(ctx)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	limiter.Allow(req)

	assert.Equal(t, 1, limiter.VisitorCount())

	// wait for cleanup to run
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, 0, limiter.VisitorCount())
}

// TestMiddleware tests the HTTP middleware
func TestMiddleware(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tiers[TierUnauth] = TierConfig{Rate: rate.Limit(1), Burst: 2}

	limiter := New(cfg)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	middleware := limiter.Middleware(handler)

	// first request should pass
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "192.168.1.1:1234"
	rec1 := httptest.NewRecorder()
	middleware.ServeHTTP(rec1, req1)

	assert.Equal(t, http.StatusOK, rec1.Code)
	assert.NotEmpty(t, rec1.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, rec1.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, rec1.Header().Get("X-RateLimit-Reset"))

	// exhaust burst
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.1:1234"
	rec2 := httptest.NewRecorder()
	middleware.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code)

	// third request should be rate limited
	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req3.RemoteAddr = "192.168.1.1:1234"
	rec3 := httptest.NewRecorder()
	middleware.ServeHTTP(rec3, req3)

	assert.Equal(t, http.StatusTooManyRequests, rec3.Code)
	assert.Equal(t, "application/json", rec3.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec3.Header().Get("Retry-After"))

	// verify JSON response
	var errResp ErrorResponse
	err := json.Unmarshal(rec3.Body.Bytes(), &errResp)
	require.NoError(t, err)
	assert.Equal(t, "rate_limit_exceeded", errResp.Error)
	assert.Equal(t, 2, errResp.Limit)
	assert.Equal(t, 0, errResp.Remaining)
}

// TestMiddlewareEventHandler tests that events are fired correctly
func TestMiddlewareEventHandler(t *testing.T) {
	handler := &mockEventHandler{}

	cfg := DefaultConfig()
	cfg.Tiers[TierUnauth] = TierConfig{Rate: rate.Limit(1), Burst: 2}
	cfg.EventHandler = handler
	cfg.WarningThreshold = 0.5 // fire approaching when 50% remaining

	limiter := New(cfg)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := limiter.Middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"

	// first request - should fire OnApproaching (1 remaining out of 2 = 50%)
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// second request - should also fire OnApproaching (0 remaining)
	rec = httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// third request - should fire OnLimited
	rec = httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	assert.Equal(t, 1, handler.LimitedCount())
	assert.GreaterOrEqual(t, handler.ApproachingCount(), 1)
}

// TestEventCreation tests event creation
func TestEventCreation(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	req.Header.Set("User-Agent", "test-agent")

	resetAt := time.Now().Add(time.Minute)
	event := NewEvent(req, "test-key", "session", TierAuth, true, 5, 10, resetAt)

	assert.Equal(t, "test-key", event.Key)
	assert.Equal(t, "session", event.KeyType)
	assert.Equal(t, TierAuth, event.Tier)
	assert.Equal(t, "/api/v1/test", event.Path)
	assert.Equal(t, http.MethodPost, event.Method)
	assert.True(t, event.Allowed)
	assert.Equal(t, 5, event.Remaining)
	assert.Equal(t, 10, event.Limit)
	assert.Equal(t, "192.168.1.1:1234", event.RemoteAddr)
	assert.Equal(t, "test-agent", event.UserAgent)
}

// TestNoopEventHandler tests the no-op handler doesn't panic
func TestNoopEventHandler(t *testing.T) {
	handler := &NoopEventHandler{}
	event := Event{Key: "test"}

	// should not panic
	handler.OnLimited(event)
	handler.OnApproaching(event)
	handler.OnBlocked(event)
}

// TestConcurrentAccess tests thread safety
func TestConcurrentAccess(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tiers[TierUnauth] = TierConfig{Rate: rate.Limit(1000), Burst: 1000}

	limiter := New(cfg)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = "192.168.1.1:1234"

			for j := 0; j < 10; j++ {
				limiter.Allow(req)
			}
		}(i)
	}

	wg.Wait()
	// if we get here without deadlock or panic, test passes
	assert.Equal(t, 1, limiter.VisitorCount())
}

// TestTierUpgrade tests that tier changes are handled correctly
func TestTierUpgrade(t *testing.T) {
	tierForKey := TierUnauth

	cfg := DefaultConfig()
	cfg.Tiers[TierUnauth] = TierConfig{Rate: rate.Limit(1), Burst: 1}
	cfg.Tiers[TierPaid] = TierConfig{Rate: rate.Limit(1), Burst: 10}

	cfg.TierResolver = func(key, keyType string) Tier {
		return tierForKey
	}

	cfg.KeyExtractor = func(r *http.Request) (string, string, error) {
		return "user-123", "session", nil
	}

	limiter := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// as unauth, should hit limit after 1 request
	allowed, _ := limiter.Allow(req)
	assert.True(t, allowed)
	allowed, _ = limiter.Allow(req)
	assert.False(t, allowed)

	// upgrade to paid tier
	tierForKey = TierPaid

	// should now have higher limit
	for i := 0; i < 10; i++ {
		allowed, _ = limiter.Allow(req)
		assert.True(t, allowed, "paid tier request %d should be allowed", i+1)
	}
}
