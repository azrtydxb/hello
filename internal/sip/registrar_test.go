package sip

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
	"github.com/valkey-io/valkey-go"
)

func contactExpires(t *testing.T, res *sip.Response) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, h := range res.GetHeaders("Contact") {
		c := h.(*sip.ContactHeader)
		v, _ := c.Params.Get("expires")
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("contact %s without expires", c.Address.String())
		}
		out[c.Address.String()] = n
	}
	return out
}

func bindings(t *testing.T, pbx *testPBX, aor string) map[string]livestate.Binding {
	t.Helper()
	bs, err := pbx.state.Bindings(context.Background(), aor)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]livestate.Binding{}
	for _, b := range bs {
		out[b.ContactURI] = b
	}
	return out
}

// valkeyState returns a real livestate store and throttle when
// HELLO_TEST_VALKEY_ADDR is set, flushing the database first. It uses its
// own logical database so other packages' tests flushing theirs in
// parallel do not interfere.
func valkeyState(t *testing.T) (State, Throttle, valkey.Client) {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 7})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return livestate.New(c), ValkeyThrottle{Client: c, Window: time.Minute}, c
}

func TestRegistrarFake(t *testing.T) { testRegistrar(t, nil) }

// TestRegistrarValkey runs the registrar against Valkey through livestate.
func TestRegistrarValkey(t *testing.T) {
	st, th, _ := valkeyState(t)
	testRegistrar(t, func(_ *Config, d *Deps) { d.State, d.Throttle = st, th })
}

// testRegistrar fails if two contacts of one AOR are not both stored and
// listed with their expiry, a refresh duplicates a binding, the source is
// not the packet's source, expiry is not clamped, or Expires: 0 / Contact: *
// leave bindings behind.
func testRegistrar(t *testing.T, opt pbxOpt) {
	opts := []pbxOpt{}
	if opt != nil {
		opts = append(opts, opt)
	}
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw")}, opts...)
	p := newPhone(t, pbx, "alice", "pw")
	aor := "sip:alice@" + testDomain
	c1, c2 := "sip:alice@127.0.0.1:7001", "sip:alice@127.0.0.1:7002"

	res := p.authDo(p.registerReq(-1, "<"+c1+">;expires=120", "<"+c2+">"), AlgSHA256, "pw")
	if res.StatusCode != 200 {
		t.Fatalf("register two contacts = %d", res.StatusCode)
	}
	exp := contactExpires(t, res)
	if len(exp) != 2 || exp[c1] < 115 || exp[c1] > 120 || exp[c2] < 3595 || exp[c2] > 3600 {
		t.Fatalf("200 contacts = %v; want c1≈120 and c2≈3600 (default)", exp)
	}
	bs := bindings(t, pbx, aor)
	if len(bs) != 2 {
		t.Fatalf("stored = %v, want 2", bs)
	}
	b := bs[c1]
	if b.ReceivedNode != "sip-test" || b.Source != p.addr || b.Device != "alice" || b.Extension != "100" || b.Transport != "udp" {
		t.Fatalf("binding = %+v", b)
	}

	// Refresh of c1, above the maximum: clamped, not duplicated.
	if res := p.authDo(p.registerReq(7200, "<"+c1+">"), AlgMD5, "pw"); res.StatusCode != 200 {
		t.Fatalf("refresh = %d", res.StatusCode)
	} else if e := contactExpires(t, res)[c1]; e < 3595 || e > 3600 {
		t.Fatalf("refresh expires = %d, want clamped to 3600", e)
	}
	if bs := bindings(t, pbx, aor); len(bs) != 2 || time.Until(bs[c1].Expires) > time.Hour {
		t.Fatalf("after refresh = %v", bs)
	}

	// Below the minimum: 423 with Min-Expires, nothing changed.
	res = p.authDo(p.registerReq(30, "<"+c1+">"), AlgSHA256, "pw")
	if res.StatusCode != 423 {
		t.Fatalf("expires 30 = %d, want 423", res.StatusCode)
	}
	if h := res.GetHeader("Min-Expires"); h == nil || h.Value() != "60" {
		t.Fatalf("Min-Expires = %v", h)
	}

	// Query: a REGISTER without Contact lists the bindings unchanged.
	if res := p.authDo(p.registerReq(-1, ""), AlgSHA256, "pw"); res.StatusCode != 200 || len(contactExpires(t, res)) != 2 {
		t.Fatalf("query = %d %v, want 200 listing 2", res.StatusCode, contactExpires(t, res))
	}

	// Expires: 0 removes one.
	if res := p.authDo(p.registerReq(0, "<"+c2+">"), AlgSHA256, "pw"); res.StatusCode != 200 || len(contactExpires(t, res)) != 1 {
		t.Fatalf("unregister = %d %v", res.StatusCode, contactExpires(t, res))
	}
	if bs := bindings(t, pbx, aor); len(bs) != 1 || bs[c1].ContactURI == "" {
		t.Fatalf("after Expires: 0 = %v, want only c1", bs)
	}

	// Contact: * with Expires: 0 removes the rest.
	if res := p.authDo(p.registerReq(0, "*"), AlgSHA256, "pw"); res.StatusCode != 200 {
		t.Fatalf("Contact: * = %d", res.StatusCode)
	}
	if bs := bindings(t, pbx, aor); len(bs) != 0 {
		t.Fatalf("after Contact: * = %v", bs)
	}
	// Contact: * without Expires: 0 is malformed.
	if res := p.authDo(p.registerReq(60, "*"), AlgSHA256, "pw"); res.StatusCode != 400 {
		t.Fatalf("Contact: * with Expires 60 = %d, want 400", res.StatusCode)
	}
}

