package events

import (
	"errors"
	"sync"
)

// Limits on concurrent event streams (spec I-36).
const (
	MaxStreamsPerCredential = 5
	MaxStreamsPerPod        = 200
	// subBuffer is how many events a subscriber may lag behind; one that falls
	// further is dropped and resumes with Last-Event-ID.
	subBuffer = 256
)

// ErrTooManyStreams is returned when a credential or the pod is at its limit.
var ErrTooManyStreams = errors.New("events: too many open streams")

// Sub is one subscriber of the live stream.
type Sub struct {
	// C delivers matching events in cursor order. It is closed when the
	// subscription ends; Lost then says whether the subscriber fell behind.
	C <-chan Entry

	c    chan Entry
	h    *hub
	mu   sync.Mutex
	f    Filter
	lost bool
	done bool
}

// Lost reports whether the subscriber was dropped for lagging.
func (s *Sub) Lost() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lost
}

// SetAllowed replaces the extensions the subscriber may see (nil: all).
func (s *Sub) SetAllowed(allowed []string) {
	s.mu.Lock()
	s.f.Allowed = allowed
	s.mu.Unlock()
}

// Close ends the subscription.
func (s *Sub) Close() { s.end(false) }

func (s *Sub) end(lost bool) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done, s.lost = true, lost
	close(s.c)
	s.mu.Unlock()
	s.h.forget(s)
}

// deliver hands e to the subscriber if it matches; false means the buffer is
// full.
func (s *Sub) deliver(e Entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || !s.f.Matches(e.Event) {
		return true
	}
	select {
	case s.c <- e:
		return true
	default:
		return false
	}
}

// hub fans the live stream out in memory to the subscribers of one pod.
type hub struct {
	mu    sync.Mutex
	subs  map[*Sub]struct{}
	creds map[string]int
	total int
	gauge func(int)
}

func newHub(gauge func(int)) *hub {
	if gauge == nil {
		gauge = func(int) {}
	}
	return &hub{subs: map[*Sub]struct{}{}, creds: map[string]int{}, gauge: gauge}
}

// acquire reserves a stream slot for credential key.
func (h *hub) acquire(key string) (release func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= MaxStreamsPerPod || h.creds[key] >= MaxStreamsPerCredential {
		return nil, ErrTooManyStreams
	}
	h.creds[key]++
	h.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.creds[key]--
			if h.creds[key] <= 0 {
				delete(h.creds, key)
			}
			h.total--
			h.mu.Unlock()
		})
	}, nil
}

func (h *hub) subscribe(f Filter) *Sub {
	c := make(chan Entry, subBuffer)
	s := &Sub{C: c, c: c, h: h, f: f}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	n := len(h.subs)
	h.mu.Unlock()
	h.gauge(n)
	return s
}

func (h *hub) forget(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	n := len(h.subs)
	h.mu.Unlock()
	h.gauge(n)
}

// publish delivers e to every matching subscriber; one whose buffer is full
// is dropped and resumes from its last cursor.
func (h *hub) publish(e Entry) {
	h.mu.Lock()
	subs := make([]*Sub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		if !s.deliver(e) {
			s.end(true)
		}
	}
}

// closeAll ends every subscription (shutdown).
func (h *hub) closeAll() {
	h.mu.Lock()
	subs := make([]*Sub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.end(false)
	}
}
