// Package kindupgrade proves the Helm chart's rolling upgrade keeps a live
// call. It runs only in the kind-upgrade CI job (HELLO_KIND=1): the job
// builds the images, creates a kind cluster, installs the chart and starts
// this test, which registers two phones through Kamailio, connects a call,
// runs `helm upgrade` twice (Hello's own pods, then Kamailio) and checks the
// call is still up and can be hung up from either side.
package kindupgrade

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/sipua"
)

const sipDomain = "hello.kind"

var (
	sdpOffer  = []byte("v=0\r\no=a 1 1 IN IP4 192.0.2.10\r\ns=-\r\nc=IN IP4 192.0.2.10\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\n")
	sdpAnswer = []byte("v=0\r\no=b 1 1 IN IP4 192.0.2.20\r\ns=-\r\nc=IN IP4 192.0.2.20\r\nt=0 0\r\nm=audio 40002 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")
)

type env struct {
	api, sip, contactHost, adminPassword, chart, release, namespace, tag, upgradeTag string
}

func loadEnv(t *testing.T) env {
	t.Helper()
	if os.Getenv("HELLO_KIND") != "1" {
		t.Skip("HELLO_KIND=1 not set: this test needs the kind cluster the kind-upgrade CI job creates")
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if def == "" {
			t.Fatalf("%s is required", k)
		}
		return def
	}
	return env{
		api:           get("HELLO_KIND_API", ""),
		sip:           get("HELLO_KIND_SIP", ""),
		contactHost:   get("HELLO_KIND_CONTACT_HOST", ""),
		adminPassword: get("HELLO_KIND_ADMIN_PASSWORD", ""),
		chart:         get("HELLO_KIND_CHART", ""),
		release:       get("HELLO_KIND_RELEASE", "hello"),
		namespace:     get("HELLO_KIND_NAMESPACE", "hello"),
		tag:           get("HELLO_KIND_TAG", ""),
		upgradeTag:    get("HELLO_KIND_UPGRADE_TAG", ""),
	}
}

type client struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (c *client) do(method, path string, body, out any, want int) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s = %d %s, want %d", method, path, resp.StatusCode, buf.String(), want)
	}
	if out != nil {
		return json.Unmarshal(buf.Bytes(), out)
	}
	return nil
}

func (c *client) must(method, path string, body, out any, want int) {
	c.t.Helper()
	if err := c.do(method, path, body, out, want); err != nil {
		c.t.Fatal(err)
	}
}

func eventually(t *testing.T, d time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		if last = f(); last == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s: not within %s: %v", what, d, last)
}

type device struct{ Extension, User, Secret string }

func randDigits(n int) string {
	var b strings.Builder
	for range n {
		d, _ := crand.Int(crand.Reader, big.NewInt(10))
		b.WriteString(d.String())
	}
	return b.String()
}

func (c *client) extension() device {
	c.t.Helper()
	number := "7" + randDigits(7)
	var ext struct{ ID int64 }
	c.must("POST", "/api/v1/extensions", map[string]string{"number": number, "name": "kind " + number}, &ext, 201)
	user := "desk-" + number
	var dev struct {
		ID     int64
		Secret string
	}
	c.must("POST", "/api/v1/devices", map[string]any{"extensionId": ext.ID, "sipUsername": user}, &dev, 201)
	return device{Extension: number, User: user, Secret: dev.Secret}
}

type call struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	State string `json:"state"`
	Node  string `json:"node"`
	HA    string `json:"ha"`
}

func (c *client) calls() ([]call, error) {
	var out struct{ Items []call }
	err := c.do("GET", "/api/v1/calls", nil, &out, 200)
	return out.Items, err
}

func (c *client) callTo(ext string) (call, error) {
	items, err := c.calls()
	if err != nil {
		return call{}, err
	}
	for _, it := range items {
		if it.To == ext {
			return it, nil
		}
	}
	return call{}, fmt.Errorf("no live call to %s among %d", ext, len(items))
}

func (e env) phone(t *testing.T, d device) *sipua.Phone {
	t.Helper()
	p, err := sipua.New(sipua.Options{
		User: d.User, Password: d.Secret, Domain: sipDomain, Proxy: e.sip,
		Listen: "0.0.0.0:0", ContactHost: e.contactHost,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// register registers p, retrying while the nodes pick up the new devices
// (a handful of 403s stays below the failed-authentication throttle).
func register(t *testing.T, p *sipua.Phone) {
	t.Helper()
	var last string
	for range 8 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		res, err := p.Register(ctx, time.Hour)
		cancel()
		switch {
		case err != nil:
			last = err.Error()
		case res.StatusCode == 200:
			return
		default:
			last = fmt.Sprintf("%d %s", res.StatusCode, res.Reason)
		}
		time.Sleep(4 * time.Second)
	}
	t.Fatalf("register: %s", last)
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s %s\n%s", name, strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("%s failed: %v", name, err)
	}
	return string(out)
}

// helmUpgrade runs helm upgrade with the given extra --set arguments while
// polling the call: it must stay listed (possibly on another node) for the
// whole rollout.
func (e env) helmUpgrade(t *testing.T, c *client, ext string, sets ...string) (nodes map[string]bool, ha string) {
	t.Helper()
	args := []string{"upgrade", e.release, e.chart, "-n", e.namespace, "--reuse-values", "--wait", "--timeout", "20m"}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	done := make(chan error, 1)
	go func() {
		cmd := exec.Command("helm", args...)
		out, err := cmd.CombinedOutput()
		t.Logf("$ helm %s\n%s", strings.Join(args, " "), out)
		done <- err
	}()
	nodes = map[string]bool{}
	missing := 0
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("helm upgrade: %v", err)
			}
			t.Logf("call seen on nodes %v during the upgrade; %d polls without the call", nodes, missing)
			return nodes, ha
		case <-tick.C:
			cl, err := c.callTo(ext)
			if err != nil {
				// An API blip while a control replica rolls, or the
				// handoff's few milliseconds: counted, judged by the
				// final assertions, not by one poll.
				missing++
				continue
			}
			nodes[cl.Node] = true
			if cl.HA != "" {
				ha = cl.HA
			}
		}
	}
}

