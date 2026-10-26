package media

import (
	"sync"
	"sync/atomic"
	"time"
)

// Room is the clocked mixer of one conference room: every 20 ms it takes a
// frame from each admitted party, mixes them (MixFrames) and sends every
// party its mix. Parties that wait for a leader are not mixed: they hear
// only the hold source (silence or a tone now; music on hold plugs in
// through SetHold) and their own private sounds. A room runs its goroutine
// only while it has parties.
type Room struct {
	mu      sync.Mutex
	parties map[string]*roomParty
	order   []string // insertion order, for deterministic iteration
	shared  []int16  // room-wide sound queue (tones, announcements)
	tap     func(frame []int16)
	started bool
	stop    chan struct{}
	done    chan struct{}

	ticks   atomic.Uint64
	overrun atomic.Uint64 // ticks that took longer than a frame
	mixed   atomic.Uint64 // frames sent
}

type roomParty struct {
	p        *Party
	muted    bool
	isolated bool    // waiting for a leader: not mixed in, hears the hold source
	private  []int16 // sound for this party only (prompts, control tones)
	hold     []int16 // looping hold source for an isolated party
	holdPos  int
}

// NewRoom returns an empty room. Its clock starts with the first party.
func NewRoom() *Room {
	return &Room{parties: map[string]*roomParty{}}
}

// Add puts a party in the room under id. isolated parties wait: they are
// not mixed until Admit.
func (r *Room) Add(id string, p *Party, muted, isolated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.parties[id]; ok {
		return
	}
	r.parties[id] = &roomParty{p: p, muted: muted, isolated: isolated}
	r.order = append(r.order, id)
	if !r.started {
		r.started = true
		r.stop = make(chan struct{})
		r.done = make(chan struct{})
		go r.run(r.stop, r.done)
	}
}

// Remove takes a party out of the room (it does not close the party).
func (r *Room) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.parties[id]; !ok {
		return
	}
	delete(r.parties, id)
	for i, v := range r.order {
		if v == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Len is how many parties the room holds.
func (r *Room) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.parties)
}

// SetMuted mutes or unmutes a party's microphone.
func (r *Room) SetMuted(id string, muted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rp := r.parties[id]; rp != nil {
		rp.muted = muted
	}
}

// Admit moves a waiting party into the mix.
func (r *Room) Admit(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rp := r.parties[id]; rp != nil {
		rp.isolated = false
		rp.private = nil
	}
}

// SetHold sets the looping source an isolated party hears (nil is silence).
// This is where music on hold plugs in: the caller resamples the source to
// MixRate once and hands the samples over.
func (r *Room) SetHold(id string, hold []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rp := r.parties[id]; rp != nil {
		rp.hold, rp.holdPos = hold, 0
	}
}

// SayTo queues a sound only one party hears.
func (r *Room) SayTo(id string, samples []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rp := r.parties[id]; rp != nil {
		rp.private = append(rp.private, samples...)
	}
}

// SayAll queues a sound every admitted party hears (an entry tone, an
// announcement). Sounds queue one after the other.
func (r *Room) SayAll(samples []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shared = append(r.shared, samples...)
}

// SharedPending is how many samples of room-wide sound are still queued, so
// a caller can wait for an announcement to finish.
func (r *Room) SharedPending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.shared)
}

// PrivatePending is how many samples of one party's private sound remain.
func (r *Room) PrivatePending(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rp := r.parties[id]; rp != nil {
		return len(rp.private)
	}
	return 0
}

// SetTap sets a function that receives the room's full mix (every talker,
// nobody subtracted) once per tick while at least one party is admitted:
// what a recording captures. It runs on the mixer goroutine and must not
// block.
func (r *Room) SetTap(f func(frame []int16)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tap = f
}

// Close stops the room's clock and waits for it. It is safe to call more
// than once; the parties are not closed.
func (r *Room) Close() {
	r.mu.Lock()
	stop, done := r.stop, r.done
	r.stop, r.started = nil, false
	r.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// Stats returns the clock ticks, overruns and frames sent.
func (r *Room) Stats() (ticks, overruns, frames uint64) {
	return r.ticks.Load(), r.overrun.Load(), r.mixed.Load()
}

func (r *Room) run(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(frameDur)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		start := time.Now()
		r.tick()
		r.ticks.Add(1)
		if time.Since(start) > frameDur {
			r.overrun.Add(1)
		}
	}
}

// take pulls n samples off the front of q.
func take(q *[]int16, n int) []int16 {
	if len(*q) == 0 {
		return nil
	}
	m := min(n, len(*q))
	out := make([]int16, n)
	copy(out, (*q)[:m])
	*q = (*q)[m:]
	if len(*q) == 0 {
		*q = nil
	}
	return out
}

func (r *Room) tick() {
	r.mu.Lock()
	in := map[string][]int16{}
	outs := make([]string, 0, len(r.order))
	parts := make([]*roomParty, 0, len(r.order))
	for _, id := range r.order {
		rp := r.parties[id]
		f := rp.p.pop() // always drained, so a muted party's buffer cannot grow
		if !rp.muted && !rp.isolated && f != nil {
			in[id] = f
		}
		outs = append(outs, id)
		parts = append(parts, rp)
	}
	shared := take(&r.shared, MixFrame)
	tap := r.tap
	admitted := false
	for _, rp := range parts {
		if !rp.isolated {
			admitted = true
			break
		}
	}
	frames := MixFrames(in, outs, MixFrame)
	var full []int16
	if tap != nil && admitted {
		full = MixAll(in, MixFrame)
		AddInto(full, shared)
	}
	type send struct {
		p *Party
		f []int16
	}
	sends := make([]send, 0, len(parts))
	for i, id := range outs {
		rp := parts[i]
		var f []int16
		if rp.isolated {
			f = make([]int16, MixFrame)
			if len(rp.hold) > 0 {
				for j := range f {
					f[j] = rp.hold[rp.holdPos%len(rp.hold)]
					rp.holdPos++
				}
				rp.holdPos %= len(rp.hold)
			}
		} else {
			f = frames[id]
			AddInto(f, shared)
		}
		if priv := take(&rp.private, MixFrame); priv != nil {
			AddInto(f, priv)
		}
		sends = append(sends, send{rp.p, f})
	}
	r.mu.Unlock()
	if full != nil {
		tap(full)
	}
	for _, s := range sends {
		if err := s.p.send(s.f); err == nil {
			r.mixed.Add(1)
		}
	}
}
