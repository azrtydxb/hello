package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/test/sipua"
)

// The lab tests drive the real compose lab: two control-plane nodes, two SIP
// nodes, PostgreSQL and Valkey. They run with HELLO_DOCKER=1. The lab is
// started once per test binary under its own project name and removed at the
// end; HELLO_LAB_KEEP=1 reuses a running lab and leaves it up, for iterating.

const (
	labDomain   = "hello.lab"
	labAPI      = "http://localhost:8081"
	labSIP1     = "127.0.0.1:5060"
	labSIP2     = "127.0.0.1:5062"
	labAdmin    = "admin"
	labPassword = "hello-lab-admin" // lab-only bootstrap password from compose.yaml
)

var (
	labOnce    sync.Once
	labErr     error
	labStarted bool
)

func labProject() string {
	if p := os.Getenv("HELLO_LAB_PROJECT"); p != "" {
		return p
	}
	return "hello-smoke"
}

func compose(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-p", labProject(), "-f", filepath.Join("deploy", "docker-compose", "compose.yaml")}, args...)...)
	cmd.Dir = root
	return cmd
}

func TestMain(m *testing.M) {
	code := m.Run()
	if labStarted && os.Getenv("HELLO_LAB_KEEP") != "1" {
		if out, err := compose("down", "-v").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "compose down: %v\n%s", err, out)
			code = 1
		}
	}
	os.Exit(code)
}

// labUp starts the lab once and skips the test unless HELLO_DOCKER=1.
func labUp(t *testing.T) {
	t.Helper()
	docker(t)
	labOnce.Do(func() {
		labStarted = true
		out, err := compose("up", "-d", "--build", "--wait", "--wait-timeout", "240").CombinedOutput()
		if err != nil {
			labErr = fmt.Errorf("compose up: %w\n%s", err, out)
		}
	})
	if labErr != nil {
		t.Fatal(labErr)
	}
}

// waitLabServing waits until the lab takes registrations again: both SIP
// nodes answer readyz 200, Kamailio dispatches to both, and a Valkey
// primary is known. A sibling test's failure or its cleanup can leave the
// lab briefly unable to serve — e.g. Kamailio marks both nodes inactive
// while a Valkey primary is down, and re-probes them active only after it
// is back. The next test must wait that out instead of failing its first
// REGISTER with 503 No Hello Node Available.
func waitLabServing(t *testing.T) {
	t.Helper()
	eventually(t, 60*time.Second, "the lab serving registrations", func() error {
		for _, port := range []string{"8082", "8083"} {
			if code := readyz(t, "http://localhost:"+port+"/readyz"); code != 200 {
				return fmt.Errorf("node readyz = %d", code)
			}
		}
		for _, n := range []string{"hello-sip-1", "hello-sip-2"} {
			flags, err := dispatcherFlags(n)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(flags, "A") {
				return fmt.Errorf("%s dispatcher flags %s", n, flags)
			}
		}
		if valkeyPrimary(t) == "" {
			return errors.New("no Valkey primary")
		}
		return nil
	})
}

// labClient is an authenticated management API client.
type labClient struct {
	t *testing.T
	c *http.Client
}

