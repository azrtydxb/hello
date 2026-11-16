package cdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
	"github.com/azrtydxb/momo/momotest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	hellodb "github.com/azrtydxb/hello/internal/db"
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/routing"
)

var discard = slog.New(slog.DiscardHandler)

func count(c prometheus.Counter) float64 {
	var m dto.Metric
	_ = c.Write(&m)
	return m.GetCounter().GetValue()
}

// flakyDB is a momo driver whose Exec fails the first fail calls and then
// records the first bound argument of each statement, the record's
// correlation id ($1 of the insert, TestInsertBindsEveryColumn). Query fails,
// so the call.completed lookup reports cdrId 0, as a handle without rows
// did before.
type flakyDB struct {
	mu     sync.Mutex
	fail   int // fail this many calls first
	calls  int
	rows   []string
	sqls   []string
	args   []any
	unlock chan struct{}
}

func (f *flakyDB) Dialect() *dialect.Dialect { return dialect.For(dialect.Postgres) }

func (f *flakyDB) Exec(_ context.Context, sql string, args []any) (int64, error) {
	if f.unlock != nil {
		<-f.unlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.fail {
		return 0, errors.New("database down")
	}
	f.rows = append(f.rows, args[0].(string))
	f.sqls, f.args = append(f.sqls, sql), args
	return 1, nil
}

func (f *flakyDB) Query(context.Context, string, []any) (momo.Rows, error) {
	return nil, errors.New("no rows here")
}

func (f *flakyDB) Begin(context.Context, momo.TxOptions) (momo.DriverTx, error) {
	return nil, errors.New("no transactions here")
}

func (f *flakyDB) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rows...)
}

// handle returns the momo handle the Writer gets in production, on f.
func (f *flakyDB) handle() *momo.DB { return momo.New(f) }

func newCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: "hello_cdr_dropped_total"})
}

// TestWriterRetries fails if a record is lost while the database fails a
// few times, or written twice.
func TestWriterRetries(t *testing.T) {
	db := &flakyDB{fail: 3}
	w := NewWriter(db.handle(), newCounter(), discard)
	w.InitialBackoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, time.Second); close(done) }()
	w.Enqueue(Record{CorrelationID: "a"})
	w.Enqueue(Record{CorrelationID: "b"})
	deadline := time.Now().Add(5 * time.Second)
	for len(db.written()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if got := db.written(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("written = %v, want [a b] in order after retries", got)
	}
}

// TestWriterDropsWhenFull fails if Enqueue blocks or does not count a drop
// when the queue is full.
func TestWriterDropsWhenFull(t *testing.T) {
	dropped := newCounter()
	w := newWriter((&flakyDB{}).handle(), dropped, discard, 2)
	if !w.Enqueue(Record{CorrelationID: "1"}) || !w.Enqueue(Record{CorrelationID: "2"}) {
		t.Fatal("enqueue into a non-full queue failed")
	}
	start := time.Now()
	if w.Enqueue(Record{CorrelationID: "3"}) {
		t.Fatal("enqueue into a full queue succeeded")
	}
	if time.Since(start) > 50*time.Millisecond || count(dropped) != 1 {
		t.Fatalf("drop took %s, counted %v", time.Since(start), count(dropped))
	}
	if w := NewWriter((&flakyDB{}).handle(), dropped, discard); w.queue == nil || cap(w.queue) != QueueSize {
		t.Fatal("default queue size")
	}
}

