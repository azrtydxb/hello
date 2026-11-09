package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func ev(typ, ext string) Event {
	return New(typ, Data{CallID: "call-1", Extension: ext, From: Party{Number: "100"}, To: Party{Number: "200"}}, "", time.Now())
}

// TestEventID fails if the id of an event is not stable for one call, type,
// subject and discriminator (a retried CDR write must not make a second
// event) or if any of them does not change it.
func TestEventID(t *testing.T) {
	base := ID("c", CallEnded, "100", "")
	if base != ID("c", CallEnded, "100", "") {
		t.Fatal("id is not deterministic")
	}
	for name, other := range map[string]string{
		"call": ID("d", CallEnded, "100", ""), "type": ID("c", CallMissed, "100", ""),
		"extension": ID("c", CallEnded, "101", ""), "extra": ID("c", CallEnded, "100", "7"),
		"boundary": ID("c"+CallEnded, "", "100", ""),
	} {
		if other == base {
			t.Errorf("a different %s gives the same id", name)
		}
	}
	e := ev(CallRinging, "200")
	if e.APIVersion != APIVersion || e.ID == "" || e.CreatedAt.Location() != time.UTC {
		t.Fatalf("envelope = %+v", e)
	}
}

// TestCursor fails if stream ids do not parse, print and order numerically
// (a text comparison would put 9-0 after 10-0).
func TestCursor(t *testing.T) {
	a, err := ParseCursor("9-0")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ParseCursor("10-0")
	c, _ := ParseCursor("10-3")
	if a.Compare(b) >= 0 || b.Compare(c) >= 0 || c.Compare(c) != 0 || c.Compare(a) <= 0 {
		t.Fatal("cursors do not order numerically")
	}
	if c.String() != "10-3" {
		t.Fatalf("String = %q", c.String())
	}
	if z, err := ParseCursor(""); err != nil || !z.Zero() {
		t.Fatalf("empty cursor = %v, %v", z, err)
	}
	for _, bad := range []string{"x", "1-x", "-1-2", "1-2-3", "1--2"} {
		if _, err := ParseCursor(bad); err == nil {
			t.Errorf("ParseCursor(%q) accepted", bad)
		}
	}
}

// TestParseTypes fails if a type list accepts an unknown type or a malformed
// pattern, or rejects a registered type or a prefix pattern.
func TestParseTypes(t *testing.T) {
	got, err := ParseTypes("call.ringing, call.*")
	if err != nil || !slices.Equal(got, []string{"call.ringing", "call.*"}) {
		t.Fatalf("ParseTypes = %v, %v", got, err)
	}
	if got, err := ParseTypes(""); err != nil || got != nil {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	for _, bad := range []string{"call.nope", ".*", "*", "ca%.*", "call_.*", "call ringing"} {
		if _, err := ParseTypes(bad); err == nil {
			t.Errorf("ParseTypes(%q) accepted", bad)
		}
	}
}

// TestFilter fails if a filter lets through an event of another type or
// extension, or a restricted reader sees an event about an extension it does
// not own (or one with no extension at all).
func TestFilter(t *testing.T) {
	ringing, ended, none := ev(CallRinging, "200"), ev(CallEnded, "201"), ev(CallCompleted, "")
	for _, tc := range []struct {
		name string
		f    Filter
		want [3]bool // ringing 200, ended 201, completed with no extension
	}{
		{"open", Filter{}, [3]bool{true, true, true}},
		{"exact type", Filter{Types: []string{CallRinging}}, [3]bool{true, false, false}},
		{"prefix", Filter{Types: []string{"call.*"}}, [3]bool{true, true, true}},
		{"other prefix", Filter{Types: []string{"webhook.*"}}, [3]bool{}},
		{"extension", Filter{Extension: "201"}, [3]bool{false, true, false}},
		{"allowed", Filter{Allowed: []string{"200"}}, [3]bool{true, false, false}},
		{"allowed nothing", Filter{Allowed: []string{}}, [3]bool{}},
		{"allowed and type", Filter{Allowed: []string{"200", "201"}, Types: []string{CallEnded}}, [3]bool{false, true, false}},
	} {
		got := [3]bool{tc.f.Matches(ringing), tc.f.Matches(ended), tc.f.Matches(none)}
		if got != tc.want {
			t.Errorf("%s: matches %v, want %v", tc.name, got, tc.want)
		}
	}
}

type fakeAppender struct {
	mu   sync.Mutex
	got  []string
	fail bool
	gate chan struct{}
}

func (f *fakeAppender) Append(ctx context.Context, payload []byte) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("down")
	}
	f.got = append(f.got, string(payload))
	return nil
}