// TestKindRollingUpgrade fails if the call placed before `helm upgrade` is
// lost in either rollout (Hello's own pods, then Kamailio), is no longer
// live afterwards, cannot be hung up from both sides, or leaves no CDR
// closed by that hangup.
func TestKindRollingUpgrade(t *testing.T) {
	e := loadEnv(t)
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: e.api, c: &http.Client{Jar: jar, Timeout: 15 * time.Second}}

	eventually(t, 2*time.Minute, "admin login", func() error {
		return c.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": e.adminPassword}, nil, 204)
	})
	eventually(t, 3*time.Minute, "both hello-sip nodes READY", func() error {
		var v struct {
			Members []struct{ ID, Kind, State string }
		}
		if err := c.do("GET", "/api/v1/cluster", nil, &v, 200); err != nil {
			return err
		}
		ready := 0
		for _, m := range v.Members {
			if strings.HasPrefix(m.ID, "hello-sip-") && m.State == "READY" {
				ready++
			}
		}
		if ready < 2 {
			return fmt.Errorf("%d hello-sip nodes READY, want 2 (members %+v)", ready, v.Members)
		}
		return nil
	})

	caller, callee := c.extension(), c.extension()
	a, b := e.phone(t, caller), e.phone(t, callee)
	register(t, a)
	register(t, b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	got := make(chan *sipua.Incoming, 1)
	go func() {
		in, err := b.Next(ctx)
		if err != nil {
			close(got)
			return
		}
		_ = in.Ring()
		_ = in.Answer(sdpAnswer)
		got <- in
	}()
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v", out, err)
	}
	in := <-got
	if in == nil {
		t.Fatal("the callee never received the call")
	}
	before, err := c.callTo(callee.Extension)
	if err != nil {
		t.Fatalf("the connected call is not in the live view: %v", err)
	}
	t.Logf("call %s connected on %s", before.ID, before.Node)

	// Rollout 1: Hello's own pods, one hello-sip node at a time, each
	// draining its call to the other. Kamailio's image is pinned so it does
	// not restart in the middle of a handoff.
	nodes, ha := e.helmUpgrade(t, c, callee.Extension,
		"global.imageTag="+e.upgradeTag, "kamailio.image.tag="+e.tag)
	t.Logf("rollout 1: nodes %v, ha %q", nodes, ha)
	after, err := c.callTo(callee.Extension)
	if err != nil {
		t.Fatalf("the call is not live after the hello-sip rollout: %v", err)
	}
	if after.State != "connected" {
		t.Fatalf("call state after the rollout = %q, want connected", after.State)
	}
	// Both nodes were replaced, so the call has moved at least once; the
	// takeover/handoff mark says so.
	if after.Node == before.Node && after.HA != "taken-over" && len(nodes) < 2 {
		t.Errorf("the call never left %s and is not marked taken-over: the rollout did not exercise a handoff", before.Node)
	}
	select {
	case <-in.Ended():
		t.Fatal("the callee's dialog ended during the hello-sip rollout")
	case <-out.Ended():
		t.Fatal("the caller's dialog ended during the hello-sip rollout")
	default:
	}

	// Rollout 2: Kamailio (a pod annotation changes, as a config change
	// would). Signalling pauses while it is down; the call must survive it.
	e.helmUpgrade(t, c, callee.Extension, "kamailio.podAnnotations.hello-kind-rollout=2")
	if _, err := c.callTo(callee.Extension); err != nil {
		t.Fatalf("the call is not live after the Kamailio rollout: %v", err)
	}

	// The call is still a call: the callee hangs up, the caller sees it.
	hctx, hcancel := context.WithTimeout(ctx, 30*time.Second)
	defer hcancel()
	if err := in.Hangup(hctx); err != nil {
		t.Fatalf("the callee's hangup after the upgrade: %v", err)
	}
	select {
	case <-out.Ended():
	case <-time.After(15 * time.Second):
		t.Fatal("the caller was not told the call ended after the callee hung up")
	}
	eventually(t, 30*time.Second, "the call leaving the live view", func() error {
		if _, err := c.callTo(callee.Extension); err == nil {
			return fmt.Errorf("still live")
		}
		return nil
	})
	var cdrs struct {
		Items []struct {
			Destination string `json:"destination"`
			FinalStatus int    `json:"finalStatus"`
		}
	}
	eventually(t, 30*time.Second, "a CDR for the call", func() error {
		if err := c.do("GET", "/api/v1/cdrs?limit=200", nil, &cdrs, 200); err != nil {
			return err
		}
		for _, it := range cdrs.Items {
			if it.Destination == callee.Extension && it.FinalStatus == 200 {
				return nil
			}
		}
		return fmt.Errorf("no answered CDR to %s in %d", callee.Extension, len(cdrs.Items))
	})
}
