package redirect

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestYealinkDefaultsToRPS fails if YMCS credentials alone switch the
// client to YMCS: RPS is the default, YMCS only with settings api "ymcs"
// (spec S-11).
func TestYealinkDefaultsToRPS(t *testing.T) {
	both := rpsCreds()
	for k, v := range ymcsCreds() {
		both[k] = v
	}
	c, err := New(prov.Yealink, both, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*rps); !ok {
		t.Fatalf("no api setting with YMCS credentials built %T, want RPS", c)
	}
	if _, err := New(prov.Yealink, ymcsCreds(), nil, nil); err == nil || !strings.Contains(err.Error(), KeyYealinkKey) {
		t.Fatalf("YMCS credentials without api ymcs = %v, want the missing RPS key named", err)
	}
	c, err = New(prov.Yealink, both, []byte(`{"api":"ymcs"}`), nil)
	if _, ok := c.(*ymcs); err != nil || !ok {
		t.Fatalf("api ymcs built %T, %v", c, err)
	}
}

// TestYMCSMacOnlyExisting fails if a MAC-only add of a device YMCS already
// holds (reported in errors[] by code or by text) does not delete and add
// it again with the new URL.
func TestYMCSMacOnlyExisting(t *testing.T) {
	ctx := context.Background()
	cr := ymcsCreds()
	for _, existsErr := range []string{
		`{"field":"mac","errorCode":"800003","msg":"Resource already exists"}`,
		`{"field":"mac","errorInfo":"Device already exists"}`,
	} {
		f := &ymcsFake{id: cr[KeyYMCSClientID], secret: cr[KeyYMCSClientSecret], devices: map[string]map[string]string{}, existsErr: existsErr}
		hc, _ := fakeHTTP(t, f)
		c, err := New(prov.Yealink, cr, []byte(`{"api":"ymcs","macOnly":true}`), hc)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Register(ctx, macPlain, "", phoneURL); err != nil {
			t.Fatal(err)
		}
		rotated := phoneURL + "?r=2"
		if err := c.Register(ctx, macPlain, "", rotated); err != nil || f.devices[macPlain]["uniqueServerUrl"] != rotated {
			t.Fatalf("%s: re-register = %v, device %v", existsErr, err, f.devices[macPlain])
		}
	}
}

// TestTokenRefreshOnRejection fails if a call refused for its access token
// (revoked or expired early) is not retried once with a fresh token, for
// YMCS and GDMS.
func TestTokenRefreshOnRejection(t *testing.T) {
	ctx := context.Background()
	t.Run("ymcs", func(t *testing.T) {
		cr := ymcsCreds()
		f := &ymcsFake{id: cr[KeyYMCSClientID], secret: cr[KeyYMCSClientSecret], devices: map[string]map[string]string{}}
		hc, _ := fakeHTTP(t, f)
		c, _ := New(prov.Yealink, cr, []byte(`{"api":"ymcs"}`), hc)
		if err := c.Register(ctx, macPlain, "SN1", phoneURL); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.tokens++ // the cached token is no longer valid
		f.mu.Unlock()
		if _, found, err := c.Lookup(ctx, macPlain); err != nil || !found {
			t.Fatalf("lookup after revocation = %v %v, want a retry with a new token", found, err)
		}
	})
	t.Run("gdms", func(t *testing.T) {
		f := newGDMSFake()
		hc, _ := fakeHTTP(t, f)
		c, _ := New(prov.Grandstream, gdmsCreds(), nil, hc)
		if err := c.Check(ctx); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		if err := c.Register(ctx, macPlain, "SN9", phoneURL); err != nil || len(f.devices) != 1 {
			t.Fatalf("register after revocation = %v, want a retry with a new token", err)
		}
	})
}

// TestGDMSExistsElsewhere fails if GDMS's "already exists" counts as
// registered when the device is not in Hello's site, or does not when it is.
func TestGDMSExistsElsewhere(t *testing.T) {
	ctx := context.Background()
	f := newGDMSFake()
	hc, _ := fakeHTTP(t, f)
	c, _ := New(prov.Grandstream, gdmsCreds(), nil, hc)
	f.devices["00:04:13:AA:BB:CC"] = map[string]any{"mac": "00:04:13:AA:BB:CC", "siteId": float64(9999)}
	err := c.Register(ctx, macPlain, "SN9", phoneURL)
	if err == nil || !strings.Contains(err.Error(), "already exists") || !errors.Is(err, ErrRejected) {
		t.Fatalf("device in another site = %v, want a rejection with the GDMS message", err)
	}
	f.devices["00:04:13:AA:BB:CC"]["siteId"] = float64(3345)
	if err := c.Register(ctx, macPlain, "SN9", phoneURL); err != nil {
		t.Fatalf("device already in Hello's site = %v, want registered", err)
	}
}