func (f *fakeAppender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// TestEmitterDropsWhenFull fails if Emit blocks on a full queue, or does not
// drop the new event and count it, or loses an event that fit.
func TestEmitterDropsWhenFull(t *testing.T) {
	m := NewEmitterMetrics(prometheus.NewRegistry())
	app := &fakeAppender{}
	e := newEmitter(app, m, discard, 2)
	done := make(chan struct{})
	go func() {
		for range 5 {
			e.Emit(ev(CallRinging, "200"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on a full queue")
	}
	if n := testutil.ToFloat64(m.Dropped); n != 3 {
		t.Fatalf("dropped = %v, want 3", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { e.Run(ctx); close(finished) }()
	deadline := time.Now().Add(2 * time.Second)
	for app.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-finished
	if app.count() != 2 {
		t.Fatalf("appended %d events, want the 2 that fit", app.count())
	}
	if n := testutil.ToFloat64(m.Emitted.WithLabelValues(CallRinging)); n != 2 {
		t.Fatalf("emitted = %v, want 2", n)
	}
}

// TestEmitterStreamDown fails if an unreachable stream blocks the emitter or
// an event it could not append is not counted as dropped.
func TestEmitterStreamDown(t *testing.T) {
	m := NewEmitterMetrics(prometheus.NewRegistry())
	e := newEmitter(&fakeAppender{fail: true}, m, discard, 10)
	e.Emit(ev(CallEnded, "100"))
	e.Emit(ev(CallEnded, "101"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Run flushes the queue on its way out
	e.Run(ctx)
	if n := testutil.ToFloat64(m.Dropped); n != 2 {
		t.Fatalf("dropped = %v, want 2", n)
	}
}

// TestEmitterFlushOnShutdown fails if events queued when the context ends are
// not appended before Run returns.
func TestEmitterFlushOnShutdown(t *testing.T) {
	app := &fakeAppender{}
	e := newEmitter(app, nil, discard, 10)
	for range 4 {
		e.Emit(ev(CallEnded, "100"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.Run(ctx)
	if app.count() != 4 {
		t.Fatalf("flushed %d events, want 4", app.count())
	}
}

func entry(cursor, typ, ext string) Entry {
	return Entry{Cursor: cursor, Event: ev(typ, ext)}
}

// TestHubFanOut fails if the hub delivers an event to a subscriber whose
// filter refuses it, reorders events, or lets a lagging subscriber block the
// others instead of dropping it.
func TestHubFanOut(t *testing.T) {
	gauge := 0
	h := newHub(func(n int) { gauge = n })
	all := h.subscribe(Filter{})
	own := h.subscribe(Filter{Allowed: []string{"200"}})
	slow := h.subscribe(Filter{})
	if gauge != 3 {
		t.Fatalf("gauge = %d, want 3", gauge)
	}
	h.publish(entry("1-0", CallRinging, "200"))
	h.publish(entry("2-0", CallRinging, "300"))
	if e := <-own.C; e.Cursor != "1-0" {
		t.Fatalf("restricted subscriber got %s first", e.Cursor)
	}
	select {
	case e := <-own.C:
		t.Fatalf("restricted subscriber got %s for another extension", e.Cursor)
	default:
	}
	for _, want := range []string{"1-0", "2-0"} {
		if e := <-all.C; e.Cursor != want {
			t.Fatalf("open subscriber got %s, want %s", e.Cursor, want)
		}
	}
	// slow never reads: it overflows its buffer and is dropped; the others keep going.
	for i := range subBuffer + 10 {
		h.publish(entry("3-"+strconv.Itoa(i), CallEnded, "200"))
		<-all.C
		<-own.C
	}
	n := 0
	for range slow.C {
		n++
	}
	if !slow.Lost() || n != subBuffer {
		t.Fatalf("slow subscriber: lost=%v after %d events, want lost after %d", slow.Lost(), n, subBuffer)
	}
	own.SetAllowed([]string{"300"})
	h.publish(entry("4-0", CallRinging, "200"))
	h.publish(entry("4-1", CallRinging, "300"))
	if e := <-own.C; e.Cursor != "4-1" {
		t.Fatalf("after SetAllowed got %s", e.Cursor)
	}
	all.Close()
	own.Close()
	if gauge != 0 {
		t.Fatalf("gauge after close = %d", gauge)
	}
	all.Close() // closing twice is harmless
	h.publish(entry("5-0", CallRinging, "200"))
}

// TestHubLimits fails if a credential may hold more than five streams, the
// pod more than 200, or a released slot is not reusable (twice-release
// counts once).
func TestHubLimits(t *testing.T) {
	h := newHub(nil)
	var rel []func()
	for range MaxStreamsPerCredential {
		r, err := h.acquire("tok")
		if err != nil {
			t.Fatal(err)
		}
		rel = append(rel, r)
	}
	if _, err := h.acquire("tok"); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("sixth stream: %v", err)
	}
	if r, err := h.acquire("other"); err != nil {
		t.Fatalf("another credential refused: %v", err)
	} else {
		r()
	}
	rel[0]()
	rel[0]()
	if r, err := h.acquire("tok"); err != nil {
		t.Fatalf("a released slot is not reusable: %v", err)
	} else {
		r()
	}
	for _, r := range rel[1:] {
		r()
	}
	var more []func()
	for i := range MaxStreamsPerPod {
		r, err := h.acquire("c" + strconv.Itoa(i)) // one each: only the pod limit applies
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		more = append(more, r)
	}
	if _, err := h.acquire("one-more"); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("stream over the pod limit: %v", err)
	}
	for _, r := range more {
		r()
	}
}

// TestPgArray fails if an array literal does not quote and escape its elements.
func TestPgArray(t *testing.T) {
	if got := pgArray([]string{"a", `b"c`, `d\e`, "call.%"}); got != `{"a","b\"c","d\\e","call.%"}` {
		t.Fatalf("pgArray = %s", got)
	}
	if got := pgArray(nil); got != "{}" {
		t.Fatalf("empty = %s", got)
	}
}

// TestDecode fails if a stream entry that is not an event of this version is
// accepted.
func TestDecode(t *testing.T) {
	good := `{"apiVersion":"1","id":"evt_1","type":"call.ended","createdAt":"2026-01-01T00:00:00Z","data":{"callId":"c","from":{},"to":{}}}`
	if e, ok := decode("5-1", map[string]string{"e": good}); !ok || e.Cursor != "5-1" || e.Type != CallEnded {
		t.Fatalf("decode = %+v, %v", e, ok)
	}
	for name, f := range map[string]map[string]string{
		"no field": {}, "not json": {"e": "{"}, "no id": {"e": `{"type":"call.ended"}`}, "no type": {"e": `{"id":"x"}`},
	} {
		if _, ok := decode("5-1", f); ok {
			t.Errorf("%s accepted", name)
		}
	}
	if _, ok := decode("bad", map[string]string{"e": good}); ok {
		t.Error("a bad stream id was accepted")
	}
}
