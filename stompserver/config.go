// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
	"log/slog"
	"strings"
)

type StompConfig interface {
	HeartBeat() int64
	AppDestinationPrefix() []string
	IsAppRequestDestination(destination string) bool
	Logger() *slog.Logger
	SetLogger(logger *slog.Logger)
	GetMiddlewareRegistry() MiddlewareRegistry
	SetMiddlewareRegistry(registry MiddlewareRegistry)
}

type stompConfig struct {
	heartbeat          int64
	appDestPrefix      []string
	logger             *slog.Logger
	middlewareRegistry MiddlewareRegistry
}

func NewStompConfig(heartBeatMs int64, appDestinationPrefix []string) StompConfig {
	prefixes := make([]string, len(appDestinationPrefix))
	for i := 0; i < len(appDestinationPrefix); i++ {
		if appDestinationPrefix[i] != "" && !strings.HasSuffix(appDestinationPrefix[i], "/") {
			prefixes[i] = appDestinationPrefix[i] + "/"
		} else {
			prefixes[i] = appDestinationPrefix[i]
		}
	}

	return &stompConfig{
		heartbeat:     heartBeatMs,
		appDestPrefix: prefixes,
		logger:        slog.Default(),
		middlewareRegistry: MiddlewareRegistry{
			// Global middleware (applied to all commands) under key "*"
			"*": []MiddlewareFunc{
				// For example, a logging middleware could go here.
				// LoggingMiddleware,
				//
			},
		},
	}
}

// GetMiddlewareRegistry returns the registry.
func (c *stompConfig) GetMiddlewareRegistry() MiddlewareRegistry {
	return c.middlewareRegistry
}
func (c *stompConfig) SetMiddlewareRegistry(registry MiddlewareRegistry) {
	c.middlewareRegistry = registry
}

func (c *stompConfig) HeartBeat() int64 {
	return c.heartbeat
}

func (c *stompConfig) Logger() *slog.Logger {
	if c.logger == nil {
		return slog.Default()
	}
	return c.logger
}

func (c *stompConfig) SetLogger(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	c.logger = logger
}

func (c *stompConfig) AppDestinationPrefix() []string {
	return c.appDestPrefix
}

func (c *stompConfig) IsAppRequestDestination(destination string) bool {
	for _, prefix := range c.appDestPrefix {
		if prefix != "" && strings.HasPrefix(destination, prefix) {
			return true
		}
	}
	return false
}
