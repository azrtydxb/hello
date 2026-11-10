package webhook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/secret"
)

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// request is what the receiver saw.
type request struct {
	Header http.Header
	Body   []byte
}

// receiver is a scripted subscriber.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []request
	script func(n int, r *http.Request) (status int, header map[string]string, body string)
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, request{Header: req.Header.Clone(), Body: b})
		n := len(r.got)
		script := r.script
		r.mu.Unlock()
		status, hdr, body := http.StatusOK, map[string]string(nil), ""
		if script != nil {
			status, hdr, body = script(n, req)
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) requests() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]request(nil), r.got...)
}

func (r *receiver) respond(f func(n int, r *http.Request) (int, map[string]string, string)) {
	r.mu.Lock()
	r.script = f
	r.mu.Unlock()
}

func status(code int) func(int, *http.Request) (int, map[string]string, string) {
	return func(int, *http.Request) (int, map[string]string, string) { return code, nil, "" }
}

// env is one store under test and how to put events in it.
type env struct {
	store Store
	// emit stores the event and queues its deliveries, as the ingester does.
	emit func(t *testing.T, e events.Event)
	// drop deletes a stored event (the retention purge did).
	drop func(t *testing.T, id string)
	// audit counts audit rows of an action.
	audit func(t *testing.T, action string) int
}

type maker struct {
	name string
	make func(t *testing.T) env
}

func memEnv(t *testing.T) env {
	m := NewMem()
	var n int64
	return env{
		store: m,
		emit: func(t *testing.T, e events.Event) {
			n++
			if err := m.Enqueue(context.Background(), nil, events.Entry{Cursor: strconv.FormatInt(n, 10) + "-0", Event: e}); err != nil {
				t.Fatal(err)
			}
		},
		drop: func(t *testing.T, id string) {
			m.mu.Lock()
			delete(m.events, id)
			m.mu.Unlock()
		},
		audit: func(t *testing.T, action string) int {
			m.mu.Lock()
			defer m.mu.Unlock()
			c := 0
			for _, a := range m.Audit {
				if strings.Contains(a, " "+action+" ") {
					c++
				}
			}
			return c
		},
	}
}

// pgEnv is a PostgreSQL scratch database, skipping without
// HELLO_TEST_DATABASE_URL.
func pgEnv(t *testing.T) env {
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
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_webhooks_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*scfg)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	var n int64
	return env{
		store: &PG{DB: db},
		emit: func(t *testing.T, e events.Event) {
			n++
			b, _ := json.Marshal(e)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO integration_events (id, cursor_ms, cursor_seq, type, created_at, correlation_id, extension_number, payload)
				VALUES ($1, $2, 0, $3, $4, $5, $6, $7::jsonb) ON CONFLICT (id) DO NOTHING`,
				e.ID, n, e.Type, e.CreatedAt, e.Data.CallID, e.Data.Extension, string(b)); err != nil {
				t.Fatal(err)
			}
			if err := (&PG{DB: db}).Enqueue(ctx, tx, events.Entry{Cursor: strconv.FormatInt(n, 10) + "-0", Event: e}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		},
		drop: func(t *testing.T, id string) {
			if _, err := db.ExecContext(ctx, `DELETE FROM integration_events WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		},
		audit: func(t *testing.T, action string) int {
			var c int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE resource LIKE 'webhook_%' AND action = $1`, action).Scan(&c); err != nil {
				t.Fatal(err)
			}
			return c
		},
	}
}

var makers = []maker{{"mem", memEnv}, {"pg", pgEnv}}

// forEachStore runs f against the in-memory store and, when a database is
// configured, PostgreSQL.
func forEachStore(t *testing.T, f func(t *testing.T, r *rig)) {
	for _, m := range makers {
		t.Run(m.name, func(t *testing.T) { f(t, newRig(t, m)) })
	}
}

// rig is a service and dispatcher over a store with a fake clock and a
// scripted receiver.
type rig struct {
	t      *testing.T
	env    env
	svc    *Service
	disp   *Dispatcher
	clk    *clock
	recv   *receiver
	notify []string
	nmu    sync.Mutex
}

func newRig(t *testing.T, m maker) *rig {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	e := m.make(t)
	r := &rig{t: t, env: e, recv: newReceiver(t), clk: &clock{t: time.Now().Add(time.Hour).UTC()}}
	plain := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r.svc = &Service{Store: e.store, Box: box, Now: r.clk.Now, Rand: func() float64 { return 0.5 },
		PublicClient: plain, PrivateClient: plain, skipURLCheck: true}
	r.disp = &Dispatcher{Svc: r.svc, Metrics: NewMetrics(prometheus.NewRegistry()),
		Notify: func(_ context.Context, ep Endpoint, reason string) {
			r.nmu.Lock()
			r.notify = append(r.notify, fmt.Sprintf("%d:%s", ep.ID, reason))
			r.nmu.Unlock()
		}}
	return r
}

func (r *rig) notified() []string {
	r.nmu.Lock()
	defer r.nmu.Unlock()
	return append([]string(nil), r.notify...)
}

// endpoint creates an endpoint at the receiver; it returns it and its secret.
func (r *rig) endpoint(in EndpointInput) (Endpoint, string) {
	r.t.Helper()
	if in.URL == "" {
		in.URL = r.recv.srv.URL + "/hook"
	}
	if in.Name == "" {
		in.Name = "receiver"
	}
	in.Enabled = true
	ep, sec, created, err := r.svc.Create(context.Background(), "user:test", in)
	if err != nil || !created || sec == "" {
		r.t.Fatalf("Create = %+v, %q, %v, %v", ep, sec, created, err)
	}
	return ep, sec
}

var eventSeq int

// event is a call event of the given type about ext, with a fresh id.
func event(typ, ext string) events.Event {
	eventSeq++
	return events.New(typ, events.Data{CallID: "call-" + strconv.Itoa(eventSeq), Extension: ext, Sequence: 1,
		From: events.Party{Number: "100"}, To: events.Party{Number: ext}}, "", time.Now())
}

// run delivers what is due and returns how many attempts it made.
func (r *rig) run() int {
	r.t.Helper()
	n, err := r.disp.RunOnce(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) deliveries(endpoint int64, status string) []Delivery {
	r.t.Helper()
	ds, err := r.svc.ListDeliveries(context.Background(), DeliveryQuery{EndpointID: endpoint, Status: status, Limit: 500})
	if err != nil {
		r.t.Fatal(err)
	}
	return ds
}

func (r *rig) delivery(endpoint int64) Delivery {
	r.t.Helper()
	ds := r.deliveries(endpoint, "")
	if len(ds) != 1 {
		r.t.Fatalf("%d deliveries, want 1: %+v", len(ds), ds)
	}
	return ds[0]
}
