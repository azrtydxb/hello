package sip

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/prometheus/client_golang/prometheus"
)

// TrunkState is the shared trunk state (leases, registration, health, call
// slots); *livestate.Store satisfies it.
type TrunkState interface {
	AcquireLease(ctx context.Context, key, node string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, key, node string) error
	PutTrunkRegistration(ctx context.Context, id int64, r livestate.TrunkRegistration, ttl time.Duration) error
	PutDestinationHealth(ctx context.Context, id int64, h livestate.DestinationHealth, ttl time.Duration) error
	AcquireTrunkCall(ctx context.Context, id int64, call string, max int, ttl time.Duration) (bool, error)
	RefreshTrunkCall(ctx context.Context, id int64, call string, max int, ttl time.Duration) (livestate.SlotRefresh, error)
	ReleaseTrunkCall(ctx context.Context, id int64, call string) error
	TrunkStatus(ctx context.Context, id int64) (livestate.TrunkStatus, error)
}

// trunkManager runs, for every enabled trunk this node holds the lease of,
// the trunk's registration and OPTIONS health checks, and on every node
// keeps a cache of the shared trunk status for routing and metrics.
type trunkManager struct {
	s *Server

	mu   sync.Mutex
	held map[int64]*heldTrunk

	status atomic.Pointer[map[int64]cachedStatus]
	// series are the metric label values this node has set, so those of
	// deleted or renamed trunks and destinations can be dropped.
	series map[string]map[string]bool // trunk name -> destinations
}

// cachedStatus is one trunk's last read status. A failed read keeps the
// previous status and marks it failing; it stays usable for 3 polls.
type cachedStatus struct {
	st      livestate.TrunkStatus
	at      time.Time // last successful read
	failing bool
}

type heldTrunk struct {
	trunk  routing.Trunk
	bad    string // misconfigured reason
	cancel context.CancelFunc
	done   chan struct{}
}

func newTrunkManager(s *Server) *trunkManager {
	return &trunkManager{s: s, held: map[int64]*heldTrunk{}, series: map[string]map[string]bool{}}
}

func (m *trunkManager) state() TrunkState { return m.s.deps.Trunks }

// run drives the lease and status loops until ctx ends, then stops every
// holder (unregistering) and releases the leases.
func (m *trunkManager) run(ctx context.Context) {
	if m.state() == nil {
		return
	}
	// The status poll has its own goroutine: a slow lease operation or
	// holder shutdown (unregistering) must not let the cache go stale.
	m.poll(ctx)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		t := time.NewTicker(m.s.cfg.TrunkStatusPoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.poll(ctx)
			}
		}
	}()
	defer func() { <-pollDone }()
	lease := time.NewTicker(m.s.cfg.TrunkLeaseRefresh)
	defer lease.Stop()
	m.leases(ctx)
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-lease.C:
			m.leases(ctx)
		}
	}
}

func (m *trunkManager) routing() *snapshot.RoutingState {
	snap := m.s.deps.Snapshots.Current()
	if snap == nil {
		return nil
	}
	return snap.Routing()
}

// leases takes or renews the lease of every enabled trunk and starts or
// stops this node's holder for it. Another node's unexpired lease is never
// taken; when Valkey cannot be reached a running holder keeps going (its
// registration stays valid until it expires).
func (m *trunkManager) leases(ctx context.Context) {
	rs := m.routing()
	if rs == nil {
		return
	}
	ttl := 3 * m.s.cfg.TrunkLeaseRefresh
	want := map[int64]bool{}
	for _, t := range rs.Config.Trunks {
		if !t.Enabled {
			continue
		}
		want[t.ID] = true
		lctx, cancel := m.s.stateCtx()
		ok, err := m.state().AcquireLease(lctx, livestate.TrunkLeaseKey(t.ID), m.s.cfg.NodeID, ttl)
		cancel()
		m.mu.Lock()
		h := m.held[t.ID]
		m.mu.Unlock()
		switch {
		case err != nil:
			m.s.log.Warn("trunk lease unavailable", "trunk", t.Name, "error", err)
		case ok && h == nil:
			m.start(ctx, t, rs.Misconfigured[t.ID])
		case ok && (!reflect.DeepEqual(h.trunk, t) || h.bad != rs.Misconfigured[t.ID]):
			m.stop(t.ID, false) // configuration changed: restart with it
			m.start(ctx, t, rs.Misconfigured[t.ID])
		case !ok && h != nil:
			m.s.log.Info("trunk lease lost to another node", "trunk", t.Name)
			m.stop(t.ID, false)
		}
	}
	m.mu.Lock()
	var gone []int64
	for id := range m.held {
		if !want[id] {
			gone = append(gone, id)
		}
	}
	m.mu.Unlock()
	for _, id := range gone { // disabled or deleted
		m.stop(id, true)
	}
}

