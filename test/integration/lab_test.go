package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
func (lc *labClient) extension(devices ...string) []labDevice {
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

func TestCallAcrossNodes(t *testing.T) {
	lc := newLabClient(t)
	caller, callee := lc.extension("desk")[0], lc.extension("desk")[0]
	a, b := phone(t, caller, labSIP2), phone(t, callee, labSIP1)
	register(t, a)
	register(t, b)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	answered := make(chan error, 1)
	go func() {
		in, err := b.Next(ctx)
		if err != nil {
			answered <- err
			return
		}
		if !bytes.Equal(in.Request.Body(), sdpOffer) {
			answered <- fmt.Errorf("callee got SDP %q, want the caller's offer unchanged", in.Request.Body())
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
	if out.Status != 200 || !bytes.Equal(out.Response.Body(), sdpAnswer) {
		t.Fatalf("dial = %d %q, want 200 with the callee's answer unchanged", out.Status, out.Response.Body())
	}
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
}
