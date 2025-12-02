// Copyright 2025 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: MIT

package ratelimit

import (
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

type Tier string

const (
	TierUnauth Tier = "unauth"
	TierAuth   Tier = "auth"
	TierPaid   Tier = "paid"
)

type TierConfig struct {
	Rate  rate.Limit // tokens per second
	Burst int        // max burst size
}

// KeyExtractor returns the rate limit key, key type (e.g. "session", "apikey", "ip"), and error
type KeyExtractor func(r *http.Request) (key string, keyType string, err error)

// TierResolver maps a key to its tier based on key type
type TierResolver func(key string, keyType string) Tier

type Config struct {
	Tiers            map[Tier]TierConfig
	KeyExtractor     KeyExtractor
	TierResolver     TierResolver
	EventHandler     EventHandler
	CleanupInterval  time.Duration
	WarningThreshold float64 // fires OnApproaching when remaining tokens fall below this fraction (0.0-1.0)
}

// DefaultConfig returns config with sensible defaults.
// consumers should override KeyExtractor and TierResolver.
func DefaultConfig() Config {
	return Config{
		Tiers: map[Tier]TierConfig{
			TierUnauth: {Rate: rate.Limit(10.0 / 60), Burst: 10},  // 10/min
			TierAuth:   {Rate: rate.Limit(60.0 / 60), Burst: 20},  // 60/min
			TierPaid:   {Rate: rate.Limit(300.0 / 60), Burst: 50}, // 300/min
		},
		KeyExtractor: func(r *http.Request) (string, string, error) {
			return ExtractIP(r), "ip", nil
		},
		TierResolver: func(key, keyType string) Tier {
			return TierUnauth
		},
		CleanupInterval:  5 * time.Minute,
		WarningThreshold: 0.2,
	}
}