func (m *trunkManager) start(ctx context.Context, t routing.Trunk, bad string) {
	hctx, cancel := context.WithCancel(ctx)
	h := &heldTrunk{trunk: t, bad: bad, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.held[t.ID] = h
	m.mu.Unlock()
	m.s.log.Info("trunk lease held: registering and checking health", "trunk", t.Name)
	go func() {
		defer close(h.done)
		defer contain(m.s.log, "trunk holder")
		m.hold(hctx, h)
	}()
}

// stop ends this node's holder for a trunk; release also gives up the
// lease, so another node can take it at once.
func (m *trunkManager) stop(id int64, release bool) {
	m.mu.Lock()
	h := m.held[id]
	delete(m.held, id)
	m.mu.Unlock()
	if h == nil {
		return
	}
	h.cancel()
	<-h.done
	if release {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := m.state().ReleaseLease(ctx, livestate.TrunkLeaseKey(id), m.s.cfg.NodeID); err != nil {
			m.s.log.Warn("trunk lease release failed", "trunk_id", id, "error", err)
		}
		cancel()
	}
}

func (m *trunkManager) stopAll() {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.held))
	for id := range m.held {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.stop(id, true)
	}
}

// hold is the lease holder's work for one trunk: OPTIONS to every
// destination and, for a registration trunk, the registration loop.
func (m *trunkManager) hold(ctx context.Context, h *heldTrunk) {
	t := h.trunk
	var wg sync.WaitGroup
	for _, d := range t.Destinations {
		wg.Go(func() { m.optionsLoop(ctx, t, d) })
	}
	switch {
	case h.bad != "":
		m.s.log.Error("trunk misconfigured: not registering", "trunk", t.Name, "reason", h.bad)
		m.publishMisconfigured(ctx, t)
	case t.Mode == "registration":
		m.registerLoop(ctx, t)
	}
	wg.Wait()
}

