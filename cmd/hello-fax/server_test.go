package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
)

// fakeEngine stands in for spandsp: it "receives" two pages in whatever mode
// it is run, and blocks until its context ends, so a test can stop, restart
// or let it finish.
type fakeEngine struct {
	mu    sync.Mutex
	modes []Mode
	pages []fax.Page
	hold  bool
}

func (f *fakeEngine) Run(ctx context.Context, j Job) (Outcome, error) {
	f.mu.Lock()
	f.modes = append(f.modes, j.Mode)
	hold := f.hold
	f.mu.Unlock()
	if hold {
		<-ctx.Done()
		return Outcome{Code: -1, Text: "interrupted"}, nil
	}
	return Outcome{Success: true, Text: "OK", Received: f.pages, PagesDone: len(f.pages), BitRate: 14400, ECM: true}, nil
}

func (f *fakeEngine) seen() []Mode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Mode(nil), f.modes...)
}

func testServer(t *testing.T, eng Engine, token string) (*server, *httptest.Server) {
	t.Helper()
	s := newServer(eng, fax.Rasteriser{}, token, 41000, 41999, slog.New(slog.DiscardHandler))
	s.ready = true
	ts := httptest.NewServer(s.handler())
	t.Cleanup(func() { s.Shutdown(); ts.Close() })
	return s, ts
}

func client(ts *httptest.Server, token string) *fax.Client {
	return &fax.Client{Addr: strings.TrimPrefix(ts.URL, "http://"), Token: token}
}

func blank(n int) []fax.Page {
	out := make([]fax.Page, n)
	for i := range out {
		out[i] = fax.NewPage(fax.FaxPageWidth, 64, fax.FineXDPI, fax.FineYDPI)
		out[i].Set(i, i)
	}
	return out
}

