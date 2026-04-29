package bench

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
)

var (
	sinkChan *bus.Channel
)

var errBenchChannelMissing = errors.New("channel does not exist")

func BenchmarkChannelSendOneHandler(b *testing.B) {
	eb := bus.NewEventBus()
	channelName := "bench-channel"
	ch := eb.GetChannelManager().CreateChannel(channelName)

	handler, err := eb.ListenStream(channelName)
	if err != nil {
		b.Fatal(err)
	}
	handler.Handle(func(*model.Message) {}, func(err error) {
		b.Fatal(err)
	})
	defer handler.Close()

	msg := model.GenerateResponse(&model.MessageConfig{
		Channel: channelName,
		Payload: map[string]any{"name": "bench", "value": 42},
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch.Send(msg)
		if i%1024 == 0 {
			_ = eb.GetChannelManager().WaitForChannel(channelName)
		}
	}
	_ = eb.GetChannelManager().WaitForChannel(channelName)
}

type benchChannelLookup interface {
	GetChannel(channelName string) (*bus.Channel, error)
}

type benchRWMutexChannelLookup struct {
	channels map[string]*bus.Channel
	lock     sync.RWMutex
}

func (m *benchRWMutexChannelLookup) GetChannel(channelName string) (*bus.Channel, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	ch, ok := m.channels[channelName]
	if !ok {
		return nil, errBenchChannelMissing
	}
	return ch, nil
}

type benchSyncMapChannelLookup struct {
	channels sync.Map
}

func (m *benchSyncMapChannelLookup) GetChannel(channelName string) (*bus.Channel, error) {
	ch, ok := m.channels.Load(channelName)
	if !ok {
		return nil, errBenchChannelMissing
	}
	return ch.(*bus.Channel), nil
}

type benchAtomicMapChannelLookup struct {
	channels atomic.Pointer[map[string]*bus.Channel]
}

func (m *benchAtomicMapChannelLookup) GetChannel(channelName string) (*bus.Channel, error) {
	channels := m.channels.Load()
	if channels == nil {
		return nil, errBenchChannelMissing
	}
	ch, ok := (*channels)[channelName]
	if !ok {
		return nil, errBenchChannelMissing
	}
	return ch, nil
}

func BenchmarkChannelManagerGetChannelParallel(b *testing.B) {
	const channelCount = 256

	names := make([]string, channelCount)
	channels := make(map[string]*bus.Channel, channelCount)
	eb := bus.NewEventBus()
	production := eb.GetChannelManager()
	for i := 0; i < channelCount; i++ {
		name := fmt.Sprintf("bench-channel-%03d", i)
		names[i] = name
		channels[name] = bus.NewChannel(name)
		production.CreateChannel(name)
	}

	rwMutexLookup := &benchRWMutexChannelLookup{channels: channels}

	syncMapLookup := &benchSyncMapChannelLookup{}
	for name, ch := range channels {
		syncMapLookup.channels.Store(name, ch)
	}

	atomicMapLookup := &benchAtomicMapChannelLookup{}
	atomicChannels := make(map[string]*bus.Channel, len(channels))
	for name, ch := range channels {
		atomicChannels[name] = ch
	}
	atomicMapLookup.channels.Store(&atomicChannels)

	benchLookup := func(b *testing.B, lookup benchChannelLookup) {
		var seq atomic.Uint64
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := seq.Add(1)
				ch, err := lookup.GetChannel(names[i%uint64(len(names))])
				if err != nil {
					b.Fatal(err)
				}
				sinkChan = ch
			}
		})
	}

	b.Run("production-current", func(b *testing.B) {
		benchLookup(b, production)
	})
	b.Run("local-rwmutex", func(b *testing.B) {
		benchLookup(b, rwMutexLookup)
	})
	b.Run("sync-map", func(b *testing.B) {
		benchLookup(b, syncMapLookup)
	})
	b.Run("atomic-cow-map", func(b *testing.B) {
		benchLookup(b, atomicMapLookup)
	})
}