// TestWorkerRejectedFailsAtOnce fails if a vendor rejection is retried
// instead of recorded as failed with the vendor's message.
func TestWorkerRejectedFailsAtOnce(t *testing.T) {
	ctx := context.Background()
	f := newGDMSFake()
	f.devices["00:04:13:AA:BB:CC"] = map[string]any{"mac": "00:04:13:AA:BB:CC", "siteId": float64(9999)}
	hc, _ := fakeHTTP(t, f)
	st := newFakeStore()
	st.accounts[prov.Grandstream] = Account{Vendor: prov.Grandstream, Enabled: true, Credentials: gdmsCreds()}
	st.targets["grandstream/"+macPlain] = Target{MAC: macPlain, Serial: "SN9", URL: phoneURL}
	st.jobs = []Job{{Seq: 1, Vendor: prov.Grandstream, MAC: macPlain, Op: OpRegister, FirstQueuedAt: time.Now()}}
	w := NewWorker(st, nil, slog.New(slog.DiscardHandler))
	w.hc = hc
	w.work(ctx)
	if len(st.retried) != 0 || len(st.finished) != 1 || st.finished[0].Status.State != StateFailed ||
		!strings.Contains(st.finished[0].Status.Reason, "already exists") {
		t.Fatalf("finished %+v, retried %+v; want failed with the GDMS message", st.finished, st.retried)
	}
}

// TestWorkerLeavesStatusAlone fails if the worker hands the store a status
// for a job whose phone status must not change (an unregister, a stale
// register): it must ask for no status write, not pass a zero Status.
func TestWorkerLeavesStatusAlone(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	failing, _ := fakeHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	f := newSnomFake(t)
	ok, _ := fakeHTTP(t, f)
	mk := func(st Store, hc *http.Client) *Worker {
		w := NewWorker(st, nil, slog.New(slog.DiscardHandler))
		w.hc = hc
		return w
	}
	st := newFakeStore()
	st.accounts[prov.Snom] = Account{Vendor: prov.Snom, Enabled: true, Credentials: snomCreds()}
	st.jobs = []Job{
		{Seq: 1, Vendor: prov.Snom, MAC: macPlain, Op: OpUnregister, FirstQueuedAt: now},
		{Seq: 2, Vendor: prov.Snom, MAC: macPlain, Op: OpRegister, FirstQueuedAt: now}, // no such phone
		{Seq: 3, Vendor: prov.Poly, MAC: macPlain, Op: OpUnregister, FirstQueuedAt: now},
	}
	mk(st, ok).work(ctx)
	st.jobs = []Job{{Seq: 4, Vendor: prov.Snom, MAC: macPlain, Op: OpUnregister, FirstQueuedAt: now}}
	mk(st, failing).work(ctx)
	if len(st.finished) != 3 || len(st.retried) != 1 {
		t.Fatalf("finished %+v, retried %+v", st.finished, st.retried)
	}
	for _, f := range st.finished {
		if f.Set {
			t.Fatalf("job %d wrote status %+v, want none", f.Job.Seq, f.Status)
		}
	}
	if st.retried[0].Set {
		t.Fatalf("unregister retry wrote status %+v, want none", st.retried[0].Status)
	}
}

