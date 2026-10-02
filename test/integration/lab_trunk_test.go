package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/sipua"
)

// Lab-level trunk and routing tests (spec .procoder/specs/trunks-routing.md)
// against the simulated carriers in compose.yaml: carrier-primary requires
// registration with digest and challenges INVITEs with 407; carrier-backup
// is IP-authenticated.

const (
	primaryHTTP     = "http://localhost:8091"
	backupHTTP      = "http://localhost:8092"
	primaryUser     = "hello-trunk"
	primaryPassword = "lab-only-trunk-password" // matches compose.yaml; lab only
	primaryRealm    = "carrier-primary.lab"
)

func init() { remember(primaryPassword) }

type carrierRequest struct {
	Method     string            `json:"method"`
	RequestURI string            `json:"requestUri"`
	From       string            `json:"from"`
	Source     string            `json:"source"`
	Headers    map[string]string `json:"headers"`
	Outcome    int               `json:"outcome"`
}

func carrierDo(t *testing.T, base, method, path string, body any, out any) {
	t.Helper()
	var rd bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = *bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, base+path, &rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s%s: %v", method, base, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		t.Fatalf("%s %s%s = %d %s", method, base, path, resp.StatusCode, b.String())
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

// carrierInvites returns the INVITEs a carrier received for number.
func carrierInvites(t *testing.T, base, number string) []carrierRequest {
	t.Helper()
	var log []carrierRequest
	carrierDo(t, base, "GET", "/log", nil, &log)
	var out []carrierRequest
	for _, r := range log {
		if r.Method == "INVITE" && strings.Contains(r.RequestURI, number) && r.Outcome != 407 {
			out = append(out, r)
		}
	}
	return out
}

func carrierMode(t *testing.T, base string, failWith int) {
	t.Helper()
	carrierDo(t, base, "POST", "/mode", map[string]int{"failWith": failWith}, nil)
	t.Cleanup(func() { carrierDo(t, base, "POST", "/mode", map[string]int{"failWith": 0}, nil) })
}

type labTrunk struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// trunks creates the two lab trunks (unique names) and returns primary and
// backup. maxCalls applies to the backup.
func (lc *labClient) trunks(backupMax int) (primary, backup labTrunk) {
	lc.t.Helper()
	suffix := randDigits(6)
	lc.must("POST", "/api/v1/trunks", map[string]any{
		"name": "primary-" + suffix, "mode": "registration", "username": primaryUser, "password": primaryPassword,
		"realm": primaryRealm, "fromDomain": "hello.lab", "registerExpires": 120, "optionsInterval": 5,
		"destinations": []map[string]any{{"host": "carrier-primary", "port": 5060, "priority": 0, "weight": 1}},
		"enabled":      true,
	}, &primary, 201)
	lc.must("POST", "/api/v1/trunks", map[string]any{
		"name": "backup-" + suffix, "mode": "ip", "optionsInterval": 5, "maxCalls": backupMax,
		"defaultCallerId": "+97145550999",
		"destinations":    []map[string]any{{"host": "carrier-backup", "port": 5060, "priority": 0, "weight": 1}},
		"enabled":         true,
	}, &backup, 201)
	lc.t.Cleanup(func() {
		for _, id := range []int64{primary.ID, backup.ID} {
			_ = lc.do("DELETE", fmt.Sprintf("/api/v1/trunks/%d", id), nil, nil, 204)
		}
	})
	return primary, backup
}

// outbound creates an outbound route at the front of the list: dialled
// numbers starting with 9 have the 9 stripped and go to trunks in order.
func (lc *labClient) outbound(callerIDPrefix string, trunks ...labTrunk) int64 {
	lc.t.Helper()
	ids := make([]int64, len(trunks))
	for i, tr := range trunks {
		ids[i] = tr.ID
	}
	var r struct{ ID int64 }
	lc.must("POST", "/api/v1/routes/outbound", map[string]any{
		"name": "lab-" + randDigits(6), "matchKind": "regex", "match": "^9([0-9]{6,15})$",
		"numberTransform":   map[string]any{"regex": "^9([0-9]+)$", "template": "${1}"},
		"callerIdTransform": map[string]any{"prefix": callerIDPrefix}, "trunks": ids, "enabled": true,
	}, &r, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/routes/outbound/%d", r.ID), nil, nil, 204) })
	return r.ID
}

func (lc *labClient) inbound(did string, trunk labTrunk, ext string, extra map[string]any) int64 {
	lc.t.Helper()
	body := map[string]any{
		"name": "lab-in-" + randDigits(6), "didKind": "exact", "did": did, "trunkId": trunk.ID,
		"destinationKind": "extension", "destination": ext, "enabled": true,
	}
	for k, v := range extra {
		body[k] = v
	}
	var r struct{ ID int64 }
	lc.must("POST", "/api/v1/routes/inbound", body, &r, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/routes/inbound/%d", r.ID), nil, nil, 204) })
	return r.ID
}

type trunkStatus struct {
	TrunkID      int64 `json:"trunkId"`
	Registration *struct {
		State string `json:"state"`
		Node  string `json:"node"`
	} `json:"registration"`
	Destinations []struct {
		Destination string `json:"destination"`
		Up          bool   `json:"up"`
	} `json:"destinations"`
	ActiveCalls int `json:"activeCalls"`
}

func (lc *labClient) trunkStatus(id int64) trunkStatus {
	lc.t.Helper()
	var out struct{ Items []trunkStatus }
	lc.must("GET", "/api/v1/trunks/status", nil, &out, 200)
	for _, s := range out.Items {
		if s.TrunkID == id {
			return s
		}
	}
	return trunkStatus{TrunkID: id}
}

// dialOut places an outbound call from a fresh extension's phone and
// returns the final status (hanging up an answered call after holdFor).
func dialOut(t *testing.T, lc *labClient, number string, holdFor time.Duration) int {
	t.Helper()
	d := lc.devices("desk")[0]
	p := phone(t, d, labSIP1)
	register(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := p.Dial(ctx, number, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status == 200 {
		time.Sleep(holdFor)
		_ = out.Hangup(ctx)
	}
	return out.Status
}

func TestTrunkRegistrationLeaseFailover(t *testing.T) {
	lc := newLabClient(t)
	primary, _ := lc.trunks(0)
	var registered []string
	eventually(t, 20*time.Second, "one registered contact at the carrier", func() error {
		carrierDo(t, primaryHTTP, "GET", "/registrations", nil, &registered)
		if len(registered) != 1 {
			return fmt.Errorf("contacts = %v", registered)
		}
		return nil
	})
	st := lc.trunkStatus(primary.ID)
	if st.Registration == nil || st.Registration.State != "registered" {
		t.Fatalf("trunk status = %+v", st)
	}
	holder := st.Registration.Node
	other := map[string]string{"hello-sip-1": "hello-sip-2", "hello-sip-2": "hello-sip-1"}[holder]
	if other == "" {
		t.Fatalf("unexpected lease holder %q", holder)
	}
	t.Cleanup(func() { labCompose(t, "up", "-d", "--wait", holder) })
	labCompose(t, "kill", holder)
	eventually(t, 60*time.Second, "the other node takes the lease and registers", func() error {
		st := lc.trunkStatus(primary.ID)
		if st.Registration == nil || st.Registration.Node != other || st.Registration.State != "registered" {
			return fmt.Errorf("status = %+v", st.Registration)
		}
		return nil
	})
}

func TestTrunkOptionsHealth(t *testing.T) {
	lc := newLabClient(t)
	_, backup := lc.trunks(0)
	up := func(want bool) func() error {
		return func() error {
			st := lc.trunkStatus(backup.ID)
			if len(st.Destinations) == 0 || st.Destinations[0].Up != want {
				return fmt.Errorf("destinations = %+v, want up=%v", st.Destinations, want)
			}
			return nil
		}
	}
	eventually(t, 20*time.Second, "backup destination up", up(true))
	t.Cleanup(func() { labCompose(t, "up", "-d", "carrier-backup") })
	labCompose(t, "stop", "carrier-backup")
	eventually(t, 20*time.Second, "down within two OPTIONS intervals (+ timeout)", up(false))
	// While down, routing must not select it: a route to backup alone fails.
	lc.outbound("", backup)
	if code := dialOut(t, lc, "9971500000000", 0); code != 503 {
		t.Fatalf("call via a down trunk = %d, want 503", code)
	}
	labCompose(t, "start", "carrier-backup")
	eventually(t, 20*time.Second, "backup destination up again", up(true))
}

func TestOutboundFailover(t *testing.T) {
	lc := newLabClient(t)
	primary, backup := lc.trunks(0)
	lc.outbound("", primary, backup)
	eventually(t, 20*time.Second, "primary registered", func() error {
		if st := lc.trunkStatus(primary.ID); st.Registration == nil || st.Registration.State != "registered" {
			return errors.New("not yet")
		}
		return nil
	})

	carrierMode(t, primaryHTTP, 503)
	number := "97150" + randDigits(5) + "00"
	if code := dialOut(t, lc, "9"+number, 200*time.Millisecond); code != 200 {
		t.Fatalf("call with primary 503 = %d, want 200 through backup", code)
	}
	if n := len(carrierInvites(t, primaryHTTP, number)); n != 1 {
		t.Fatalf("primary got %d INVITEs for the failed-over call, want 1", n)
	}
	if n := len(carrierInvites(t, backupHTTP, number)); n != 1 {
		t.Fatalf("backup got %d INVITEs, want 1", n)
	}

	carrierMode(t, primaryHTTP, 486)
	busy := "97150" + randDigits(5) + "00"
	if code := dialOut(t, lc, "9"+busy, 0); code != 486 {
		t.Fatalf("call with primary 486 = %d, want 486", code)
	}
	if n := len(carrierInvites(t, backupHTTP, busy)); n != 0 {
		t.Fatal("486 was failed over to the backup")
	}
}

func TestTrunkConcurrencyLimit(t *testing.T) {
	lc := newLabClient(t)
	primary, backup := lc.trunks(1)
	lc.outbound("", backup, primary)
	eventually(t, 20*time.Second, "primary registered", func() error {
		if st := lc.trunkStatus(primary.ID); st.Registration == nil || st.Registration.State != "registered" {
			return errors.New("not yet")
		}
		return nil
	})
	// Call 1 rings forever at the backup (number ends 80) and holds its slot.
	held := "97150" + randDigits(5) + "80"
	d := lc.devices("desk")[0]
	p := phone(t, d, labSIP1)
	register(t, p)
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { defer close(done1); _, _ = p.Dial(ctx1, "9"+held, sdpOffer) }()
	eventually(t, 10*time.Second, "first call reached the backup", func() error {
		if len(carrierInvites(t, backupHTTP, held)) == 0 {
			return errors.New("not yet")
		}
		return nil
	})
	// Call 2: the backup is full, so it must go to the primary.
	second := "97150" + randDigits(5) + "00"
	if code := dialOut(t, lc, "9"+second, 0); code != 200 {
		t.Fatalf("second call = %d", code)
	}
	if len(carrierInvites(t, backupHTTP, second)) != 0 || len(carrierInvites(t, primaryHTTP, second)) != 1 {
		t.Fatal("a full trunk carried a second call instead of failing over")
	}
	cancel1()
	<-done1
	// The slot is released: call 3 goes to the backup again.
	third := "97150" + randDigits(5) + "00"
	eventually(t, 10*time.Second, "backup slot released", func() error {
		if lc.trunkStatus(backup.ID).ActiveCalls != 0 {
			return errors.New("slot still held")
		}
		return nil
	})
	if code := dialOut(t, lc, "9"+third, 0); code != 200 || len(carrierInvites(t, backupHTTP, third)) != 1 {
		t.Fatalf("third call = %d, backup INVITEs %d", code, len(carrierInvites(t, backupHTTP, third)))
	}
}

func TestCallerIDPolicy(t *testing.T) {
	lc := newLabClient(t)
	_, backup := lc.trunks(0)
	lc.outbound("", backup)
	withExt := lc.devices("desk")[0]
	var ext struct{ ID int64 }
	var exts struct {
		Items []struct {
			ID     int64
			Number string
		}
	}
	lc.must("GET", "/api/v1/extensions", nil, &exts, 200)
	for _, e := range exts.Items {
		if e.Number == withExt.Extension {
			ext.ID = e.ID
		}
	}
	lc.must("PATCH", fmt.Sprintf("/api/v1/extensions/%d", ext.ID), map[string]any{"externalNumber": "+97145550101"}, nil, 200)

	call := func(d labDevice) string {
		p := phone(t, d, labSIP1)
		register(t, p)
		number := "97150" + randDigits(5) + "00"
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := p.Dial(ctx, "9"+number, sdpOffer)
		if err != nil || out.Status != 200 {
			t.Fatalf("dial = %+v, %v", out, err)
		}
		_ = out.Hangup(ctx)
		inv := carrierInvites(t, backupHTTP, number)
		if len(inv) != 1 {
			t.Fatalf("backup INVITEs = %d", len(inv))
		}
		return inv[0].From
	}
	if got := call(withExt); got != "+97145550101" {
		t.Fatalf("caller ID with external number = %q", got)
	}
	without := lc.devices("desk")[0]
	if got := call(without); got != "+97145550999" {
		t.Fatalf("caller ID without external number = %q, want the trunk default", got)
	}
}

func TestInboundRouting(t *testing.T) {
	lc := newLabClient(t)
	_, backup := lc.trunks(0)
	d := lc.devices("desk")[0]
	p := phone(t, d, labSIP1)
	register(t, p)
	did := "+9714555" + randDigits(4)
	lc.inbound(did, backup, d.Extension, nil)
	// A closed schedule must not match: this DID's only route is open on
	// every day except today (UTC), all day.
	closedDID := "+9714556" + randDigits(4)
	var otherDays []int
	for d := range 7 {
		if time.Weekday(d) != time.Now().UTC().Weekday() {
			otherDays = append(otherDays, d)
		}
	}
	lc.inbound(closedDID, backup, d.Extension, map[string]any{"schedule": map[string]any{
		"timeZone": "UTC", "windows": []map[string]any{{"days": otherDays, "start": "00:00", "end": "23:59"}},
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rang := answerNext(ctx, p)
	var res struct{ Status int }
	carrierDo(t, backupHTTP, "POST", "/call", map[string]any{"from": "+97145557777", "to": did, "target": "hello-sip-1:5060", "hangupAfterMs": 200}, &res)
	if res.Status != 200 {
		t.Fatalf("inbound call to %s = %d", did, res.Status)
	}
	if in := <-rang; in == nil {
		t.Fatal("the mapped extension did not ring")
	}
	carrierDo(t, backupHTTP, "POST", "/call", map[string]any{"from": "+97145557777", "to": closedDID, "target": "hello-sip-1:5060"}, &res)
	if res.Status != 404 {
		t.Fatalf("inbound call to a closed-schedule DID = %d, want 404", res.Status)
	}

	// A carrier-style INVITE (no credentials, foreign From domain) from an
	// address that is no trunk — a host-side UA, not the carrier: 403.
	stranger, err := sipua.New(sipua.Options{User: "+97145558888", Domain: "carrier.example", Proxy: labSIP1, Listen: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stranger.Close)
	out, err := stranger.Dial(ctx, did, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 403 {
		t.Fatalf("INVITE from a non-trunk source = %d, want 403", out.Status)
	}
	// Clear the throttle count this refusal added for the host's IP.
	labCompose(t, "exec", "-T", "valkey", "sh", "-c", "valkey-cli --scan --pattern 'hello:authfail:*' | xargs -r valkey-cli del")
}

func TestCDRTrace(t *testing.T) {
	lc := newLabClient(t)
	_, backup := lc.trunks(0)
	lc.outbound("", backup)
	number := "97150" + randDigits(5) + "00"
	if code := dialOut(t, lc, "9"+number, 100*time.Millisecond); code != 200 {
		t.Fatalf("outbound = %d", code)
	}
	failed := "97150" + randDigits(5) + "86"
	if code := dialOut(t, lc, "9"+failed, 0); code != 486 {
		t.Fatalf("failed outbound = %d", code)
	}
	type cdr struct {
		ID                   int64  `json:"id"`
		Direction            string `json:"direction"`
		OriginalDestination  string `json:"originalDestination"`
		RewrittenDestination string `json:"rewrittenDestination"`
		Route                string `json:"route"`
		Trunk                string `json:"trunk"`
		FinalStatus          int    `json:"finalStatus"`
	}
	find := func(rewritten string) (cdr, error) {
		var page struct{ Items []cdr }
		lc.must("GET", "/api/v1/cdrs?limit=200", nil, &page, 200)
		for _, c := range page.Items {
			if c.RewrittenDestination == rewritten {
				return c, nil
			}
		}
		return cdr{}, errors.New("no CDR yet")
	}
	for _, tc := range []struct {
		number string
		status int
	}{{number, 200}, {failed, 486}} {
		var c cdr
		eventually(t, 10*time.Second, "CDR for "+tc.number, func() (err error) { c, err = find(tc.number); return })
		if c.Direction != "outbound" || c.OriginalDestination != "9"+tc.number || c.Route == "" || c.Trunk != backup.Name || c.FinalStatus != tc.status {
			t.Fatalf("CDR = %+v", c)
		}
		var detail struct {
			Trace []struct {
				N    int    `json:"n"`
				Text string `json:"text"`
			} `json:"trace"`
			Explanation string `json:"explanation"`
		}
		lc.must("GET", fmt.Sprintf("/api/v1/cdrs/%d", c.ID), nil, &detail, 200)
		texts := make([]string, len(detail.Trace))
		for i, s := range detail.Trace {
			texts[i] = s.Text
			if s.N != i+1 {
				t.Fatalf("trace step numbers = %+v", detail.Trace)
			}
		}
		joined := strings.Join(texts, "\n")
		if !strings.Contains(joined, "Rewrite 9"+tc.number+" -> "+tc.number) || !strings.Contains(joined, backup.Name) {
			t.Fatalf("trace lacks the rewrite or trunk:\n%s", joined)
		}
		if tc.status != 200 && detail.Explanation == "" {
			t.Fatalf("failed call has no explanation; trace:\n%s", joined)
		}
		if tc.status == 200 && !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "200") }) {
			t.Fatalf("answered call's trace lacks the 200:\n%s", joined)
		}
	}
}
