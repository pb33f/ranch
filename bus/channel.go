// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"context"

	"github.com/google/uuid"
	"github.com/pb33f/ranch/bridge"
	"github.com/pb33f/ranch/model"
	"sync"
	"sync/atomic"
)

// Channel represents a named bus stream and the handlers subscribed to it.
type Channel struct {
	Name                      string `json:"string"`
	eventHandlers             atomic.Pointer[[]*channelEventHandler]
	activity                  *channelActivity
	galactic                  bool
	galacticMappedDestination string
	private                   bool
	channelLock               sync.Mutex
	wg                        sync.WaitGroup
	brokerSubs                []*connectionSub
	brokerConns               []bridge.Connection
	brokerMappedEvent         chan bool
}

type channelActivity struct {
	lock        sync.Mutex
	cond        *sync.Cond
	next        uint64
	outstanding map[uint64]struct{}
}

func newChannelActivity() *channelActivity {
	activity := &channelActivity{outstanding: make(map[uint64]struct{})}
	activity.cond = sync.NewCond(&activity.lock)
	return activity
}

func (activity *channelActivity) watermark() uint64 {
	activity.lock.Lock()
	defer activity.lock.Unlock()
	return activity.next
}

func (activity *channelActivity) begin() uint64 {
	activity.lock.Lock()
	defer activity.lock.Unlock()

	activity.next++
	seq := activity.next
	activity.outstanding[seq] = struct{}{}
	return seq
}

func (activity *channelActivity) end(seq uint64) {
	activity.lock.Lock()
	delete(activity.outstanding, seq)
	activity.cond.Broadcast()
	activity.lock.Unlock()
}