// TestWriterFlushOnShutdown fails if queued records are not written after
// the context is cancelled.
func TestWriterFlushOnShutdown(t *testing.T) {
	db := &flakyDB{unlock: make(chan struct{})}
	w := NewWriter(db.handle(), newCounter(), discard)
	for i := range 3 {
		w.Enqueue(Record{CorrelationID: fmt.Sprint(i)})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	close(db.unlock)
	w.Run(ctx, time.Second)
	if got := db.written(); len(got) != 3 {
		t.Fatalf("flushed %v, want 3", got)
	}
}

// TestWriterPostgres fails if a record does not land in the cdrs table with
// NULL for unset times, or a retried record is inserted twice.
func TestWriterPostgres(t *testing.T) {
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	defer func() { _ = admin.Close() }()
	name := fmt.Sprintf("hello_cdr_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") }()
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, _ := pgx.ParseConfig(u.String())
	sdb := stdlib.OpenDB(*scfg)
	if _, err := migrate.Up(ctx, sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	w := NewWriter(hellodb.NewPool(pool, discard), newCounter(), discard)
	sink := &eventSink{}
	w.Events = sink
	start := time.Now().Add(-10 * time.Second).UTC().Truncate(time.Millisecond)
	r := Record{CorrelationID: "corr-1", SIPCallID: "call-1", Source: "100", Destination: "200",
		StartTime: start, AnswerTime: start.Add(2 * time.Second), EndTime: start.Add(10 * time.Second),
		DurationMs: 10000, BillableMs: 8000, SIPNode: "sip-1", FinalStatus: 200, TerminationSide: SideCaller}
	r.Events = []events.Event{{Type: "call.completed"}}
	if err := w.write(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := w.write(ctx, r); err != nil { // retry after an ambiguous failure
		t.Fatal(err)
	}
	var storedID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	if got := sink.cdrIDs(); len(got) != 2 || got[0] != storedID || got[1] != storedID {
		t.Fatalf("call.completed cdrIds = %v, want the stored row's id %d twice (once per write)", got, storedID)
	}
	var (
		n            int
		ring, answer *time.Time
		media, side  string
		billable     int64
	)
	if err := pool.QueryRow(ctx, "SELECT count(*), max(ring_time), max(answer_time), max(media_mode), max(termination_side), max(billable_ms) FROM cdrs").
		Scan(&n, &ring, &answer, &media, &side, &billable); err != nil {
		t.Fatal(err)
	}
	if n != 1 || ring != nil || answer == nil || !answer.Equal(r.AnswerTime) || media != "direct" || side != "caller" || billable != 8000 {
		t.Fatalf("row: n=%d ring=%v answer=%v media=%s side=%s billable=%d", n, ring, answer, media, side, billable)
	}
	var qp, ql *int64
	var qj *float64
	if err := pool.QueryRow(ctx, "SELECT rtp_packets, rtp_lost, rtp_jitter_ms FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&qp, &ql, &qj); err != nil || qp != nil || ql != nil || qj != nil {
		t.Fatalf("unmeasured quality = %v %v %v, %v", qp, ql, qj, err)
	}
	var cc, ce *string
	if err := pool.QueryRow(ctx, "SELECT caller_codec, callee_codec FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&cc, &ce); err != nil || cc != nil || ce != nil {
		t.Fatalf("codecs of a record without any = %v %v, %v, want NULL", cc, ce, err)
	}
	codec := Record{CorrelationID: "corr-codec", SIPCallID: "call-codec", StartTime: start, EndTime: start.Add(time.Second),
		SIPNode: "sip-1", FinalStatus: 200, TerminationSide: SideCaller, CallerCodec: "opus/48000", CalleeCodec: "PCMU/8000"}
	if err := w.write(ctx, codec); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT caller_codec, callee_codec FROM cdrs WHERE correlation_id = 'corr-codec'").Scan(&cc, &ce); err != nil ||
		cc == nil || ce == nil || *cc != "opus/48000" || *ce != "PCMU/8000" {
		t.Fatalf("stored codecs = %v %v, %v", cc, ce, err)
	}
	var dir string
	if err := pool.QueryRow(ctx, "SELECT direction FROM cdrs WHERE correlation_id = 'corr-1'").Scan(&dir); err != nil || dir != "internal" {
		t.Fatalf("default direction = %q, %v", dir, err)
	}

	// An outbound call's routing detail and trace.
	var tr routing.Trace
	tr.Add(`Route "UAE Mobile" matched (regex ^05[0-9]{8}$)`)
	tr.Add("carrier-backup -> 200 OK")
	out := Record{CorrelationID: "corr-2", SIPCallID: "call-2", Source: "100", Destination: "0501234567",
		StartTime: start, EndTime: start.Add(time.Second), SIPNode: "sip-1", FinalStatus: 200, TerminationSide: SideCaller,
		Direction: DirectionOutbound, OriginalDestination: "0501234567", RewrittenDestination: "+971501234567",
		Route: "UAE Mobile", Trunk: "carrier-backup", Trace: tr}
	if err := w.write(ctx, out); err != nil {
		t.Fatal(err)
	}
	var (
		orig, rewritten, route, trunk string
		raw                           []byte
	)
	if err := pool.QueryRow(ctx, `SELECT direction, original_destination, rewritten_destination, route_name, trunk_name, trace
		FROM cdrs WHERE correlation_id = 'corr-2'`).Scan(&dir, &orig, &rewritten, &route, &trunk, &raw); err != nil {
		t.Fatal(err)
	}
	var got routing.Trace
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if dir != "outbound" || orig != "0501234567" || rewritten != "+971501234567" || route != "UAE Mobile" || trunk != "carrier-backup" ||
		len(got) != 2 || got[1].N != 2 || got[1].Text != "carrier-backup -> 200 OK" {
		t.Fatalf("outbound row: %s %s %s %s %s %+v", dir, orig, rewritten, route, trunk, got)
	}
}

// eventSink collects the events the writer publishes.
type eventSink struct {
	mu  sync.Mutex
	got []events.Event
}

func (s *eventSink) Emit(e events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, e)
}

func (s *eventSink) cdrIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, e := range s.got {
		out = append(out, e.Data.CDRID)
	}
	return out
}

