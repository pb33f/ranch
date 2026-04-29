// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/pb33f/ranch/bridge"
	"github.com/pb33f/ranch/model"
	"sync"
	"sync/atomic"
)

// ChannelManager owns channel lifecycle and subscription access for an EventBus.
type ChannelManager interface {
	CreateChannel(channelName string) *Channel
	DestroyChannel(channelName string)
	CheckChannelExists(channelName string) bool
	GetChannel(channelName string) (*Channel, error)
	GetAllChannels() map[string]*Channel
	SubscribeChannelHandler(channelName string, fn MessageHandlerFunction, runOnce bool) (*uuid.UUID, error)
	SubscribeChannelHandlerContext(channelName string, fn MessageHandlerContextFunction, runOnce bool) (*uuid.UUID, error)
	UnsubscribeChannelHandler(channelName string, id *uuid.UUID) error
	WaitForChannel(channelName string) error
	MarkChannelAsGalactic(channelName string, brokerDestination string, connection bridge.Connection) (err error)
	MarkChannelAsLocal(channelName string) (err error)
}

// NewBusChannelManager creates the channel manager used by an EventBus.
func NewBusChannelManager(bus EventBus) ChannelManager {
	manager := new(busChannelManager)
	channels := make(map[string]*Channel)
	manager.channels.Store(&channels)
	manager.bus = bus.(*transportEventBus)
	return manager
}

type busChannelManager struct {
	// channels uses atomic copy-on-write because channel lookup is on the send path.
	// BenchmarkChannelManagerGetChannelParallel on Apple M4 Max: RWMutex ~130 ns/op,
	// sync.Map ~66 ns/op, atomic COW map ~65 ns/op; all variants were 0 allocs/op.
	channels atomic.Pointer[map[string]*Channel]
	bus      *transportEventBus
	lock     sync.Mutex
}

func (manager *busChannelManager) CreateChannel(channelName string) *Channel {
	manager.lock.Lock()
	defer manager.lock.Unlock()

	current := manager.channelSnapshot()
	channel, ok := current[channelName]
	if ok {
		return channel
	}

	next := make(map[string]*Channel, len(current)+1)
	for name, ch := range current {
		next[name] = ch
	}
	next[channelName] = NewChannel(channelName)
	manager.channels.Store(&next)
	go manager.bus.SendMonitorEvent(ChannelCreatedEvt, channelName, nil)
	return next[channelName]
}

func (manager *busChannelManager) DestroyChannel(channelName string) {
	manager.lock.Lock()
	defer manager.lock.Unlock()

	current := manager.channelSnapshot()
	next := make(map[string]*Channel, len(current))
	for name, ch := range current {
		if name != channelName {
			next[name] = ch
		}
	}
	manager.channels.Store(&next)
	go manager.bus.SendMonitorEvent(ChannelDestroyedEvt, channelName, nil)
}

func (manager *busChannelManager) GetChannel(channelName string) (*Channel, error) {
	if channel, ok := manager.channelSnapshot()[channelName]; ok {
		return channel, nil
	} else {
		return nil, errors.New("Channel does not exist: " + channelName)
	}
}

func (manager *busChannelManager) GetAllChannels() map[string]*Channel {
	current := manager.channelSnapshot()
	channels := make(map[string]*Channel, len(current))
	for name, ch := range current {
		channels[name] = ch
	}
	return channels
}

func (manager *busChannelManager) CheckChannelExists(channelName string) bool {
	return manager.channelSnapshot()[channelName] != nil
}

func (manager *busChannelManager) channelSnapshot() map[string]*Channel {
	channels := manager.channels.Load()
	if channels == nil {
		return nil
	}
	return *channels
}

func (manager *busChannelManager) SubscribeChannelHandler(channelName string, fn MessageHandlerFunction, runOnce bool) (*uuid.UUID, error) {
	channel, err := manager.GetChannel(channelName)
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	channel.subscribeHandler(&channelEventHandler{callBackFunction: fn, runOnce: runOnce, uuid: &id})
	manager.bus.SendMonitorEvent(ChannelSubscriberJoinedEvt, channelName, nil)
	return &id, nil
}