func (m *trunkManager) publishMisconfigured(ctx context.Context, t routing.Trunk) {
	tick := time.NewTicker(m.s.cfg.TrunkLeaseRefresh)
	defer tick.Stop()
	for {
		m.putRegistration(t, livestate.TrunkRegistration{State: "misconfigured"}, 3*m.s.cfg.TrunkLeaseRefresh)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *trunkManager) putRegistration(t routing.Trunk, r livestate.TrunkRegistration, ttl time.Duration) {
	r.Node, r.UpdatedAt = m.s.cfg.NodeID, time.Now()
	ctx, cancel := m.s.stateCtx()
	defer cancel()
	if err := m.state().PutTrunkRegistration(ctx, t.ID, r, ttl); err != nil {
		m.s.log.Warn("could not publish trunk registration", "trunk", t.Name, "error", err)
	}
}

// destAddr is where to send to a destination: its first resolved address,
// else host:port as configured (sipgo resolves it then).
func (m *trunkManager) destAddr(d routing.Destination) string {
	if rs := m.routing(); rs != nil {
		if a := rs.Resolved[livestate.DestinationKey(d)]; len(a) > 0 {
			return a[0]
		}
	}
	port := d.Port
	if port == 0 {
		port = sip.DefaultPort("udp")
	}
	return d.Host + ":" + strconv.Itoa(port)
}

// trunkDomain is the domain a trunk's AOR and From use.
func trunkDomain(t routing.Trunk) string {
	switch {
	case t.FromDomain != "":
		return t.FromDomain
	case t.Realm != "":
		return t.Realm
	case len(t.Destinations) > 0:
		return t.Destinations[0].Host
	}
	return ""
}

func firstDestination(t routing.Trunk) (routing.Destination, bool) {
	if len(t.Destinations) == 0 {
		return routing.Destination{}, false
	}
	best := t.Destinations[0]
	for _, d := range t.Destinations[1:] {
		if d.Priority < best.Priority {
			best = d
		}
	}
	return best, true
}

// registerLoop keeps the trunk registered: REGISTER (answering the
// carrier's digest challenge with the trunk credentials), re-REGISTER at 80%
// of the granted expiry, and back off exponentially to TrunkRetryMax on
// failure. On exit it unregisters.
func (m *trunkManager) registerLoop(ctx context.Context, t routing.Trunk) {
	d, ok := firstDestination(t)
	if !ok {
		m.putRegistration(t, livestate.TrunkRegistration{State: "failed"}, 3*m.s.cfg.TrunkLeaseRefresh)
		return
	}
	backoff := m.s.cfg.TrunkRetryBase
	registered := false
	defer func() {
		if registered {
			uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _, _ = m.register(uctx, t, d, 0)
			cancel()
		}
	}()
	for {
		m.putRegistration(t, livestate.TrunkRegistration{State: "registering"}, 3*m.s.cfg.TrunkLeaseRefresh)
		code, granted, err := m.register(ctx, t, d, t.RegisterExpires)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if err == nil && code/100 == 2 {
			registered = true
			backoff = m.s.cfg.TrunkRetryBase
			exp := time.Now().Add(granted)
			m.putRegistration(t, livestate.TrunkRegistration{State: "registered", LastCode: code, Expires: exp},
				max(granted, 3*m.s.cfg.TrunkLeaseRefresh))
			// At 80% of the expiry, but never sooner than the floor: a carrier
			// granting seconds must not make us REGISTER in a tight loop.
			wait = max(granted*8/10, m.s.cfg.TrunkReRegisterMin)
		} else {
			registered = false
			m.s.log.Warn("trunk registration failed", "trunk", t.Name, "code", code, "error", err, "retry_in", backoff.String())
			m.putRegistration(t, livestate.TrunkRegistration{State: "failed", LastCode: code}, max(backoff, 3*m.s.cfg.TrunkLeaseRefresh))
			wait = backoff
			backoff = min(2*backoff, m.s.cfg.TrunkRetryMax)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// register sends one REGISTER (expires 0 unregisters) and returns the final
// code and the expiry the carrier granted.
func (m *trunkManager) register(ctx context.Context, t routing.Trunk, d routing.Destination, expires time.Duration) (int, time.Duration, error) {
	domain := trunkDomain(t)
	host, port, _ := sip.ParseAddr(m.s.cfg.AdvertisedAddr)
	aor := sip.Uri{Scheme: "sip", User: t.Username, Host: domain}
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: domain})
	req.AppendHeader(&sip.FromHeader{Address: aor, Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}}})
	req.AppendHeader(&sip.ToHeader{Address: aor})
	req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: t.Username, Host: host, Port: port}})
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(int(expires/time.Second))))
	req.SetDestination(m.destAddr(d))
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := m.s.client.Do(rctx, req)
	if err != nil {
		return 0, 0, err
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		if t.Password == "" {
			return res.StatusCode, 0, errors.New("challenged but the trunk has no password")
		}
		res, err = m.s.client.DoDigestAuth(rctx, req, res, sipgo.DigestAuth{Username: t.Username, Password: t.Password})
		if err != nil {
			return 0, 0, err
		}
	}
	granted := expires
	if res.StatusCode/100 == 2 {
		granted = grantedExpiry(res, req.Contact(), expires)
	}
	return res.StatusCode, granted, nil
}