func newLabClient(t *testing.T) *labClient {
	t.Helper()
	labUp(t)
	jar, _ := cookiejar.New(nil)
	lc := &labClient{t: t, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	var err error
	for range 30 { // bootstrap admin is created asynchronously at startup
		if err = lc.do("POST", "/api/v1/auth/login", map[string]string{"username": labAdmin, "password": labPassword}, nil, 204); err == nil {
			waitLabServing(t)
			return lc
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("login: %v", err)
	return nil
}

func (lc *labClient) do(method, path string, body, out any, want int) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, labAPI+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := lc.c.Do(req)
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

func (lc *labClient) must(method, path string, body, out any, want int) {
	lc.t.Helper()
	if err := lc.do(method, path, body, out, want); err != nil {
		lc.t.Fatal(err)
	}
}

// labDevice is a provisioned device and its one-time secret.
type labDevice struct {
	ID        int64
	Extension string
	User      string
	Secret    string
}

// randDigits returns n random digits, so each test owns its extensions.
func randDigits(n int) string {
	var b strings.Builder
	for range n {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteString(d.String())
	}
	return b.String()
}

// extension creates an extension with the given devices and returns them.
//
// It returns once every ready SIP node serves a snapshot holding the new
// devices, so a test's first REGISTER succeeds. Registering earlier gets
// 403 for an unknown device, and every such 403 counts toward the per-IP
// failed-authentication throttle; the lab's phones all share the runner's
// address, so a few dozen of those within the throttle window lock every
// later test out with 403 until the window ends.
func (lc *labClient) extension(devices ...string) []labDevice {
	lc.t.Helper()
	out := lc.createExtension(devices...)
	lc.waitSnapshots(10 * time.Second)
	return out
}

// createExtension is extension without waiting for the SIP nodes.
func (lc *labClient) createExtension(devices ...string) []labDevice {
	lc.t.Helper()
	number := "7" + randDigits(7)
	var ext struct{ ID int64 }
	lc.must("POST", "/api/v1/extensions", map[string]string{"number": number, "name": "test " + number}, &ext, 201)
	out := make([]labDevice, 0, len(devices))
	for _, d := range devices {
		user := d + "-" + number
		var dev struct {
			ID     int64
			Secret string
		}
		lc.must("POST", "/api/v1/devices", map[string]any{"extensionId": ext.ID, "sipUsername": user}, &dev, 201)
		out = append(out, labDevice{ID: dev.ID, Extension: number, User: user, Secret: dev.Secret})
	}
	return out
}

// labNodeHTTP maps each SIP node to its published health/metrics address.
var labNodeHTTP = map[string]string{"hello-sip-1": "http://localhost:8082", "hello-sip-2": "http://localhost:8083"}

// configRevision is the control plane's current configuration revision.
func (lc *labClient) configRevision() int64 {
	lc.t.Helper()
	var v struct{ ConfigRevision int64 }
	lc.must("GET", "/api/v1/version", nil, &v, 200)
	return v.ConfigRevision
}

// waitSnapshots waits until every ready SIP node serves the control plane's
// current revision, failing the test after d. A node that is not ready
// (killed, partitioned, draining) takes no new registrations and is skipped.
func (lc *labClient) waitSnapshots(d time.Duration) {
	lc.t.Helper()
	want := lc.configRevision()
	for _, node := range []string{"hello-sip-1", "hello-sip-2"} {
		if readyz(lc.t, labNodeHTTP[node]+"/readyz") != 200 {
			continue
		}
		waitSnapshot(lc.t, node, want, time.Now().Add(d))
	}
}

// waitSnapshot waits until node serves configuration revision want or newer,
// failing the test unless that happens by deadline. It reads the node's
// hello_config_revision gauge, so waiting sends no REGISTER.
func waitSnapshot(t *testing.T, node string, want int64, deadline time.Time) {
	t.Helper()
	eventuallyBy(t, deadline, node+" serving configuration revision "+strconv.FormatInt(want, 10), func(context.Context) error {
		got, err := nodeConfigRevision(node)
		if err != nil {
			return err
		}
		if got < want {
			return fmt.Errorf("at revision %d", got)
		}
		return nil
	})
}

// nodeConfigRevision reads node's hello_config_revision gauge.
func nodeConfigRevision(node string) (int64, error) {
	res, err := (&http.Client{Timeout: time.Second}).Get(labNodeHTTP[node] + "/metrics")
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, "hello_config_revision "); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return int64(f), err
		}
	}
	return 0, errors.New("no hello_config_revision in the node's metrics")
}

// extensionID looks up an extension's id by number, for the PATCH paths
// that address extensions by id.
func (lc *labClient) extensionID(number string) string {
	var list struct {
		Items []struct {
			ID     int64  `json:"id"`
			Number string `json:"number"`
		} `json:"items"`
	}
	lc.must("GET", "/api/v1/extensions", nil, &list, 200)
	for _, e := range list.Items {
		if e.Number == number {
			return strconv.FormatInt(e.ID, 10)
		}
	}
	lc.t.Fatalf("extension %s not found", number)
	return ""
}

