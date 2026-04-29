package fabric

import "github.com/pb33f/ranch/monitor"

const (
	// FabricEndpointSubscribeEvt is emitted when a STOMP client subscribes through Fabric.
	FabricEndpointSubscribeEvt = monitor.FabricEndpointSubscribeEvt
	// FabricEndpointUnsubscribeEvt is emitted when a STOMP client unsubscribes through Fabric.
	FabricEndpointUnsubscribeEvt = monitor.FabricEndpointUnsubscribeEvt
)
