package chat

import (
	"slices"
	"sync"
	"time"
)

// Adaptive raises the proof-of-work difficulty of a site that is being
// flooded (spec M-29a.3): when the sessions of this minute are more than
// three times the median per-minute rate of the last hour, the difficulty
// goes up one level (two bits) for 15 minutes. It is kept per pod and is
// only ever a hardening: nothing depends on it for correctness.
type Adaptive struct {
	mu     sync.Mutex
	sites  map[int64]*siteRate
	raised func()
}

const (
	adaptiveStep   = 2
	adaptiveFor    = 15 * time.Minute
	adaptiveWindow = 60 // minutes of history
	adaptiveMin    = 10 // sessions in a minute before it counts as a flood
)

type siteRate struct {
	counts [adaptiveWindow]int
	stamp  [adaptiveWindow]int64 // the unix minute each slot holds
	until  time.Time
}

// NewAdaptive returns the tracker; raised is called each time a site's
// difficulty is raised (for the metric).
func NewAdaptive(raised func()) *Adaptive {
	return &Adaptive{sites: map[int64]*siteRate{}, raised: raised}
}

func (s *siteRate) inc(m int64) int {
	i := int(m % adaptiveWindow)
	if s.stamp[i] != m {
		s.stamp[i], s.counts[i] = m, 0
	}
	s.counts[i]++
	return s.counts[i]
}

// median is the median per-minute count of the 59 minutes before m, a minute
// with no sessions counting as 0.
func (s *siteRate) median(m int64) int {
	v := make([]int, 0, adaptiveWindow-1)
	for k := int64(1); k < adaptiveWindow; k++ {
		i := int((m - k) % adaptiveWindow)
		if s.stamp[i] == m-k {
			v = append(v, s.counts[i])
		} else {
			v = append(v, 0)
		}
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// Observe records a session on a site at now.
func (a *Adaptive) Observe(siteID int64, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sites[siteID]
	if s == nil {
		if len(a.sites) > 10000 {
			clear(a.sites)
		}
		s = &siteRate{}
		a.sites[siteID] = s
	}
	m := now.Unix() / 60
	cur := s.inc(m)
	if now.Before(s.until) {
		return
	}
	if cur >= adaptiveMin && cur > 3*max(s.median(m), 1) {
		s.until = now.Add(adaptiveFor)
		if a.raised != nil {
			a.raised()
		}
	}
}

// Extra is the bits to add to a site's difficulty at now.
func (a *Adaptive) Extra(siteID int64, now time.Time) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sites[siteID]; s != nil && now.Before(s.until) {
		return adaptiveStep
	}
	return 0
}