// phone starts a test phone for d that sends to the given SIP node.
func phone(t *testing.T, d labDevice, node string) *sipua.Phone {
	t.Helper()
	p, err := sipua.New(sipua.Options{
		User: d.User, Password: d.Secret, Domain: labDomain, Proxy: node,
		Listen: "0.0.0.0:0", ContactHost: os.Getenv("HELLO_LAB_CONTACT_HOST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// register registers p for an hour and fails the test on anything but 200.
// Devices from lc.extension are already in the ready nodes' snapshots, so
// there is nothing to retry: a 403 here is a real failure (and a counted
// failed authentication).
func register(t *testing.T, p *sipua.Phone) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := p.Register(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("REGISTER = %d %s", res.StatusCode, res.Reason)
	}
}

var (
	sdpOffer  = []byte("v=0\r\no=a 1 1 IN IP4 192.0.2.10\r\ns=-\r\nc=IN IP4 192.0.2.10\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\n")
	sdpAnswer = []byte("v=0\r\no=b 1 1 IN IP4 192.0.2.20\r\ns=-\r\nc=IN IP4 192.0.2.20\r\nt=0 0\r\nm=audio 40002 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")
)

// sdpDirectOrAnchored accepts either the pass-through SDP (direct media) or
// the anchor's SDP for the call. Phase 5 anchors these lab calls: the phones
// run on the runner and reach the nodes through Docker's published ports, so
// a binding's contact host:port never equals the packet source (the
// masquerade rewrites the port), which is a true NAT under the anchoring
// contract — the media anchors even though both phones share the host. The
// anchor's SDP keeps the caller offer's c= address (the lab nodes advertise
// no anchor host) and its audio payload type, on a relay port.
func sdpDirectOrAnchored(want, got []byte) error {
	if bytes.Equal(want, got) {
		return nil
	}
	off, err := media.ParseAudioSDP(sdpOffer)
	if err != nil {
		return err
	}
	ans, err := media.ParseAudioSDP(got)
	if err != nil {
		return fmt.Errorf("SDP %q is neither the expected %q nor audio SDP: %w", got, want, err)
	}
	if ans.Address != off.Address || ans.PayloadType != off.PayloadType {
		return fmt.Errorf("SDP %q is neither the expected %q nor the anchor's SDP for the offer", got, want)
	}
	return nil
}

func TestCallAcrossNodes(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.extension("desk")[0], lc.extension("desk")[0]
	a, b := phone(t, caller, labSIP2), phone(t, callee, labSIP1)
	register(t, a)
	register(t, b)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// What the nodes actually observe for these phones decides direct vs
	// anchored media (contract 2); log it so failures explain themselves.
	var regs struct {
		Items []map[string]any `json:"items"`
	}
	if err := lc.do("GET", "/api/v1/registrations", nil, &regs, 200); err != nil {
		t.Logf("registrations unavailable: %v", err)
	} else {
		t.Logf("registrations: %v", regs.Items)
	}
	answered := make(chan error, 1)
	go func() {
		in, err := b.Next(ctx)
		if err != nil {
			answered <- err
			return
		}
		if err := sdpDirectOrAnchored(sdpOffer, in.Request.Body()); err != nil {
			answered <- fmt.Errorf("callee media: %w", err)
			return
		}
		_ = in.Ring()
		answered <- in.Answer(sdpAnswer)
	}()
	out, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	if out.Status != 200 {
		t.Fatalf("dial = %d %q, want 200", out.Status, out.Response.Body())
	}
	// The caller's 200 body: the callee's answer (direct) or the anchor's
	// SDP (anchored; it carries the offer's address and audio payload type).
	if err := sdpDirectOrAnchored(sdpAnswer, out.Response.Body()); err != nil {
		t.Fatalf("dial media: %v", err)
	}
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
}
