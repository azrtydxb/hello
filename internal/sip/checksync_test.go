package sip

import (
	"context"
	"crypto/md5" //nolint:gosec // RFC 3261 digest.
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// onNotify answers an NOTIFY like a handset: 200, or a digest challenge
// first when notifyPass is set, or a fixed status.
func (p *phone) onNotify(req *sip.Request, tx sip.ServerTransaction) {
	p.mu.Lock()
	pass, realm, status := p.notifyPass, p.notifyRealm, p.notifyStatus
	p.mu.Unlock()
	if status < 0 {
		return // never answers
	}
	if status > 0 {
		push(p.notifies, req)
		_ = tx.Respond(sip.NewResponseFromRequest(req, status, "Status", nil))
		return
	}
	if pass != "" {
		h := req.GetHeader("Authorization")
		if h == nil || !p.digestOK(h.Value(), req, pass) {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			for _, alg := range []string{AlgSHA256, AlgMD5} {
				c := digest.Challenge{Realm: realm, Nonce: "nonce-1", Algorithm: alg, QOP: []string{"auth"}}
				res.AppendHeader(sip.NewHeader("WWW-Authenticate", c.String()))
			}
			_ = tx.Respond(res)
			return
		}
	}
	push(p.notifies, req)
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

// digestOK checks an Authorization value against the device password.
func (p *phone) digestOK(v string, req *sip.Request, pass string) bool {
	c, err := digest.ParseCredentials(v)
	if err != nil || c.Username != p.user || c.Nonce != "nonce-1" || c.QOP != "auth" {
		return false
	}
	hex := func(alg, s string) string {
		if alg == AlgSHA256 {
			return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
		}
		return fmt.Sprintf("%x", md5.Sum([]byte(s))) //nolint:gosec // digest.
	}
	alg := strings.ToUpper(c.Algorithm)
	if alg != AlgSHA256 {
		// The test phone offered both; Hello must prefer SHA-256.
		p.t.Errorf("Hello answered with %q, want SHA-256", c.Algorithm)
	}
	ha1 := hex(alg, p.user+":"+c.Realm+":"+pass)
	ha2 := hex(alg, req.Method.String()+":"+c.URI)
	return c.Response == hex(alg, fmt.Sprintf("%s:%s:%08x:%s:%s:%s", ha1, c.Nonce, c.Nc, c.Cnonce, c.QOP, ha2))
}

// fakePushes is an in-memory PushQueue.
type fakePushes struct {
	mu      sync.Mutex
	pending []PushJob
	done    map[int64]PushResult
	rate    int
	wake    func()
}

func newFakePushes() *fakePushes { return &fakePushes{done: map[int64]PushResult{}} }

func (f *fakePushes) Watch(ctx context.Context, wake func()) {
	f.mu.Lock()
	f.wake = wake
	f.mu.Unlock()
	wake()
	<-ctx.Done()
}

func (f *fakePushes) Claim(_ context.Context, _ string, perMinute int) ([]PushJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rate = perMinute
	jobs := f.pending
	f.pending = nil
	return jobs, nil
}

func (f *fakePushes) Finish(_ context.Context, id int64, r PushResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[id] = r
	return nil
}

func (f *fakePushes) queue(j PushJob) {
	f.mu.Lock()
	f.pending = append(f.pending, j)
	w := f.wake
	f.mu.Unlock()
	if w != nil {
		w()
	}
}

func (f *fakePushes) result(t *testing.T, id int64) PushResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		r, ok := f.done[id]
		f.mu.Unlock()
		if ok {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("push %d never finished", id)
	return PushResult{}
}

func withPushes(f *fakePushes, rate int) pbxOpt {
	return func(c *Config, d *Deps) {
		d.Pushes = f
		c.PushRate = rate
	}
}

func checkSyncEventOf(t *testing.T, r *sip.Request) string {
	t.Helper()
	h := r.GetHeader("Event")
	if h == nil {
		t.Fatal("NOTIFY without Event")
	}
	return h.Value()
}

// TestCheckSyncNotify fails if the NOTIFY does not reach every registered
// contact of the device, if a digest challenge is not answered (SHA-256,
// realm-checked, from the stored HA1), if a plain resync asks for a reboot
// (or a reboot does not), if a vendor gets the wrong event form, if any
// event other than check-sync is ever produced (Event: reset), if a phone
// that is not registered, that answers 481, that never answers, or that
// challenges with an unknown realm or rejects the credentials is not
// reported as not_registered, timeout or auth_failed, or if the configured
// push rate is not passed to the queue.
func TestCheckSyncNotify(t *testing.T) {
	old := checkSyncTimeout
	checkSyncTimeout = 600 * time.Millisecond
	t.Cleanup(func() { checkSyncTimeout = old })
	oldPoll := pushPollInterval
	pushPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pushPollInterval = oldPoll })

	q := newFakePushes()
	pbx := startPBX(t, callerDevices(), withPushes(q, 7))
	b1, b2 := newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "b1", "pb1")
	b1.register(t)
	b2.register(t)
	var id int64
	push := func(vendor, kind string) PushResult {
		id++
		q.queue(PushJob{ID: id, Kind: kind, Vendor: vendor, Device: "b1"})
		return q.result(t, id)
	}
	drain := func(p *phone) {
		for {
			select {
			case <-p.notifies:
			default:
				return
			}
		}
	}

	// Both contacts get a plain resync; it carries no reboot parameter.
	drain(b1)
	drain(b2)
	r := push("grandstream", PushResync)
	if r.Status != PushOK || r.Contacts != 2 {
		t.Fatalf("resync result = %+v, want ok to 2 contacts", r)
	}
	for _, p := range []*phone{b1, b2} {
		req := waitReq(t, p.notifies, "check-sync NOTIFY")
		if ev := checkSyncEventOf(t, req); ev != "check-sync" {
			t.Fatalf("grandstream resync event = %q, want check-sync", ev)
		}
		if req.Recipient.User != "b1" && req.Recipient.Host == "" {
			t.Fatalf("bad request URI %v", req.Recipient)
		}
	}
	if got := q.rate; got != 7 {
		t.Fatalf("queue claimed with rate %d, want HELLO_PROV_PUSH_RATE 7", got)
	}

	// Vendor forms; a reboot is only ever asked for on request.
	forms := map[string][2]string{ // vendor: resync, reboot
		"yealink":     {"check-sync;reboot=false", "check-sync;reboot=true"},
		"snom":        {"check-sync;reboot=false", "check-sync;reboot=true"},
		"poly":        {"check-sync", "check-sync;reboot=true"},
		"grandstream": {"check-sync", "check-sync;reboot=true"},
		"fanvil":      {"check-sync", "check-sync;reboot=true"},
		"generic":     {"check-sync", "check-sync;reboot=true"},
	}
	for vendor, want := range forms {
		for i, kind := range []string{PushResync, PushReboot} {
			if got := checkSyncEvent(vendor, kind); got != want[i] {
				t.Errorf("checkSyncEvent(%s, %s) = %q, want %q", vendor, kind, got, want[i])
			}
			if strings.Contains(checkSyncEvent(vendor, kind), "reset") {
				t.Errorf("%s %s produced a reset event", vendor, kind)
			}
		}
		if strings.Contains(checkSyncEvent(vendor, PushResync), "reboot=true") {
			t.Errorf("%s resync asks for a reboot", vendor)
		}
	}
	drain(b1)
	drain(b2)
	if r := push("yealink", PushReboot); r.Status != PushOK {
		t.Fatalf("reboot result = %+v", r)
	}
	if ev := checkSyncEventOf(t, waitReq(t, b1.notifies, "reboot NOTIFY")); ev != "check-sync;reboot=true" {
		t.Fatalf("reboot event = %q", ev)
	}

	// A digest challenge is answered from the stored HA1.
	for _, p := range []*phone{b1, b2} {
		p.mu.Lock()
		p.notifyPass, p.notifyRealm = "pb1", testDomain
		p.mu.Unlock()
	}
	drain(b1)
	drain(b2)
	if r := push("grandstream", PushResync); r.Status != PushOK || r.Contacts != 2 {
		t.Fatalf("challenged resync = %+v, want ok", r)
	}
	waitReq(t, b1.notifies, "authorized NOTIFY")
	waitReq(t, b2.notifies, "authorized NOTIFY")

	// A challenge in a realm Hello does not know is not answered.
	for _, p := range []*phone{b1, b2} {
		p.mu.Lock()
		p.notifyRealm = "other.example"
		p.mu.Unlock()
	}
	if r := push("grandstream", PushResync); r.Status != PushAuthFailed {
		t.Fatalf("unknown realm result = %+v, want auth_failed", r)
	}
	// A phone that rejects the credentials is auth_failed too.
	for _, p := range []*phone{b1, b2} {
		p.mu.Lock()
		p.notifyPass, p.notifyRealm = "different", testDomain
		p.mu.Unlock()
	}
	if r := push("grandstream", PushResync); r.Status != PushAuthFailed {
		t.Fatalf("wrong password result = %+v, want auth_failed", r)
	}

	// 481 is not_registered, silence is a timeout, and the best contact
	// wins when only one of two answers.
	b1.mu.Lock()
	b1.notifyPass, b1.notifyStatus = "", 481
	b1.mu.Unlock()
	b2.mu.Lock()
	b2.notifyPass, b2.notifyStatus = "", -1
	b2.mu.Unlock()
	if r := push("yealink", PushResync); r.Status != PushTimeout {
		t.Fatalf("481 + silence result = %+v, want timeout", r)
	}
	b2.mu.Lock()
	b2.notifyStatus = 0
	b2.mu.Unlock()
	if r := push("yealink", PushResync); r.Status != PushOK {
		t.Fatalf("481 + 200 result = %+v, want ok (best of the contacts)", r)
	}
	b2.mu.Lock()
	b2.notifyStatus = 481
	b2.mu.Unlock()
	if r := push("yealink", PushResync); r.Status != PushNotRegistered {
		t.Fatalf("481 result = %+v, want not_registered", r)
	}

	// A device with no registration is not_registered without any send.
	id++
	q.queue(PushJob{ID: id, Kind: PushResync, Vendor: "yealink", Device: "a1"})
	if r := q.result(t, id); r.Status != PushNotRegistered || r.Contacts != 0 {
		t.Fatalf("unregistered result = %+v, want not_registered", r)
	}
}