func (manager *busChannelManager) SubscribeChannelHandlerContext(
	channelName string, fn MessageHandlerContextFunction, runOnce bool) (*uuid.UUID, error) {
	channel, err := manager.GetChannel(channelName)
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	channel.subscribeHandler(&channelEventHandler{contextCallBackFunction: fn, runOnce: runOnce, uuid: &id})
	manager.bus.SendMonitorEvent(ChannelSubscriberJoinedEvt, channelName, nil)
	return &id, nil
}

func (manager *busChannelManager) UnsubscribeChannelHandler(channelName string, uuid *uuid.UUID) error {
	channel, err := manager.GetChannel(channelName)
	if err != nil {
		return err
	}
	found := channel.unsubscribeHandler(uuid)
	if !found {
		return fmt.Errorf("no handler in Channel '%s' for uuid [%s]", channelName, uuid)
	}
	manager.bus.SendMonitorEvent(ChannelSubscriberLeftEvt, channelName, nil)
	return nil
}

func (manager *busChannelManager) WaitForChannel(channelName string) error {
	channel, _ := manager.GetChannel(channelName)
	if channel == nil {
		return fmt.Errorf("no such Channel as '%s'", channelName)
	}
	channel.wg.Wait()
	return nil
}

func (manager *busChannelManager) MarkChannelAsGalactic(channelName string, dest string, conn bridge.Connection) (err error) {
	channel, err := manager.GetChannel(channelName)
	if err != nil {
		return
	}

	channel.SetGalactic(dest)

	pl := &galacticEvent{conn: conn, dest: dest}

	manager.handleGalacticChannelEvent(channelName, pl)
	return nil
}

func (manager *busChannelManager) MarkChannelAsLocal(channelName string) (err error) {
	channel, err := manager.GetChannel(channelName)
	if err != nil {
		return
	}
	channel.SetLocal()

	channel.removeBrokerConnections()

	manager.handleLocalChannelEvent(channelName)

	return nil
}

func (manager *busChannelManager) handleGalacticChannelEvent(channelName string, ge *galacticEvent) {
	ch, _ := manager.GetChannel(channelName)

	if ge.conn == nil {
		return
	}

	if !ch.isBrokerSubscribedToDestination(ge.conn, ge.dest) {
		if sub, e := ge.conn.Subscribe(ge.dest); e == nil {

			ch.addBrokerConnection(ge.conn)

			m := model.GenerateResponse(&model.MessageConfig{Payload: ge.dest}) // set the mapped destination as the payload
			ch.addBrokerSubscription(ge.conn, sub)
			manager.bus.SendMonitorEvent(BrokerSubscribedEvt, channelName, m)
			select {
			case ch.brokerMappedEvent <- true: // let channel watcher know, the channel is mapped
			default: // if no-one is listening, drop.
			}
		}
	}
}

func (manager *busChannelManager) handleLocalChannelEvent(channelName string) {
	ch, _ := manager.GetChannel(channelName)
	// loop through all the connections we have mapped, and subscribe!
	for _, s := range ch.brokerSubs {
		if e := s.s.Unsubscribe(); e == nil {
			ch.removeBrokerSubscription(s.s)
			m := model.GenerateResponse(&model.MessageConfig{Payload: s.s.GetDestination()}) // set the unmapped destination as the payload
			manager.bus.SendMonitorEvent(BrokerUnsubscribedEvt, channelName, m)
			select {
			case ch.brokerMappedEvent <- false: // let channel watcher know, the channel is un-mapped
			default: // if no-one is listening, drop.
			}
		}
	}
	ch.removeBrokerConnections()
}

type galacticEvent struct {
	conn bridge.Connection
	dest string
}
