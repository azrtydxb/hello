// Command verify checks the lab's SIP edge by hand (plan Task 5): test
// phones on this host register through Kamailio, the program reports which
// hello-sip node holds each binding, and it places calls between phones on
// different nodes, ending one from the caller's side and one from the
// callee's. It exits non-zero on the first failure.
//
//	go run ./deploy/verify                 # 6 phones, both hang-up sides
//	go run ./deploy/verify -phones 2       # e.g. with one SIP node stopped
//
// It needs the compose lab (deploy/docker-compose) and creates extensions
// through the management API with the lab-only bootstrap admin. Device
// secrets are never printed.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"

	"github.com/azrtydxb/hello/test/sipua"
)

var (
	api      = flag.String("api", "http://localhost:8081", "management API base URL")
	edge     = flag.String("kamailio", "127.0.0.1:5080", "Kamailio host:port the phones use")
	domain   = flag.String("domain", "hello.lab", "SIP domain")
	admin    = flag.String("admin", "admin", "management user")
	password = flag.String("password", "hello-lab-admin", "management password (lab-only default)")
	phones   = flag.Int("phones", 6, "phones to register")
	calls    = flag.Bool("calls", true, "place the test calls")
)

var (
	offer  = []byte("v=0\r\no=a 1 1 IN IP4 192.0.2.10\r\ns=-\r\nc=IN IP4 192.0.2.10\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
	answer = []byte("v=0\r\no=b 1 1 IN IP4 192.0.2.20\r\ns=-\r\nc=IN IP4 192.0.2.20\r\nt=0 0\r\nm=audio 40002 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
)

type client struct{ c *http.Client }

func (c *client) do(method, path string, body, out any, want int) error {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, *api+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		return fmt.Errorf("%s %s = %d %s", method, path, res.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type device struct{ ext, user, secret string }

func (c *client) newDevice() (device, error) {
	n, _ := rand.Int(rand.Reader, big.NewInt(9_000_000))
	number := fmt.Sprintf("6%07d", n.Int64())
	var ext struct{ ID int64 }
	if err := c.do("POST", "/api/v1/extensions", map[string]string{"number": number, "name": "verify " + number}, &ext, 201); err != nil {
		return device{}, err
	}
	user := "verify-" + number
	var dev struct{ Secret string }
	if err := c.do("POST", "/api/v1/devices", map[string]any{"extensionId": ext.ID, "sipUsername": user}, &dev, 201); err != nil {
		return device{}, err
	}
	return device{ext: number, user: user, secret: dev.Secret}, nil
}

// node returns the hello-sip node holding user's binding.
func (c *client) node(user string) string {
	var out struct {
		Items []struct {
			Device       string `json:"device"`
			ReceivedNode string `json:"receivedNode"`
			Source       string `json:"source"`
			Path         []string
		}
	}
	if err := c.do("GET", "/api/v1/registrations", nil, &out, 200); err != nil {
		return "?"
	}
	for _, b := range out.Items {
		if b.Device == user {
			return fmt.Sprintf("%s (source %s, path %s)", b.ReceivedNode, b.Source, strings.Join(b.Path, ","))
		}
	}
	return "none"
}

func register(p *sipua.Phone) error {
	deadline := time.Now().Add(5 * time.Second) // a new device reaches the snapshots within 2s
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		res, err := p.Register(ctx, time.Hour)
		cancel()
		if err != nil {
			return err
		}
		if res.StatusCode == 200 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("REGISTER = %d %s", res.StatusCode, res.Reason)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// call places a call from a to b's extension; byCaller picks who hangs up.
func call(a, b *sipua.Phone, ext string, byCaller bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	in := make(chan *sipua.Incoming, 1)
	go func() {
		i, err := b.Next(ctx)
		if err != nil {
			close(in)
			return
		}
		_ = i.Ring()
		_ = i.Answer(answer)
		in <- i
	}()
	out, err := a.Dial(ctx, ext, offer)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	if out.Status != 200 {
		return fmt.Errorf("INVITE = %d", out.Status)
	}
	got, ok := <-in
	if !ok {
		return errors.New("callee never saw the INVITE")
	}
	time.Sleep(500 * time.Millisecond)
	if byCaller {
		if err := out.Hangup(ctx); err != nil {
			return fmt.Errorf("caller BYE: %w", err)
		}
		select {
		case <-got.Ended():
		case <-ctx.Done():
			return errors.New("callee never got the BYE")
		}
		return nil
	}
	if err := got.Hangup(ctx); err != nil {
		return fmt.Errorf("callee BYE: %w", err)
	}
	select {
	case <-out.Ended():
	case <-ctx.Done():
		return errors.New("caller never got the BYE")
	}
	return nil
}

func run() error {
	jar, _ := cookiejar.New(nil)
	c := &client{c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	if err := c.do("POST", "/api/v1/auth/login", map[string]string{"username": *admin, "password": *password}, nil, 204); err != nil {
		return err
	}
	type reg struct {
		d    device
		p    *sipua.Phone
		node string
	}
	var regs []reg
	byNode := map[string][]reg{}
	for range *phones {
		d, err := c.newDevice()
		if err != nil {
			return err
		}
		p, err := sipua.New(sipua.Options{User: d.user, Password: d.secret, Domain: *domain, Proxy: *edge, Listen: "0.0.0.0:0", ContactHost: os.Getenv("HELLO_LAB_CONTACT_HOST")})
		if err != nil {
			return err
		}
		defer p.Close()
		start := time.Now()
		if err := register(p); err != nil {
			return fmt.Errorf("%s: %w", d.user, err)
		}
		node := c.node(d.user)
		fmt.Printf("REGISTER %s via %s: 200 in %v -> %s\n", d.user, *edge, time.Since(start).Round(time.Millisecond), node)
		r := reg{d, p, strings.Fields(node)[0]}
		regs = append(regs, r)
		byNode[r.node] = append(byNode[r.node], r)
	}
	for n, rs := range byNode {
		fmt.Printf("node %s holds %d of %d bindings\n", n, len(rs), len(regs))
	}
	if !*calls || len(regs) < 2 {
		return nil
	}
	// Caller and callee on different nodes when both nodes hold bindings.
	a, b := regs[0], regs[1]
	for _, r := range regs[1:] {
		if r.node != a.node {
			b = r
			break
		}
	}
	for _, byCaller := range []bool{true, false} {
		side := map[bool]string{true: "caller", false: "callee"}[byCaller]
		start := time.Now()
		if err := call(a.p, b.p, b.d.ext, byCaller); err != nil {
			return fmt.Errorf("call %s (%s) -> %s (%s), %s hangs up: %w", a.d.user, a.node, b.d.user, b.node, side, err)
		}
		fmt.Printf("CALL %s (%s) -> %s (%s): answered, %s hung up, both sides ended (%v)\n", a.d.user, a.node, b.d.user, b.node, side, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("OK")
}
