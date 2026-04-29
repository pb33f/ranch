package fabric

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/pb33f/ranch/stompserver"
)

// EndpointConfig configures the Fabric STOMP endpoint.
type EndpointConfig struct {
	Logger *slog.Logger `json:"-"`

	// Prefix for public topics e.g. "/topic"
	TopicPrefix string
	// Prefix for user queues e.g. "/user/queue"
	UserQueuePrefix string
	// Prefix used for public application requests e.g. "/pub"
	AppRequestPrefix string
	// Prefix used for "private" application requests e.g. "/pub/queue"
	// Requests sent to destinations prefixed with the AppRequestQueuePrefix
	// should generate responses sent to single client queue.
	// E.g. if a client sends a request to the "/pub/queue/sample-channel" destination
	// the application should sent the response only to this client on the
	// "/user/queue/sample-channel" destination.
	// This behavior will mimic the Spring SimpleMessageBroker implementation.
	AppRequestQueuePrefix string
	Heartbeat             int64

	// Custom middleware for broker commands and destinations.
	MiddlewareRegistry stompserver.MiddlewareRegistry
}

func (ec *EndpointConfig) validate() error {
	if ec.TopicPrefix == "" || !strings.HasPrefix(ec.TopicPrefix, "/") {
		return fmt.Errorf("invalid TopicPrefix")
	}

	if ec.AppRequestQueuePrefix != "" && ec.UserQueuePrefix == "" {
		return fmt.Errorf("missing UserQueuePrefix")
	}

	return nil
}