// bound returns the insert's arguments of r by @name.
func bound(t *testing.T, r Record) map[string]any {
	t.Helper()
	args, err := insertArgs(r)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	for _, a := range args {
		na, ok := a.(momo.NamedArg)
		if !ok {
			t.Fatalf("insert argument %#v is not named", a)
		}
		if _, dup := m[na.Name]; dup {
			t.Fatalf("@%s bound twice", na.Name)
		}
		m[na.Name] = na.Value
	}
	return m
}

// TestInsertBindsEveryColumn fails if a cdrs column of the insert is not
// bound by the @name of that column, in the same position, or if the insert
// does not take exactly the arguments insertArgs gives: a new column is then
// one column, one @name and one argument, with nothing renumbered.
func TestInsertBindsEveryColumn(t *testing.T) {
	head := insert[strings.Index(insert, "INSERT INTO cdrs (")+len("INSERT INTO cdrs ("):]
	cols := strings.Split(head[:strings.Index(head, ")")], ",")
	vals := head[strings.Index(head, "VALUES (")+len("VALUES ("):]
	binds := strings.Split(vals[:strings.Index(vals, ")")], ",")
	if len(cols) != len(binds) {
		t.Fatalf("%d columns, %d values", len(cols), len(binds))
	}
	for i := range cols {
		c, b := strings.TrimSpace(cols[i]), strings.TrimSpace(binds[i])
		if b != "@"+c {
			t.Fatalf("column %d %s is bound by %s, want @%s", i+1, c, b, c)
		}
	}
	m := bound(t, Record{})
	for _, legs := range []string{"page_legs", "follow_me_legs", "queue_legs"} {
		if !strings.Contains(insert, "@"+legs+"::jsonb") {
			t.Fatalf("the insert does not read @%s as jsonb", legs)
		}
	}
	if want := len(cols) + 3; len(m) != want {
		t.Fatalf("insertArgs binds %d names, want %d (the columns and the three leg arrays)", len(m), want)
	}
	// The statement builds: every @name has its argument and every argument
	// is used.
	c, err := momo.RawQuery(momo.NewOffline(dialect.Postgres), insert, mustArgs(t, Record{})...).SQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Args) != 40 {
		t.Fatalf("the insert renders %d binds, want 40", len(c.Args))
	}
}