// TestRegisterOtherAOR fails if a device can register someone else's AOR.
func TestRegisterOtherAOR(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw"), dev(2, "bob", "200", "pw2")})
	p := newPhone(t, pbx, "alice", "pw")
	req := p.registerReq(300)
	req.To().Address.User = "bob"
	if res := p.authDo(req, AlgSHA256, "pw"); res.StatusCode != 403 {
		t.Fatalf("register bob as alice = %d, want 403", res.StatusCode)
	}
}

func TestAuthFailThrottleFake(t *testing.T) { testAuthFailThrottle(t, nil) }

// TestAuthFailThrottleValkey fails if the counter does not live in Valkey
// under hello:authfail:{ip} with the window as its TTL.
func TestAuthFailThrottleValkey(t *testing.T) {
	_, th, c := valkeyState(t)
	testAuthFailThrottle(t, func(_ *Config, d *Deps) { d.Throttle = th })
	ttl, err := c.Do(context.Background(), c.B().Ttl().Key("hello:authfail:127.0.0.1").Build()).AsInt64()
	if err != nil || ttl <= 0 || ttl > 60 {
		t.Fatalf("authfail TTL = %d, %v; want within the 1m window", ttl, err)
	}
}

// testAuthFailThrottle fails if the attempt after AuthFailLimit failures is
// challenged instead of 403, or if unknown devices are not counted.
func testAuthFailThrottle(t *testing.T, opt pbxOpt) {
	opts := []pbxOpt{func(c *Config, _ *Deps) { c.AuthFailLimit = 3 }}
	if opt != nil {
		opts = append(opts, opt)
	}
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw")}, opts...)
	p := newPhone(t, pbx, "alice", "pw")
	ghost := newPhone(t, pbx, "ghost", "pw")
	if res := ghost.authDo(ghost.registerReq(300), AlgSHA256, "pw"); res.StatusCode != 403 {
		t.Fatalf("unknown device = %d, want 403", res.StatusCode)
	}
	for i := range 2 {
		if res := p.authDo(p.registerReq(300), AlgSHA256, "wrong"); res.StatusCode != 401 {
			t.Fatalf("bad attempt %d = %d, want 401", i+2, res.StatusCode)
		}
	}
	res := p.do(p.registerReq(300))
	if res.StatusCode != 403 || res.GetHeader("WWW-Authenticate") != nil {
		t.Fatalf("attempt after limit = %d (challenge %v), want 403 without challenge", res.StatusCode, res.GetHeader("WWW-Authenticate"))
	}
}