// grantedExpiry reads the expiry the registrar granted for our contact:
// its expires parameter, else the Expires header, else what we asked for.
func grantedExpiry(res *sip.Response, ours *sip.ContactHeader, asked time.Duration) time.Duration {
	for _, h := range res.GetHeaders("Contact") {
		c, ok := h.(*sip.ContactHeader)
		if !ok || ours == nil || !strings.EqualFold(c.Address.Host, ours.Address.Host) || c.Address.Port != ours.Address.Port {
			continue
		}
		if v, ok := c.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}
	if h := res.GetHeader("Expires"); h != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(h.Value())); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return asked
}

// optionsLoop checks one destination every OptionsInterval: up after any
// final response, down after a timeout.
func (m *trunkManager) optionsLoop(ctx context.Context, t routing.Trunk, d routing.Destination) {
	interval := t.OptionsInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		m.options(ctx, t, d, interval)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *trunkManager) options(ctx context.Context, t routing.Trunk, d routing.Destination, interval time.Duration) {
	req := sip.NewRequest(sip.OPTIONS, sip.Uri{Scheme: "sip", Host: d.Host, Port: d.Port})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "hello", Host: m.s.cfg.Domain}, Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", Host: d.Host, Port: d.Port}})
	req.SetDestination(m.destAddr(d))
	timeout := min(m.s.cfg.TrunkOptionsTimeout, interval)
	octx, cancel := context.WithTimeout(ctx, timeout)
	start := time.Now()
	res, err := m.s.client.Do(octx, req)
	cancel()
	if ctx.Err() != nil {
		return
	}
	h := livestate.DestinationHealth{Destination: livestate.DestinationKey(d), CheckedAt: time.Now()}
	if err == nil {
		h.Up, h.LastCode, h.Latency = true, res.StatusCode, time.Since(start)
	}
	pctx, pcancel := m.s.stateCtx()
	defer pcancel()
	if err := m.state().PutDestinationHealth(pctx, t.ID, h, 3*interval); err != nil {
		m.s.log.Warn("could not publish trunk health", "trunk", t.Name, "error", err)
	}
}

// poll refreshes the status cache (and the trunk metrics) from the shared
// state. It runs on every node, so routing everywhere sees the holder's
// health and the cluster's call counts.
func (m *trunkManager) poll(ctx context.Context) {
	rs := m.routing()
	if rs == nil {
		return
	}
	var old map[int64]cachedStatus
	if p := m.status.Load(); p != nil {
		old = *p
	}
	out := make(map[int64]cachedStatus, len(rs.Config.Trunks))
	for _, t := range rs.Config.Trunks {
		pctx, cancel := context.WithTimeout(ctx, max(m.s.cfg.StateTimeout, time.Second))
		st, err := m.state().TrunkStatus(pctx, t.ID)
		cancel()
		if err != nil {
			prev := old[t.ID] // keep the last known status, judged per trunk
			prev.failing = true
			out[t.ID] = prev
			continue
		}
		out[t.ID] = cachedStatus{st: st, at: time.Now()}
		m.setMetrics(t, st)
	}
	m.status.Store(&out)
	m.pruneSeries(rs.Config.Trunks)
}

