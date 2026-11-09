package events

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
)

// Consumer group and timings of the ingester.
const (
	Group = "ingest"
	// defaultClaimIdle is how long an entry may stay unacknowledged before
	// another replica takes it over; defaultClaimEvery is how often replicas
	// look.
	defaultClaimIdle  = 60 * time.Second
	defaultClaimEvery = 30 * time.Second
	// DefaultRetentionDays is how long integration_events keeps an event.
	DefaultRetentionDays = 7
	// TicketTTL is how long a stream ticket can be redeemed.
	TicketTTL = 30 * time.Second
	ticketKey = "hello:events:ticket:"
	// replayCap bounds the events one resume replays.
	replayCap = 10000
	purgeLock = "hello:events:purge"
)

// ServiceMetrics are hello-control's event series.
type ServiceMetrics struct {
	IngestLag     prometheus.Histogram
	Ingested      *prometheus.CounterVec
	StreamClients prometheus.Gauge
}

// NewServiceMetrics registers hello_event_ingest_lag_seconds and
// hello_event_stream_clients on reg.
func NewServiceMetrics(reg prometheus.Registerer) *ServiceMetrics {
	m := &ServiceMetrics{
		IngestLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_event_ingest_lag_seconds", Help: "Time from an event's creation to its storage.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 15, 60, 300}}),
		Ingested: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_events_ingested_total", Help: "Events consumed from the stream, by result (stored, duplicate, invalid)."}, []string{"result"}),
		StreamClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_event_stream_clients", Help: "Open SSE event streams on this pod."}),
	}
	reg.MustRegister(m.IngestLag, m.Ingested, m.StreamClients)
	return m
}

// TxHook runs inside the ingest transaction for every newly stored event;
// later phases add webhook deliveries here. A failing hook rolls the batch
// back and it is retried.
type TxHook func(ctx context.Context, tx *sql.Tx, e Entry) error

// Locker takes a cluster-wide advisory lock (store.Store.TryLock).
type Locker interface {
	TryLock(ctx context.Context, key string) (unlock func(), ok bool, err error)
}

// Service is hello-control's side of the event pipeline.
type Service struct {
	// DB is the PostgreSQL database holding integration_events.
	DB *sql.DB
	// Valkey returns the connected client, or nil while connecting.
	Valkey func() valkey.Client
	// Locker serialises the retention job across replicas; nil skips it.
	Locker Locker
	// Consumer names this replica in the consumer group.
	Consumer string
	// RetentionDays is how long events are kept (1 to 30; 7 when zero).
	RetentionDays int
	// ClaimIdle and ClaimEvery tune how a crashed replica's unacknowledged
	// entries are taken over (60s and 30s when zero).
	ClaimIdle, ClaimEvery time.Duration
	// Hooks run in the ingest transaction, in order.
	Hooks   []TxHook
	Log     *slog.Logger
	Metrics *ServiceMetrics

	once sync.Once
	hub  *hub
}

func (s *Service) init() {
	s.once.Do(func() {
		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
		if s.Consumer == "" {
			s.Consumer = "control"
		}
		g := func(n int) {
			if s.Metrics != nil {
				s.Metrics.StreamClients.Set(float64(n))
			}
		}
		s.hub = newHub(g)
	})
}

// Run starts the ingester, the live reader and the retention job, and
// returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.init()
	var wg sync.WaitGroup
	for _, f := range []func(context.Context){s.ingestLoop, s.liveLoop, s.purgeLoop} {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	wg.Wait()
	s.hub.closeAll()
}

// Subscribe registers a live subscriber. The caller must have reserved a slot
// with Acquire and closes the subscription when done.
func (s *Service) Subscribe(f Filter) *Sub {
	s.init()
	return s.hub.subscribe(f)
}

// Acquire reserves one of a credential's stream slots (5 per credential, 200
// per pod); the returned function releases it.
func (s *Service) Acquire(credential string) (release func(), err error) {
	s.init()
	return s.hub.acquire(credential)
}

// Available reports whether the live stream can be served: Valkey is
// connected.
func (s *Service) Available() bool { return s.Valkey != nil && s.Valkey() != nil }

// wait sleeps d or until ctx ends; it reports whether ctx is still live.
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// decode reads one stream entry; ok is false for an entry that is not an
// event this version understands.
func decode(id string, fields map[string]string) (Entry, bool) {
	raw, ok := fields[streamField]
	if !ok {
		return Entry{}, false
	}
	var ev Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.ID == "" || ev.Type == "" {
		return Entry{}, false
	}
	if _, err := ParseCursor(id); err != nil {
		return Entry{}, false
	}
	return Entry{Cursor: id, Event: ev}, true
}