// TestStateUnavailable fails if REGISTER or INVITE are not answered 503
// with Retry-After when the live state cannot be reached.
func TestStateUnavailable(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw"), dev(2, "bob", "200", "pw")})
	p := newPhone(t, pbx, "alice", "pw")
	pbx.fake.down.Store(true)
	res := p.authDo(p.registerReq(300), AlgSHA256, "pw")
	if res.StatusCode != 503 || res.GetHeader("Retry-After") == nil {
		t.Fatalf("REGISTER with state down = %d, want 503 + Retry-After", res.StatusCode)
	}
	_, err := p.call(t.Context(), "200")
	if code := responseCode(err); code != 503 {
		t.Fatalf("INVITE with state down = %v, want 503", err)
	}
	if r := pbx.nextCDR(t); r.FinalStatus != 503 || r.TerminationSide != "system" {
		t.Fatalf("CDR = %+v", r)
	}

	// Throttle store down too.
	pbx2 := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw")}, func(_ *Config, d *Deps) {
		th := &fakeThrottle{n: map[string]int64{}}
		th.down.Store(true)
		d.Throttle = th
	})
	p2 := newPhone(t, pbx2, "alice", "pw")
	if res := p2.do(p2.registerReq(300)); res.StatusCode != 503 {
		t.Fatalf("REGISTER with throttle down = %d, want 503", res.StatusCode)
	}
}

// TestStateUnavailableValkey points a real client at a closed port.
func TestStateUnavailableValkey(t *testing.T) {
	if os.Getenv("HELLO_TEST_VALKEY_ADDR") == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{"127.0.0.1:1"}, ForceSingleClient: true})
	if c == nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw")}, func(_ *Config, d *Deps) {
		d.State, d.Throttle = livestate.New(c), ValkeyThrottle{Client: c, Window: time.Minute}
	})
	p := newPhone(t, pbx, "alice", "pw")
	start := time.Now()
	if res := p.do(p.registerReq(300)); res.StatusCode != 503 || res.GetHeader("Retry-After") == nil {
		t.Fatalf("REGISTER with Valkey down = %d, want 503 + Retry-After", res.StatusCode)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("503 took %s; StateTimeout should bound it", d)
	}
}

// TestRemovedDeviceRejectedAndUnbound fails if a device removed from the
// snapshot can still register, or keeps its bindings after the reload.
func TestRemovedDeviceRejectedAndUnbound(t *testing.T) {
	alice := dev(1, "alice", "100", "pw")
	pbx := startPBX(t, []snapshot.Device{alice})
	p := newPhone(t, pbx, "alice", "pw")
	p.register(t)
	old := pbx.snaps.Current()
	cur := snapshot.New(2, testDomain, nil)
	pbx.snaps.p.Store(cur)
	pbx.srv.SnapshotChanged(old, cur)
	eventually(t, "bindings removed", func() bool { return len(bindings(t, pbx, "sip:alice@"+testDomain)) == 0 })
	if res := p.authDo(p.registerReq(300), AlgSHA256, "pw"); res.StatusCode != 403 {
		t.Fatalf("removed device REGISTER = %d, want 403", res.StatusCode)
	}
}

// TestValkeyThrottleAlwaysExpires fails if a failure counter can be left
// without a TTL (a key with none gets one on the next failure) or if a
// failure extends the window.
func TestValkeyThrottleAlwaysExpires(t *testing.T) {
	_, th, c := valkeyState(t)
	ctx := context.Background()
	key := "hello:authfail:192.0.2.1"
	if err := c.Do(ctx, c.B().Set().Key(key).Value("3").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if err := th.RecordFailure(ctx, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	n, _ := th.Failures(ctx, "192.0.2.1")
	ttl, _ := c.Do(ctx, c.B().Ttl().Key(key).Build()).AsInt64()
	if n != 4 || ttl <= 0 || ttl > 60 {
		t.Fatalf("after failure: count %d ttl %d; want 4 within the 1m window", n, ttl)
	}
	if err := c.Do(ctx, c.B().Expire().Key(key).Seconds(5).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	_ = th.RecordFailure(ctx, "192.0.2.1")
	if ttl, _ := c.Do(ctx, c.B().Ttl().Key(key).Build()).AsInt64(); ttl > 5 {
		t.Fatalf("a failure extended the window to %ds", ttl)
	}
}
