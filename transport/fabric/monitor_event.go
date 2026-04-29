package fabric

import "github.com/pb33f/ranch/bus"

const (
	// FabricEndpointSubscribeEvt is emitted when a STOMP client subscribes through Fabric.
	FabricEndpointSubscribeEvt bus.MonitorEventType = "fabric.endpoint.subscribe"
	// FabricEndpointUnsubscribeEvt is emitted when a STOMP client unsubscribes through Fabric.
	FabricEndpointUnsubscribeEvt bus.MonitorEventType = "fabric.endpoint.unsubscribe"
)