// TestDriftOnStartWhenOverdue fails if Run does not run the drift check
// at start when the last one (kept by the store) is more than a day old,
// runs it when it is not due, or does not record when it ran.
func TestDriftOnStartWhenOverdue(t *testing.T) {
	f := newSnomFake(t)
	hc, _ := fakeHTTP(t, f)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	mk := func(last time.Time) *fakeStore {
		st := newFakeStore()
		st.accounts[prov.Snom] = Account{Vendor: prov.Snom, Enabled: true, Credentials: snomCreds()}
		st.registered[prov.Snom] = []Target{{MAC: "000413000009", URL: phoneURL}} // not with the vendor: drift
		st.lastDrift = last
		return st
	}
	run := func(st *fakeStore) {
		ctx, cancel := context.WithCancel(context.Background())
		w := NewWorker(st, nil, slog.New(slog.DiscardHandler))
		w.hc, w.now = hc, func() time.Time { return now }
		done := make(chan struct{})
		go func() { w.Run(ctx); close(done) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			st.mu.Lock()
			n := len(st.driftSets)
			st.mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
	}
	overdue := mk(now.Add(-25 * time.Hour))
	run(overdue)
	if s := overdue.statuses["000413000009"]; s.Reason != "drift" || len(overdue.driftSets) != 1 || !overdue.driftSets[0].Equal(now) {
		t.Fatalf("overdue: status %+v, recorded %v; want the check run at start and recorded", s, overdue.driftSets)
	}
	recent := mk(now.Add(-time.Hour))
	run(recent)
	if _, ran := recent.statuses["000413000009"]; ran || len(recent.driftSets) != 0 {
		t.Fatal("the drift check ran although the last one was an hour ago")
	}
}

// TestReconcileLogsSkippedVendors fails if the drift check skips a vendor
// whose account cannot be read or whose client cannot be built without a
// log line naming the vendor and the reason.
func TestReconcileLogsSkippedVendors(t *testing.T) {
	var logs bytes.Buffer
	st := newFakeStore()
	st.accountErr = map[prov.Vendor]error{prov.Snom: errors.New("db is down")}
	st.accounts[prov.Yealink] = Account{Vendor: prov.Yealink, Enabled: true, Credentials: rpsCreds(), Settings: []byte(`{"api":"soap"}`)}
	w := NewWorker(st, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	w.reconcile(context.Background())
	out := logs.String()
	for _, want := range []string{`"vendor":"snom"`, "db is down", `"vendor":"yealink"`, "api must be"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

// TestHostileMACs fails if a MAC that is not 12 hex digits after removing
// separators reaches any vendor (it could alter the request path or body).
func TestHostileMACs(t *testing.T) {
	ctx := context.Background()
	hostile := []string{
		"", "000413aabbc", "000413aabbccd", "zz0413aabbcc", "000413aabbcc/../x", "000413aabbcc?x=1",
		"00:04:13:aa:bb:cc\n", "00 04 13 aa bb cc", "000413aabb%63", "000413aabbcc</string>",
	}
	cr := ymcsCreds()
	type mk func(hc *http.Client) (Client, error)
	for name, build := range map[string]mk{
		"snom rest": func(hc *http.Client) (Client, error) { return New(prov.Snom, snomCreds(), nil, hc) },
		"snom xmlrpc": func(hc *http.Client) (Client, error) {
			return New(prov.Snom, snomCreds(), []byte(`{"api":"xmlrpc"}`), hc)
		},
		"yealink rps": func(hc *http.Client) (Client, error) { return New(prov.Yealink, rpsCreds(), nil, hc) },
		"yealink ymcs": func(hc *http.Client) (Client, error) {
			return New(prov.Yealink, cr, []byte(`{"api":"ymcs","macOnly":true}`), hc)
		},
		"grandstream": func(hc *http.Client) (Client, error) { return New(prov.Grandstream, gdmsCreds(), nil, hc) },
	} {
		hc, rw := fakeHTTP(t, http.NotFoundHandler())
		c, err := build(hc)
		if err != nil {
			t.Fatal(name, err)
		}
		for _, m := range hostile {
			if err := c.Register(ctx, m, "SN1", phoneURL); err == nil {
				t.Errorf("%s: register %q accepted", name, m)
			}
			if err := c.Unregister(ctx, m); err == nil {
				t.Errorf("%s: unregister %q accepted", name, m)
			}
			if _, _, err := c.Lookup(ctx, m); err == nil || errors.Is(err, ErrUnsupported) && name != "grandstream" {
				t.Errorf("%s: lookup %q = %v, want an invalid-MAC error", name, m, err)
			}
		}
		if n := rw.n.Load(); n != 0 {
			t.Errorf("%s: %d requests made for invalid MACs", name, n)
		}
	}
	if m, err := normMAC("00-04-13-AA-BB-CC"); err != nil || m != macPlain {
		t.Fatalf("normMAC = %q %v", m, err)
	}
}