// pruneSeries drops the metric series of trunks and destinations that are
// no longer configured (deleted or renamed).
func (m *trunkManager) pruneSeries(trunks []routing.Trunk) {
	want := map[string]map[string]bool{}
	for _, t := range trunks {
		ds := map[string]bool{}
		for _, d := range t.Destinations {
			ds[livestate.DestinationKey(d)] = true
		}
		want[t.Name] = ds
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, dests := range m.series {
		wd, ok := want[name]
		if !ok {
			for _, v := range []interface {
				DeletePartialMatch(prometheus.Labels) int
			}{m.s.m.TrunkStatus, m.s.m.TrunkRegistered, m.s.m.TrunkOptionsLatency, m.s.m.TrunkCalls, m.s.m.TrunkActiveCalls} {
				v.DeletePartialMatch(prometheus.Labels{"trunk": name})
			}
			delete(m.series, name)
			continue
		}
		for d := range dests {
			if !wd[d] {
				m.s.m.TrunkStatus.DeleteLabelValues(name, d)
				m.s.m.TrunkOptionsLatency.DeleteLabelValues(name, d)
				delete(dests, d)
			}
		}
	}
}

func (m *trunkManager) noteSeries(trunk, dest string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.series[trunk] == nil {
		m.series[trunk] = map[string]bool{}
	}
	if dest != "" {
		m.series[trunk][dest] = true
	}
}

func (m *trunkManager) setMetrics(t routing.Trunk, st livestate.TrunkStatus) {
	reg := 0.0
	if st.Registration != nil && st.Registration.State == "registered" {
		reg = 1
	}
	m.s.m.TrunkRegistered.WithLabelValues(t.Name).Set(reg)
	m.s.m.TrunkActiveCalls.WithLabelValues(t.Name).Set(float64(st.ActiveCalls))
	m.noteSeries(t.Name, "")
	configured := map[string]bool{}
	for _, d := range t.Destinations {
		configured[livestate.DestinationKey(d)] = true
	}
	for _, h := range st.Destinations {
		if !configured[h.Destination] {
			continue // a removed destination's record, until its TTL
		}
		m.noteSeries(t.Name, h.Destination)
		up := 0.0
		if h.Up {
			up = 1
			m.s.m.TrunkOptionsLatency.WithLabelValues(t.Name, h.Destination).Set(h.Latency.Seconds())
		}
		m.s.m.TrunkStatus.WithLabelValues(t.Name, h.Destination).Set(up)
	}
}

// statusOf returns the cached status of a trunk and whether the cache is
// fresh (the last full poll is recent, i.e. Valkey is reachable).
//
// Freshness is per trunk: stale only when the last read of this trunk
// failed and its last good read is older than three polls. A trunk not read
// yet (just added) is fresh with nothing known.
func (m *trunkManager) statusOf(id int64) (livestate.TrunkStatus, bool, bool) {
	p := m.status.Load()
	if p == nil {
		return livestate.TrunkStatus{}, false, true
	}
	e, known := (*p)[id]
	if !known {
		return livestate.TrunkStatus{}, false, true
	}
	fresh := !e.failing || time.Since(e.at) <= 3*m.s.cfg.TrunkStatusPoll
	return e.st, !e.at.IsZero(), fresh
}

// destinationDown reports whether the last known health of d is down.
func (m *trunkManager) destinationDown(id int64, d routing.Destination) bool {
	st, known, _ := m.statusOf(id)
	if !known {
		return false
	}
	for _, h := range st.Destinations {
		if h.Destination == livestate.DestinationKey(d) {
			return !h.Up
		}
	}
	return false // never checked: assume up
}

// usability is the routing engine's TrunkUsability for one decision. The
// rule itself is livestate.TrunkStatus.Usability, shared with the route
// tester; this adds what only the call path knows: a password that did not
// open in this node's snapshot, and an unreachable trunk state (refused
// with "state unavailable" except for emergency routes, which use the last
// known status). "full" is left to the call slot taken at attempt time.
func (s *Server) usability(rs *snapshot.RoutingState) routing.TrunkUsability {
	return func(id int64, emergency bool) (bool, string) {
		t, ok := rs.Router.Trunk(id)
		if !ok {
			return false, "unknown trunk"
		}
		if !t.Enabled {
			return false, "disabled"
		}
		if _, bad := rs.Misconfigured[id]; bad {
			return false, "misconfigured"
		}
		if s.deps.Trunks == nil {
			return false, "state unavailable"
		}
		st, _, fresh := s.trunks.statusOf(id)
		if !fresh && !emergency {
			return false, "state unavailable"
		}
		// Fullness is decided at attempt time by AcquireTrunkCall, which is
		// exact; the cached count lags by up to TrunkStatusPoll and would
		// skip a trunk whose slot was just freed.
		st.ActiveCalls = 0
		return st.Usability(*t, emergency)
	}
}