func (activity *channelActivity) waitQuiescentAfter(ctx context.Context, start uint64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Done() != nil {
		stopWakeup := context.AfterFunc(ctx, func() {
			activity.lock.Lock()
			activity.cond.Broadcast()
			activity.lock.Unlock()
		})
		defer stopWakeup()
	}

	activity.lock.Lock()
	defer activity.lock.Unlock()
	target := activity.next
	for {
		for activity.hasOutstandingAfterLocked(start, target) {
			if err := ctx.Err(); err != nil {
				return err
			}
			activity.cond.Wait()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if activity.next == target {
			return nil
		}
		target = activity.next
	}
}

func (activity *channelActivity) hasOutstandingAfterLocked(start uint64, target uint64) bool {
	for seq := range activity.outstanding {
		if seq > start && seq <= target {
			return true
		}
	}
	return false
}

// NewChannel creates an empty bus channel with the provided name.
func NewChannel(channelName string) *Channel {
	eventHandlers := make([]*channelEventHandler, 0)
	c := &Channel{
		Name:              channelName,
		activity:          newChannelActivity(),
		channelLock:       sync.Mutex{},
		galactic:          false,
		private:           false,
		wg:                sync.WaitGroup{},
		brokerMappedEvent: make(chan bool, 10),
		brokerConns:       []bridge.Connection{},
		brokerSubs:        []*connectionSub{}}
	c.eventHandlers.Store(&eventHandlers)
	return c
}

// SetPrivate marks the channel as private or public.
func (channel *Channel) SetPrivate(private bool) {
	channel.private = private
}

// SetGalactic marks the channel as backed by a broker destination.
func (channel *Channel) SetGalactic(mappedDestination string) {
	channel.galactic = true
	channel.galacticMappedDestination = mappedDestination
}

// SetLocal marks the channel as local-only.
func (channel *Channel) SetLocal() {
	channel.galactic = false
	channel.galacticMappedDestination = ""
}

// IsGalactic reports whether the channel forwards through a broker destination.
func (channel *Channel) IsGalactic() bool {
	return channel.galactic
}

// IsPrivate reports whether the channel is marked private.
func (channel *Channel) IsPrivate() bool {
	return channel.private
}

// Send dispatches a message to the channel using a background context.
func (channel *Channel) Send(message *model.Message) {
	channel.SendContext(context.Background(), message)
}

// SendContext dispatches a message to subscribed handlers unless ctx is already canceled.
func (channel *Channel) SendContext(ctx context.Context, message *model.Message) {
	channel.dispatchContext(ctx, message)
}

func (channel *Channel) dispatchContext(ctx context.Context, message *model.Message) {
	if ctx == nil {
		ctx = context.Background()
	}
	eventHandlers := channel.handlersSnapshot()
	scheduledHandlers := make([]*channelEventHandler, 0, len(eventHandlers))
	pruneFiredRunOnce := false
	if len(eventHandlers) > 0 {
	dispatchHandlers:
		for _, eventHandler := range eventHandlers {
			select {
			case <-ctx.Done():
				break dispatchHandlers
			default:
			}
			if eventHandler.runOnce {
				pruneFiredRunOnce = true
				if !eventHandler.fired.CompareAndSwap(false, true) {
					continue
				}
			}
			scheduledHandlers = append(scheduledHandlers, eventHandler)
		}
	}
	if pruneFiredRunOnce {
		channel.pruneFiredRunOnceHandlers()
	}
	if len(scheduledHandlers) == 0 {
		return
	}
	// One dispatch goroutine owns the scheduled handler list. This keeps the hot
	// path at one goroutine per message while preserving async delivery.
	seq := channel.activity.begin()
	channel.wg.Add(1)
	go channel.sendMessageToHandlers(ctx, scheduledHandlers, message, seq)
}

// ContainsHandlers reports whether the channel currently has subscribed handlers.
func (channel *Channel) ContainsHandlers() bool {
	return len(channel.handlersSnapshot()) > 0
}

func (channel *Channel) sendMessageToHandlers(
	ctx context.Context, handlers []*channelEventHandler, message *model.Message, seq uint64) {
	defer channel.activity.end(seq)
	defer channel.wg.Done()
	for _, handler := range handlers {
		if handler.contextCallBackFunction != nil {
			handler.contextCallBackFunction(ctx, message)
		} else if handler.callBackFunction != nil {
			handler.callBackFunction(message)
		}
	}
}

func (channel *Channel) subscribeHandler(handler *channelEventHandler) {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	current := channel.handlersSnapshot()
	next := make([]*channelEventHandler, 0, len(current)+1)
	for _, h := range current {
		if h.runOnce && h.fired.Load() {
			continue
		}
		next = append(next, h)
	}
	next = append(next, handler)
	channel.eventHandlers.Store(&next)
}

func (channel *Channel) unsubscribeHandler(uuid *uuid.UUID) bool {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	current := channel.handlersSnapshot()
	next := make([]*channelEventHandler, 0, len(current))
	found := false
	for _, handler := range current {
		if handler.uuid != nil && uuid != nil && *handler.uuid == *uuid {
			found = true
			continue
		}
		if handler.runOnce && handler.fired.Load() {
			continue
		}
		next = append(next, handler)
	}
	channel.eventHandlers.Store(&next)
	return found
}

func (channel *Channel) pruneFiredRunOnceHandlers() {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	current := channel.handlersSnapshot()
	next := make([]*channelEventHandler, 0, len(current))
	removed := false
	for _, handler := range current {
		if handler.runOnce && handler.fired.Load() {
			removed = true
			continue
		}
		next = append(next, handler)
	}
	if removed {
		channel.eventHandlers.Store(&next)
	}
}

// Remove handler function from being subscribed to the Channel.
func (channel *Channel) removeEventHandler(index int) {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	current := channel.handlersSnapshot()
	numHandlers := len(current)
	if numHandlers <= 0 {
		return
	}
	if index >= numHandlers {
		return
	}

	next := make([]*channelEventHandler, 0, numHandlers-1)
	next = append(next, current[:index]...)
	next = append(next, current[index+1:]...)
	channel.eventHandlers.Store(&next)
}

func (channel *Channel) handlersSnapshot() []*channelEventHandler {
	handlers := channel.eventHandlers.Load()
	if handlers == nil {
		return nil
	}
	return *handlers
}

func (channel *Channel) listenToBrokerSubscription(sub *connectionSub) {
	for {
		select {
		case msg, ok := <-sub.s.GetMsgChannel():
			if !ok {
				return
			}
			channel.Send(msg)
		case <-sub.stop:
			return
		}
	}
}

func (channel *Channel) isBrokerSubscribed(sub bridge.Subscription) bool {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	for _, cs := range channel.brokerSubs {
		if *sub.GetId() == *cs.s.GetId() {
			return true
		}
	}
	return false
}

func (channel *Channel) isBrokerSubscribedToDestination(c bridge.Connection, dest string) bool {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	for _, cs := range channel.brokerSubs {
		if cs.s != nil && cs.s.GetDestination() == dest && cs.c != nil && *cs.c.GetId() == *c.GetId() {
			return true
		}
	}
	return false
}

func (channel *Channel) addBrokerSubscription(conn bridge.Connection, sub bridge.Subscription) {
	channel.addBrokerSubscriptionIfMissing(conn, sub)
}

func (channel *Channel) addBrokerSubscriptionIfMissing(conn bridge.Connection, sub bridge.Subscription) bool {
	cs := &connectionSub{c: conn, s: sub, stop: make(chan struct{})}

	channel.channelLock.Lock()
	for _, existing := range channel.brokerSubs {
		if existing.s != nil && existing.s.GetDestination() == sub.GetDestination() &&
			existing.c != nil && *existing.c.GetId() == *conn.GetId() {
			channel.channelLock.Unlock()
			return false
		}
	}
	hasConnection := false
	for _, brCon := range channel.brokerConns {
		if *brCon.GetId() == *conn.GetId() {
			hasConnection = true
			break
		}
	}
	if !hasConnection {
		channel.brokerConns = append(channel.brokerConns, conn)
	}
	channel.brokerSubs = append(channel.brokerSubs, cs)
	channel.channelLock.Unlock()

	go channel.listenToBrokerSubscription(cs)
	return true
}

func (channel *Channel) removeBrokerSubscription(sub bridge.Subscription) {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	for i, cs := range channel.brokerSubs {
		if *sub.GetId() == *cs.s.GetId() {
			cs.stopListening()
			channel.brokerSubs = removeSub(channel.brokerSubs, i)
			return
		}
	}
}

func (channel *Channel) closeBrokerSubscriptions() []*connectionSub {
	channel.channelLock.Lock()
	defer channel.channelLock.Unlock()

	subs := append([]*connectionSub(nil), channel.brokerSubs...)
	for _, sub := range subs {
		sub.stopListening()
	}
	channel.brokerSubs = []*connectionSub{}
	channel.brokerConns = []bridge.Connection{}
	return subs
}

func removeSub(s []*connectionSub, i int) []*connectionSub {
	s[len(s)-1], s[i] = s[i], s[len(s)-1]
	return s[:len(s)-1]
}

type connectionSub struct {
	c        bridge.Connection
	s        bridge.Subscription
	stop     chan struct{}
	stopOnce sync.Once
}

func (sub *connectionSub) stopListening() {
	if sub == nil || sub.stop == nil {
		return
	}
	sub.stopOnce.Do(func() {
		close(sub.stop)
	})
}