func waitDone(t *testing.T, c *fax.Client, id string) fax.SessionInfo {
	t.Helper()
	for i := 0; i < 200; i++ {
		info, err := c.Status(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if info.Done() {
			return info
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session did not finish")
	return fax.SessionInfo{}
}

func TestReceiveSessionProducesPDF(t *testing.T) {
	_, ts := testServer(t, &fakeEngine{pages: blank(2)}, "")
	c := client(ts, "")
	info, err := c.Start(context.Background(), fax.SessionRequest{Direction: fax.SessionReceive, Mode: "g711", Ident: "+3225551234", Remote: "127.0.0.1:20000"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Port < 41000 || info.Port > 41999 || info.ID == "" {
		t.Fatalf("session: %+v", info)
	}
	done := waitDone(t, c, info.ID)
	if !done.Success || done.Pages != 2 || !done.HasPDF || done.BitRate != 14400 || !done.ECM {
		t.Fatalf("result: %+v", done)
	}
	pdf, err := c.PDF(context.Background(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := fax.DecodePDF(pdf)
	if err != nil || len(pages) != 2 {
		t.Fatalf("PDF: %v, %d pages", err, len(pages))
	}
	if err := c.Forget(context.Background(), info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(context.Background(), info.ID); err == nil {
		t.Fatal("a forgotten session still answers")
	}
}

func TestReceiveKeepsPagesWhenTheFaxFails(t *testing.T) {
	eng := &failingEngine{pages: blank(3)}
	_, ts := testServer(t, eng, "")
	c := client(ts, "")
	info, err := c.Start(context.Background(), fax.SessionRequest{Direction: fax.SessionReceive, Mode: "t38", Remote: "127.0.0.1:20000"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, c, info.ID)
	if done.Success || done.Pages != 3 || !done.HasPDF || done.Code == 0 {
		t.Fatalf("a failed fax must keep its pages: %+v", done)
	}
}

type failingEngine struct{ pages []fax.Page }

func (f *failingEngine) Run(context.Context, Job) (Outcome, error) {
	return Outcome{Code: 22, Text: "The call dropped prematurely", Received: f.pages, PagesDone: 3}, nil
}

func TestPageLimitTruncates(t *testing.T) {
	_, ts := testServer(t, &fakeEngine{pages: blank(fax.MaxReceivePages + 5)}, "")
	c := client(ts, "")
	info, _ := c.Start(context.Background(), fax.SessionRequest{Direction: fax.SessionReceive, Mode: "g711", Remote: "127.0.0.1:20000"}, nil)
	done := waitDone(t, c, info.ID)
	if !done.Truncated {
		t.Fatalf("more than %d pages must be reported truncated: %+v", fax.MaxReceivePages, done)
	}
	pdf, _ := c.PDF(context.Background(), info.ID)
	if pages, _ := fax.DecodePDF(pdf); len(pages) != fax.MaxReceivePages {
		t.Fatalf("PDF holds %d pages", len(pages))
	}
}

func TestSwitchToT38RestartsTheSession(t *testing.T) {
	eng := &fakeEngine{hold: true}
	_, ts := testServer(t, eng, "")
	c := client(ts, "")
	info, _ := c.Start(context.Background(), fax.SessionRequest{Direction: fax.SessionReceive, Mode: "g711", Remote: "127.0.0.1:20000"}, nil)
	time.Sleep(30 * time.Millisecond)
	got, err := c.SwitchT38(context.Background(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != info.Port {
		t.Fatalf("the port changed from %d to %d: the carrier was told the old one", info.Port, got.Port)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(eng.seen()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m := eng.seen(); len(m) != 2 || m[0] != ModeG711 || m[1] != ModeT38 {
		t.Fatalf("engine runs: %v", m)
	}
	st, _ := c.Status(context.Background(), info.ID)
	if st.Mode != "t38" || st.Done() {
		t.Fatalf("after the switch: %+v", st)
	}
	stopped, err := c.Stop(context.Background(), info.ID)
	if err != nil || !stopped.Done() || stopped.Success {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	// A finished session does not switch again.
	if again, _ := c.SwitchT38(context.Background(), info.ID); again.Mode != "t38" {
		t.Fatalf("%+v", again)
	}
}

func TestOutboundAsksForT38OnlyWhenAllowed(t *testing.T) {
	eng := &fakeEngine{hold: true}
	_, ts := testServer(t, eng, "")
	c := client(ts, "")
	pdf, _ := fax.EncodePDF(blank(1))
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	for _, allow := range []bool{false, true} {
		info, err := c.Start(context.Background(), fax.SessionRequest{Direction: fax.SessionSend, Mode: "g711", Remote: "127.0.0.1:20000", AllowT38: allow}, pdf)
		if err != nil {
			t.Fatal(err)
		}
		if info.WantT38 != allow {
			t.Fatalf("allow=%v: wantT38=%v", allow, info.WantT38)
		}
	}
}

func TestAuthAndValidation(t *testing.T) {
	_, ts := testServer(t, &fakeEngine{}, "s3cret")
	anon := client(ts, "")
	if err := anon.Healthy(context.Background()); err == nil {
		t.Fatal("no token was accepted")
	}
	c := client(ts, "s3cret")
	if err := c.Healthy(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range []fax.SessionRequest{
		{Direction: "x", Mode: "g711", Remote: "127.0.0.1:1"},
		{Direction: fax.SessionReceive, Mode: "bogus", Remote: "127.0.0.1:1"},
		{Direction: fax.SessionReceive, Mode: "g711", Remote: "nonsense"},
		{Direction: fax.SessionReceive, Mode: "g711", PT: 9, Remote: "127.0.0.1:1"},
	} {
		if _, err := c.Start(context.Background(), r, nil); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if _, err := c.Rasterise(context.Background(), []byte("not a pdf")); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("a non-PDF must be rejected with 422: %v", err)
	}
}

func TestUnavailableWithoutEngine(t *testing.T) {
	s := newServer(stubEngineForTest{}, fax.Rasteriser{}, "", 41000, 41999, slog.New(slog.DiscardHandler))
	s.ready = false
	ts := httptest.NewServer(s.handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	c := client(ts, "")
	if _, err := c.Start(context.Background(), fax.SessionRequest{Direction: fax.SessionReceive, Mode: "g711", Remote: "127.0.0.1:1"}, nil); err == nil {
		t.Fatal("a session started without an engine")
	}
}

type stubEngineForTest struct{}

func (stubEngineForTest) Run(context.Context, Job) (Outcome, error) { return Outcome{}, ErrNoEngine }

func TestRasteriseEndpoint(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	_, ts := testServer(t, &fakeEngine{}, "")
	c := client(ts, "")
	pdf, _ := fax.EncodePDF(blank(3))
	n, err := c.Rasterise(context.Background(), pdf)
	if err != nil || n != 3 {
		t.Fatalf("pages = %d, err = %v", n, err)
	}
	big := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), fax.MaxSendBytes)...)
	if _, err := c.Rasterise(context.Background(), big); err == nil {
		t.Fatal("an oversized PDF was accepted")
	}
	var _ = json.Marshal
}
