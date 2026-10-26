package core

import (
	"context"
	"sync"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

// Bus fans events out to subscribers. Publishing never blocks: a subscriber
// that falls behind loses events instead of stalling the drivers.
type Bus struct {
	mu     sync.Mutex
	seq    uint64
	subs   map[*subscriber]struct{}
	recent []sdk.Event // ring of notable events
	next   int
	full   bool
	now    func() time.Time
}

type subscriber struct {
	ch      chan sdk.Event
	dropped uint64
}

const recentEvents = 100

// NewBus returns an empty bus.
func NewBus() *Bus {
	return &Bus{
		subs:   map[*subscriber]struct{}{},
		recent: make([]sdk.Event, recentEvents),
		now:    time.Now,
	}
}

// Publish stamps e with a sequence number (and a time, if unset) and
// delivers it to every subscriber.
func (b *Bus) Publish(e sdk.Event) sdk.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	if e.Time.IsZero() {
		e.Time = b.now()
	}
	if e.Type.Notable() {
		b.recent[b.next] = e
		b.next = (b.next + 1) % len(b.recent)
		if b.next == 0 {
			b.full = true
		}
	}
	for s := range b.subs {
		select {
		case s.ch <- e:
		default:
			s.dropped++
		}
	}
	return e
}

// Subscribe returns a channel of events that is closed when ctx is done.
func (b *Bus) Subscribe(ctx context.Context, buffer int) <-chan sdk.Event {
	s := &subscriber{ch: make(chan sdk.Event, max(buffer, 1))}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, s)
		close(s.ch)
		b.mu.Unlock()
	}()
	return s.ch
}

// Recent returns up to n of the latest notable events, oldest first.
func (b *Bus) Recent(n int) []sdk.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var all []sdk.Event
	if b.full {
		all = append(all, b.recent[b.next:]...)
	}
	all = append(all, b.recent[:b.next]...)
	if n >= 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
