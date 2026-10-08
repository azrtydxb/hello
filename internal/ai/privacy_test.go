package ai_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeDNS answers from a map the test changes between calls (rebinding)
// and records every dialled address.
type fakeDNS struct {
	mu     sync.Mutex
	hosts  map[string][]netip.Addr
	dialed []string
	real   string // the address every dial really goes to
}

func (d *fakeDNS) set(host string, addrs ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var as []netip.Addr
	for _, a := range addrs {
		as = append(as, netip.MustParseAddr(a))
	}
	d.hosts[host] = as
}

func (d *fakeDNS) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if as, ok := d.hosts[host]; ok {
		return as, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (d *fakeDNS) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, addr)
	d.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, d.real)
}

// llm is a minimal OpenAI-compatible endpoint answering {"answer":"ok"}.
func llm(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop",` +
			`"message":{"role":"assistant","content":"{\"answer\":\"ok\"}"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func cfgFor(url string) config.AIAgent {
	c := aifake.Config()
	c.BaseURL = url
	c.APIKey = "sk-NEVER-SHOWN-123"
	return c
}

// TestPrivacyGuard (spec S-2, S-3) fails if a public, mixed or rebinding
// host is dialled without HELLO_AI_ALLOW_PUBLIC_ENDPOINT, a private one or
// localhost is refused, an unresolvable host turns AI off, the opt-in does
// not open public endpoints, or a go-ai-sdk provider is built outside
// provider.go.
func TestPrivacyGuard(t *testing.T) {
	ctx := context.Background()
	srv := llm(t)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	url := func(host string) string { return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/v1" }

	newSvc := func(t *testing.T, c config.AIAgent, d *fakeDNS) *ai.Service {
		t.Helper()
		s, err := ai.NewWith(c, aifake.NewStore(), nil, prometheus.NewRegistry(), nil, ai.WithNet(ai.Options{Replica: "r"}, d.resolve, d.dial))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	dns := func() *fakeDNS {
		return &fakeDNS{hosts: map[string][]netip.Addr{}, real: srv.Listener.Addr().String()}
	}

	starts := []struct {
		name, host string
		addrs      []string
		allow      bool
		on         bool
	}{
		{"loopback literal", "127.0.0.1", nil, false, true},
		{"localhost", "localhost", nil, false, true},
		{"RFC 1918 name", "llm.lan", []string{"10.1.2.3", "192.168.1.5"}, false, true},
		{"RFC 6598", "llm.cgn", []string{"100.64.0.9"}, false, true},
		{"IPv6 ULA and link-local", "llm.v6", []string{"fd00::1", "fe80::1"}, false, true},
		{"public literal", "8.8.8.8", nil, false, false},
		{"public name", "api.example", []string{"203.0.113.7"}, false, false},
		{"mixed answer", "llm.mixed", []string{"10.0.0.1", "203.0.113.7"}, false, false},
		{"mapped public IPv6", "::ffff:8.8.8.8", nil, false, false},
		{"unresolvable stays on", "llm.nowhere", nil, false, true},
		{"public with opt-in", "api.example", []string{"203.0.113.7"}, true, true},
	}
	for _, c := range starts {
		t.Run(c.name, func(t *testing.T) {
			d := dns()
			if c.addrs != nil {
				d.set(c.host, c.addrs...)
			}
			cfg := cfgFor(url(c.host))
			cfg.AllowPublic = c.allow
			on, reason := newSvc(t, cfg, d).Enabled()
			if on != c.on {
				t.Fatalf("enabled = %v (%s), want %v", on, reason, c.on)
			}
			if !on && reason != ai.ReasonEndpointNotPrivate {
				t.Errorf("reason = %q", reason)
			}
		})
	}

	t.Run("rebinding to a public address fails the call undialled", func(t *testing.T) {
		d := dns()
		d.set("llm.lan", "10.0.0.5")
		s := newSvc(t, cfgFor(url("llm.lan")), d)
		if _, _, err := ai.Generate(ctx, s, ai.Call[struct {
			Answer string `json:"answer"`
		}]{Feature: "t", Prompt: "q"}); err != nil {
			t.Fatalf("private call: %v", err)
		}
		d.set("llm.lan", "10.0.0.5", "203.0.113.7")
		n := len(d.dialed)
		_, _, err := ai.Generate(ctx, s, ai.Call[struct {
			Answer string `json:"answer"`
		}]{Feature: "t", Prompt: "q"})
		if ai.CodeOf(err) != ai.CodeEndpointNotPublic {
			t.Fatalf("rebound call: %v", err)
		}
		if len(d.dialed) != n {
			t.Errorf("dialled %v after rebinding", d.dialed[n:])
		}
	})

	t.Run("opt-in dials public addresses", func(t *testing.T) {
		d := dns()
		d.set("api.example", "203.0.113.7")
		cfg := cfgFor(url("api.example"))
		cfg.AllowPublic = true
		s := newSvc(t, cfg, d)
		if _, _, err := ai.Generate(ctx, s, ai.Call[struct {
			Answer string `json:"answer"`
		}]{Feature: "t", Prompt: "q"}); err != nil {
			t.Fatal(err)
		}
		if len(d.dialed) == 0 || !strings.HasPrefix(d.dialed[0], "203.0.113.7:") {
			t.Errorf("dialled %v, want the checked address", d.dialed)
		}
	})

	t.Run("unresolvable host fails calls with provider_error", func(t *testing.T) {
		s := newSvc(t, cfgFor(url("llm.nowhere")), dns())
		_, _, err := ai.Generate(ctx, s, ai.Call[struct {
			Answer string `json:"answer"`
		}]{Feature: "t", Prompt: "q"})
		if ai.CodeOf(err) != ai.CodeProviderError {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("providers only in provider.go", func(t *testing.T) {
		root := filepath.Join("..", "..")
		err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if n := de.Name(); p != root && (strings.HasPrefix(n, ".") || n == "web" || n == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, filepath.Join("internal", "ai", "provider.go")) {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if bytes.Contains(src, []byte("go-ai-sdk/"+"providers/")) {
				t.Errorf("%s imports a go-ai-sdk provider; only internal/ai/provider.go may", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// TestAIEnablement (spec S-1) fails if AI turns on without both the base URL
// and the model, starts anything while off, or reports another reason; and
// (with TestAIMetrics) if the API key reaches the status, a log line or a
// metric.
func TestAIEnablement(t *testing.T) {
	cases := []struct {
		url, model string
		on         bool
		reason     string
	}{
		{"", "", false, config.AIReasonNotConfigured},
		{"http://127.0.0.1:1/v1", "", false, config.AIReasonIncompleteSetup},
		{"", "m", false, config.AIReasonIncompleteSetup},
		{"http://127.0.0.1:1/v1", "m", true, ""},
	}
	for _, c := range cases {
		cfg := aifake.Config()
		cfg.BaseURL, cfg.Model, cfg.APIKey = c.url, c.model, "sk-NEVER-SHOWN-123"
		var logs bytes.Buffer
		reg := prometheus.NewRegistry()
		st := aifake.NewStore()
		s, err := ai.New(cfg, st, nil, reg, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil {
			t.Fatal(err)
		}
		on, reason := s.Enabled()
		if on != c.on || reason != c.reason {
			t.Errorf("%q %q: enabled %v %q, want %v %q", c.url, c.model, on, reason, c.on, c.reason)
		}
		if got := testutil.ToFloat64(s.Metrics().Enabled()); (got == 1) != c.on {
			t.Errorf("hello_ai_enabled = %v", got)
		}
		status, err := s.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(fmt.Sprintf("%+v", status)), "sk-never") {
			t.Errorf("status carries the key: %+v", status)
		}
		if !on {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s.Run(ctx) // returns at once, starting nothing
			if _, err := s.Tasks.Start(context.Background(), "message", 1, "", nil); ai.CodeOf(err) != ai.CodeDisabled {
				t.Errorf("task started while off: %v", err)
			}
			if len(st.Tasks) != 0 {
				t.Error("a task row was written while off")
			}
		}
		if strings.Contains(logs.String(), "sk-NEVER") {
			t.Errorf("the key is logged: %s", logs.String())
		}
	}
	if _, err := ai.New(config.AIAgent{Provider: "other", BaseURL: "http://127.0.0.1/v1", Model: "m"}, aifake.NewStore(), nil, nil, nil); err == nil ||
		errors.Is(err, nil) {
		t.Error("an unknown provider is accepted")
	}
}