func mustArgs(t *testing.T, r Record) []any {
	t.Helper()
	args, err := insertArgs(r)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// TestQueriesGolden fails if the SQL the writer sends changed. After an
// intended change, run with MOMO_UPDATE_GOLDEN=1 and review the diff of
// testdata/queries/postgres.sql.
func TestQueriesGolden(t *testing.T) {
	momotest.QueryGolden(t, Queries, "testdata/queries")
}

// TestWriterSendsInsert fails if the writer does not run the insert, with
// the record's correlation id first and 40 binds, through its momo handle.
func TestWriterSendsInsert(t *testing.T) {
	db := &flakyDB{}
	w := NewWriter(db.handle(), newCounter(), discard)
	now := time.Now()
	if err := w.write(context.Background(), Record{CorrelationID: "x", StartTime: now, EndTime: now}); err != nil {
		t.Fatal(err)
	}
	if len(db.sqls) != 1 || !strings.Contains(db.sqls[0], "INSERT INTO cdrs") || len(db.args) != 40 || db.args[0] != "x" {
		t.Fatalf("sent %d statements, %d args (first %v)", len(db.sqls), len(db.args), db.args[0])
	}
}

// TestAnsweredByCDR fails if the assistant's extension is not bound to
// answered_by (migration 00065).
func TestAnsweredByCDR(t *testing.T) {
	now := time.Now()
	if got := bound(t, Record{CorrelationID: "b", StartTime: now, EndTime: now, AnsweredBy: "300"})["answered_by"]; got != "300" {
		t.Fatalf("answered_by = %#v, want the assistant's extension", got)
	}
}

// TestCallQualityCDR fails if the three quality columns carry anything but
// the record's values, or are non-NULL for a record without quality
// (directly-media or unanswered call), or the SRTP, key exchange, ICE, kind
// and ring tag columns lose their defaults.
func TestCallQualityCDR(t *testing.T) {
	now := time.Now()
	base := Record{CorrelationID: "q", StartTime: now, EndTime: now}
	m := bound(t, base)
	if m["srtp"] != SRTPNone || m["srtp_keyex"] != KeyExNone || m["ice"] != false {
		t.Fatalf("srtp, key exchange, ice = %v, %v, %v, want the defaults %q, %q, false", m["srtp"], m["srtp_keyex"], m["ice"], SRTPNone, KeyExNone)
	}
	if m["rtp_packets"] != (*int64)(nil) || m["rtp_lost"] != (*int64)(nil) || m["rtp_jitter_ms"] != (*float64)(nil) {
		t.Fatalf("quality = %#v %#v %#v, want nil pointers", m["rtp_packets"], m["rtp_lost"], m["rtp_jitter_ms"])
	}
	if m["kind"] != "call" {
		t.Fatalf("kind = %v, want the default call", m["kind"])
	}
	if m["ring_tag"] != "" || m["ring_rule"] != "" {
		t.Fatalf("ring tag = %v (%v), want empty for a call that rang no device", m["ring_tag"], m["ring_rule"])
	}
	p, l, j := int64(1000), int64(12), 37.5
	base.RTPPackets, base.RTPLost, base.RTPJitterMs = &p, &l, &j
	base.RingTag, base.RingRule = "vip", "rule:1 boss"
	m = bound(t, base)
	if m["ring_tag"] != "vip" || m["ring_rule"] != "rule:1 boss" {
		t.Fatalf("ring tag = %v (%v), want vip (rule:1 boss)", m["ring_tag"], m["ring_rule"])
	}
	gp, gl, gj := m["rtp_packets"].(*int64), m["rtp_lost"].(*int64), m["rtp_jitter_ms"].(*float64)
	if *gp != 1000 || *gl != 12 || *gj != 37.5 {
		t.Fatalf("quality = %d, %d, %v", *gp, *gl, *gj)
	}
}

// TestFollowMeLegsCDR fails if the follow-me legs are not bound as a JSON
// array (empty for a call with none), or if the statement does not write
// them to cdr_legs with the role follow_me.
func TestFollowMeLegsCDR(t *testing.T) {
	now := time.Now()
	r := Record{CorrelationID: "fm", StartTime: now, EndTime: now}
	if got := bound(t, r)["follow_me_legs"]; got != "[]" {
		t.Fatalf("follow-me legs = %v, want [] for a call with none", got)
	}
	r.FollowMeLegs = []FollowMeLeg{
		{Kind: "own", Extension: "101", Result: "no_answer"},
		{Kind: "external", Trunk: "carrier", Result: "answered"},
	}
	arg := bound(t, r)["follow_me_legs"]
	var got []FollowMeLeg
	if err := json.Unmarshal([]byte(arg.(string)), &got); err != nil || len(got) != 2 || got[1] != r.FollowMeLegs[1] {
		t.Fatalf("follow-me legs argument = %v (%v), want %+v", arg, err, r.FollowMeLegs)
	}
	if !strings.Contains(insert, "INTO cdr_legs") || !strings.Contains(insert, "'follow_me'") {
		t.Fatal("the insert does not write cdr_legs with the role follow_me")
	}
}

// TestPeerCallIDStored fails if the callee leg's Call-ID is not bound to
// peer_call_id: an RTCP-XR report from the callee's phone finds the CDR by
// it.
func TestPeerCallIDStored(t *testing.T) {
	now := time.Now()
	if got := bound(t, Record{CorrelationID: "p", StartTime: now, EndTime: now, PeerCallID: "leg-b@hello"})["peer_call_id"]; got != "leg-b@hello" {
		t.Fatalf("peer call id argument = %#v", got)
	}
}

// TestCDROrigin fails if a click-to-call's origin and client are not bound,
// or a record without an origin is not stored as a call dialled at a phone.
func TestCDROrigin(t *testing.T) {
	now := time.Now()
	r := Record{CorrelationID: "o", StartTime: now, EndTime: now}
	if m := bound(t, r); m["origin"] != OriginPhone || m["origin_client"] != "" {
		t.Fatalf("default origin = %v %v, want phone and no client", m["origin"], m["origin_client"])
	}
	r.Origin, r.OriginClient = OriginAPI, "token:7"
	if m := bound(t, r); m["origin"] != OriginAPI || m["origin_client"] != "token:7" {
		t.Fatalf("origin = %v %v, want api and token:7", m["origin"], m["origin_client"])
	}
}

// TestCDRCodecColumns fails if the codec columns are not NULL for a leg
// without a codec, or carry anything but the record's strings.
func TestCDRCodecColumns(t *testing.T) {
	now := time.Now()
	r := Record{CorrelationID: "c", StartTime: now, EndTime: now}
	if m := bound(t, r); m["caller_codec"] != nil || m["callee_codec"] != nil {
		t.Fatalf("codecs = %#v, %#v, want nil, nil", m["caller_codec"], m["callee_codec"])
	}
	r.CallerCodec, r.CalleeCodec = "opus/48000", "PCMU/8000"
	if m := bound(t, r); m["caller_codec"] != "opus/48000" || m["callee_codec"] != "PCMU/8000" {
		t.Fatalf("codecs = %#v, %#v", m["caller_codec"], m["callee_codec"])
	}
}