// liveLoop reads the stream with one blocking read and fans it out in
// memory.
func (s *Service) liveLoop(ctx context.Context) {
	last := ""
	for ctx.Err() == nil {
		c := s.Valkey()
		if c == nil {
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		if last == "" {
			// Start at the newest entry: the live stream carries what
			// happens from now on; a resume replays the rest.
			es, err := c.Do(ctx, c.B().Arbitrary("XREVRANGE").Keys(StreamKey).Args("+", "-", "COUNT", "1").Build()).AsXRange()
			if err != nil && !valkey.IsValkeyNil(err) {
				s.Log.Warn("events: reading the stream tail", "error", err)
				if !wait(ctx, 2*time.Second) {
					return
				}
				continue
			}
			if last = "0-0"; len(es) > 0 {
				last = es[0].ID
			}
		}
		res, err := c.Do(ctx, c.B().Arbitrary("XREAD").Args("COUNT", "100", "BLOCK", "5000", "STREAMS").
			Keys(StreamKey).Args(last).Blocking()).AsXRead()
		if err != nil {
			if !valkey.IsValkeyNil(err) && ctx.Err() == nil {
				s.Log.Warn("events: live read failed", "error", err)
				if !wait(ctx, 2*time.Second) {
					return
				}
			}
			continue
		}
		for _, x := range res[StreamKey] {
			last = x.ID
			if e, ok := decode(x.ID, x.FieldValues); ok {
				s.hub.publish(e)
			}
		}
	}
}

// ingestLoop stores events from the stream through the consumer group.
func (s *Service) ingestLoop(ctx context.Context) {
	grouped := false
	pending := true // read this consumer's own unacknowledged entries first
	lastClaim := time.Time{}
	for ctx.Err() == nil {
		c := s.Valkey()
		if c == nil {
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		if !grouped {
			err := c.Do(ctx, c.B().Arbitrary("XGROUP").Args("CREATE", StreamKey, Group, "0", "MKSTREAM").Build()).Error()
			if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
				s.Log.Warn("events: creating the consumer group", "error", err)
				if !wait(ctx, 2*time.Second) {
					return
				}
				continue
			}
			grouped = true
		}
		var batch []rawEntry
		if time.Since(lastClaim) >= cmp.Or(s.ClaimEvery, defaultClaimEvery) {
			lastClaim = time.Now()
			batch = s.autoclaim(ctx, c)
		}
		if len(batch) == 0 {
			id, block := ">", "2000"
			if pending {
				id, block = "0", "1" // BLOCK is ignored for history reads
			}
			res, err := c.Do(ctx, c.B().Arbitrary("XREADGROUP").
				Args("GROUP", Group, s.Consumer, "COUNT", "100", "BLOCK", block, "STREAMS").Keys(StreamKey).Args(id).Blocking()).AsXRead()
			switch {
			case err != nil && valkey.IsValkeyNil(err):
				pending = false
				continue
			case err != nil:
				if ctx.Err() != nil {
					return
				}
				if strings.Contains(err.Error(), "NOGROUP") {
					grouped = false
				}
				s.Log.Warn("events: group read failed", "error", err)
				if !wait(ctx, 2*time.Second) {
					return
				}
				continue
			}
			for _, x := range res[StreamKey] {
				batch = append(batch, rawEntry{x.ID, x.FieldValues})
			}
			if pending && len(batch) == 0 {
				pending = false
				continue
			}
		}
		if err := s.store(ctx, c, batch); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Log.Error("events: storing a batch; it is retried", "error", err)
			pending = true
			if !wait(ctx, 2*time.Second) {
				return
			}
		}
	}
}

type rawEntry struct {
	id     string
	fields map[string]string
}

// autoclaim takes over entries another replica read and never acknowledged
// (it crashed), so none is lost.
func (s *Service) autoclaim(ctx context.Context, c valkey.Client) []rawEntry {
	msg, err := c.Do(ctx, c.B().Arbitrary("XAUTOCLAIM").Keys(StreamKey).
		Args(Group, s.Consumer, strconv.FormatInt(cmp.Or(s.ClaimIdle, defaultClaimIdle).Milliseconds(), 10), "0-0", "COUNT", "100").Build()).ToArray()
	if err != nil || len(msg) < 2 {
		return nil
	}
	items, err := msg[1].ToArray()
	if err != nil {
		return nil
	}
	var out []rawEntry
	for _, it := range items {
		e, err := it.AsXRangeEntry()
		if err != nil {
			continue // a deleted entry is reported as nil
		}
		out = append(out, rawEntry{e.ID, e.FieldValues})
	}
	return out
}

// store writes a batch in one transaction, runs the hooks and acknowledges.
func (s *Service) store(ctx context.Context, c valkey.Client, batch []rawEntry) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		results = map[string]int{}
		lagging []Entry
	)
	for _, r := range batch {
		e, ok := decode(r.id, r.fields)
		if !ok {
			results["invalid"]++
			continue
		}
		fresh, err := insert(ctx, tx, e)
		if err != nil {
			return err
		}
		if !fresh {
			results["duplicate"]++
			continue
		}
		for _, h := range s.Hooks {
			if err := h(ctx, tx, e); err != nil {
				return err
			}
		}
		results["stored"]++
		lagging = append(lagging, e)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if m := s.Metrics; m != nil {
		for k, n := range results {
			m.Ingested.WithLabelValues(k).Add(float64(n))
		}
		for _, e := range lagging {
			m.IngestLag.Observe(time.Since(e.CreatedAt).Seconds())
		}
	}
	ids := make([]string, len(batch))
	for i, r := range batch {
		ids[i] = r.id
	}
	// An acknowledgement that fails leaves the entries pending: they are
	// read again and found stored (duplicate), which is harmless.
	return c.Do(ctx, c.B().Arbitrary("XACK").Keys(StreamKey).Args(append([]string{Group}, ids...)...).Build()).Error()
}

// purgeLoop deletes events past the retention, on one replica at a time.
func (s *Service) purgeLoop(ctx context.Context) {
	if !wait(ctx, time.Minute) {
		return
	}
	for {
		s.purge(ctx)
		if !wait(ctx, time.Hour) {
			return
		}
	}
}

func (s *Service) purge(ctx context.Context) {
	if s.Locker != nil {
		unlock, ok, err := s.Locker.TryLock(ctx, purgeLock)
		if err != nil || !ok {
			return
		}
		defer unlock()
	}
	days := s.RetentionDays
	if days < 1 || days > 30 {
		days = DefaultRetentionDays
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM integration_events WHERE created_at < now() - make_interval(days => $1)`, days)
	if err != nil {
		s.Log.Warn("events: retention purge failed", "error", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.Log.Info("events: purged old events", "count", n)
	}
}

// Replay calls fn, in cursor order, for every event after the cursor that
// matches f: older ones from integration_events, recent ones (not yet
// ingested, or ingested by a replica still committing) from the stream. It
// stops when fn returns an error or after replayCap events.
func (s *Service) Replay(ctx context.Context, after Cursor, f Filter, fn func(Entry) error) error {
	s.init()
	sent := 0
	emit := func(e Entry) error {
		sent++
		return fn(e)
	}
	var c valkey.Client
	if s.Valkey != nil {
		c = s.Valkey()
	}
	// The stream holds the newest events; the database the older ones.
	var oldest Cursor
	hasOldest := false
	if c != nil {
		es, err := c.Do(ctx, c.B().Arbitrary("XRANGE").Keys(StreamKey).Args("-", "+", "COUNT", "1").Build()).AsXRange()
		if err != nil && !valkey.IsValkeyNil(err) {
			return err
		}
		if len(es) > 0 {
			if oldest, err = ParseCursor(es[0].ID); err == nil {
				hasOldest = true
			}
		}
	}
	// Events older than the stream's first entry come from the database.
	start := "(" + after.String()
	if after.Zero() {
		start = "-"
	}
	if !hasOldest || after.Compare(oldest) < 0 {
		cur := after
		for sent < replayCap {
			q := Query{Filter: f, After: cur, Limit: 500}
			if hasOldest {
				q.Before = oldest
			}
			page, err := s.History(ctx, q)
			if err != nil {
				return err
			}
			for _, e := range page {
				if err := emit(e); err != nil {
					return err
				}
				cur, _ = ParseCursor(e.Cursor)
			}
			if len(page) < 500 {
				break
			}
		}
		if !hasOldest {
			return nil
		}
		start = "-" // the stream's oldest entry is the first one not in the database page
	}
	for sent < replayCap {
		es, err := c.Do(ctx, c.B().Arbitrary("XRANGE").Keys(StreamKey).Args(start, "+", "COUNT", "500").Build()).AsXRange()
		if err != nil && !valkey.IsValkeyNil(err) {
			return err
		}
		for _, x := range es {
			if e, ok := decode(x.ID, x.FieldValues); ok && f.Matches(e.Event) {
				if err := emit(e); err != nil {
					return err
				}
			}
			start = "(" + x.ID
		}
		if len(es) < 500 {
			return nil
		}
	}
	return nil
}

// PutTicket stores a single-use stream ticket (by hash) for TicketTTL.
func (s *Service) PutTicket(ctx context.Context, hash string, payload []byte) error {
	c := s.Valkey()
	if c == nil {
		return errors.New("events: valkey is not connected")
	}
	return c.Do(ctx, c.B().Set().Key(ticketKey+hash).Value(string(payload)).Ex(TicketTTL).Build()).Error()
}

// TakeTicket redeems a ticket exactly once.
func (s *Service) TakeTicket(ctx context.Context, hash string) ([]byte, bool, error) {
	c := s.Valkey()
	if c == nil {
		return nil, false, errors.New("events: valkey is not connected")
	}
	v, err := c.Do(ctx, c.B().Getdel().Key(ticketKey+hash).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(v), true, nil
}
