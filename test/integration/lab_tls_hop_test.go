package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// metricSum adds up the samples of one metric family whose line matches the
// label text (a substring such as `transport="tls"`) in a Prometheus text
// exposition.
func metricSum(body, name, label string) float64 {
	var sum float64
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, name+"{") || !strings.Contains(l, label) {
			continue
		}
		f := strings.Fields(l)
		if v, err := strconv.ParseFloat(f[len(f)-1], 64); err == nil {
			sum += v
		}
	}
	return sum
}

// helloTLSRequests is how many requests the SIP nodes counted as arrived over
// TLS (hello_sip_requests_by_wire_total{transport="tls"}).
func helloTLSRequests(t *testing.T) float64 {
	t.Helper()
	var sum float64
	for _, base := range labNodeHTTP {
		resp, err := http.Get(base + "/metrics")
		if err != nil {
			t.Fatalf("scrape %s: %v", base, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		sum += metricSum(string(b), "hello_sip_requests_by_wire_total", `transport="tls"`)
	}
	return sum
}

// kamailioHelloTLSRequests is how many requests Kamailio received on its
// hello_tls socket: what Hello sent back over the hop.
func kamailioHelloTLSRequests(t *testing.T) float64 {
	t.Helper()
	out, err := compose("exec", "-T", "kamailio", "wget", "-qO-", "http://10.89.53.10:9090/metrics").CombinedOutput()
	if err != nil {
		t.Fatalf("kamailio metrics: %v\n%s", err, out)
	}
	return metricSum(string(out), "kamailio_edge_requests_total", `socket="hello_tls"`)
}

var dispatcherEntry = regexp.MustCompile(`URI:\s*(\S+)\s+FLAGS:\s*(\w+)`)

// dispatcherFlagsByURI is the flags of each dispatcher destination by URI
// (kamcmd dispatcher.list): A is active, I inactive, P probing.
func dispatcherFlagsByURI(t *testing.T) map[string]string {
	t.Helper()
	out, err := compose("exec", "-T", "kamailio", "kamcmd", "dispatcher.list").CombinedOutput()
	if err != nil {
		t.Fatalf("kamcmd dispatcher.list: %v\n%s", err, out)
	}
	flags := map[string]string{}
	for _, m := range dispatcherEntry.FindAllStringSubmatch(string(out), -1) {
		flags[m[1]] = m[2]
	}
	return flags
}

// readRequest reads one SIP message from the connection and returns its first
// line and headers.
func (s *sipConn) readRequest() (string, textproto.MIMEHeader) {
	s.t.Helper()
	_ = s.c.SetDeadline(time.Now().Add(20 * time.Second))
	tp := textproto.NewReader(s.br)
	line, err := tp.ReadLine()
	if err != nil {
		s.t.Fatalf("read request line: %v", err)
	}
	h, err := tp.ReadMIMEHeader()
	if err != nil {
		s.t.Fatalf("read request headers: %v", err)
	}
	if n, _ := strconv.Atoi(h.Get("Content-Length")); n > 0 {
		_, _ = io.CopyN(io.Discard, s.br, int64(n))
	}
	return line, h
}

// TestLabTLSHop checks the TLS hop from Kamailio to hello-sip (S-12) on the
// lab, where it is on at both ends: Kamailio probes the nodes' TLS addresses
// (dispatcher set 2) and marks one down when it stops answering; a phone that
// registers over TLS reaches the node over its TLS listener (counted by the
// node); and Hello's request to that phone (a call) comes back to Kamailio's
// hello_tls socket (counted by Kamailio) and reaches the phone's connection.
func TestLabTLSHop(t *testing.T) {
	lc := newLabClient(t)
	pool := labSIPCA(t)
	const (
		tls1 = "sip:10.89.53.11:5061;transport=tls"
		tls2 = "sip:10.89.53.12:5061;transport=tls"
	)

	t.Run("the TLS addresses are probed and follow their node", func(t *testing.T) {
		eventually(t, 40*time.Second, "both TLS destinations active and probing", func() error {
			f := dispatcherFlagsByURI(t)
			for _, u := range []string{tls1, tls2} {
				if !strings.HasPrefix(f[u], "A") || !strings.Contains(f[u], "P") {
					return fmt.Errorf("destination %s has flags %q, want active and probing (have %v)", u, f[u], f)
				}
			}
			return nil
		})
		// Stopping a node stops its TLS answers: its TLS destination goes
		// inactive from the TLS probes alone (set 2 is probed on its own).
		labCompose(t, "stop", "hello-sip-2")
		defer func() {
			labCompose(t, "start", "hello-sip-2")
			waitLabServing(t)
		}()
		eventually(t, 40*time.Second, "the stopped node's TLS destination inactive", func() error {
			f := dispatcherFlagsByURI(t)
			if !strings.HasPrefix(f[tls2], "I") {
				return fmt.Errorf("destination %s has flags %q, want inactive (have %v)", tls2, f[tls2], f)
			}
			if !strings.HasPrefix(f[tls1], "A") {
				return fmt.Errorf("the running node's destination %s has flags %q, want active", tls1, f[tls1])
			}
			return nil
		})
	})

	t.Run("a TLS phone registers and is called over the hop", func(t *testing.T) {
		waitLabServing(t)
		eventually(t, 40*time.Second, "both TLS destinations active again", func() error {
			f := dispatcherFlagsByURI(t)
			if !strings.HasPrefix(f[tls1], "A") || !strings.HasPrefix(f[tls2], "A") {
				return fmt.Errorf("flags %v", f)
			}
			return nil
		})
		ext := lc.extension("tls")[0]
		remember(ext.Secret)
		caller := lc.devices("desk")[0]
		beforeNode, beforeEdge := helloTLSRequests(t), kamailioHelloTLSRequests(t)

		c, err := dialTLS(t, &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		registerOverTLS(t, c, ext)
		if got := helloTLSRequests(t); got < beforeNode+2 {
			t.Fatalf("the nodes counted %v requests over TLS after a TLS REGISTER (before %v), want at least 2 more: the REGISTER did not reach hello-sip on its TLS listener", got, beforeNode)
		}

		// A call to the phone: Hello's INVITE goes to hello_tls, then down
		// the phone's own TLS connection.
		a := phone(t, caller, labSIP1)
		register(t, a)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		dialDone := make(chan string, 1)
		go func() {
			out, err := a.Dial(ctx, ext.Extension, sdpOffer)
			if err != nil {
				dialDone <- "dial error: " + err.Error()
				return
			}
			dialDone <- fmt.Sprintf("dial answered %d", out.Status)
		}()
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			// What the call did and what the edge and the nodes saw, for the CI log.
			select {
			case r := <-dialDone:
				t.Logf("the caller's dial: %s", r)
			case <-time.After(20 * time.Second):
				t.Log("the caller's dial is still running")
			}
			time.Sleep(3 * time.Second) // the CDR is written after the call
			var recent struct{ Items []labCDR }
			if err := lc.do("GET", "/api/v1/cdrs?limit=4", nil, &recent, 200); err == nil {
				for _, c := range recent.Items {
					t.Logf("CDR %d %s -> %s status %d trace: %q", c.ID, c.Source, c.Destination, c.FinalStatus, lc.cdrTrace(c.ID))
				}
			}
			for _, svc := range []string{"kamailio", "hello-sip-1", "hello-sip-2"} {
				out, _ := compose("logs", "--no-color", "--tail", "120", svc).CombinedOutput()
				var keep []string
				for _, l := range strings.Split(string(out), "\n") {
					if svc == "kamailio" && !strings.Contains(l, "INFO: tls") && !strings.Contains(l, "INFO: <core>") || svc != "kamailio" && !strings.Contains(l, "snapshot") {
						keep = append(keep, l)
					}
				}
				t.Logf("=== %s (tail)\n%s", svc, strings.Join(keep, "\n"))
			}
		})
		line, h := c.readRequest()
		if !strings.HasPrefix(line, "INVITE ") {
			t.Fatalf("the TLS phone received %q, want an INVITE", line)
		}
		if len(h["Record-Route"]) == 0 {
			t.Errorf("the INVITE carries no Record-Route: it did not pass Kamailio's dialog routing (%v)", h)
		}
		if got := kamailioHelloTLSRequests(t); got <= beforeEdge {
			t.Fatalf("Kamailio counted %v requests on hello_tls after Hello's INVITE (before %v): Hello's request did not come back over the hop", got, beforeEdge)
		}
	})
}

// registerOverTLS registers ext's device on c with digest and fails the test
// on anything but 200.
func registerOverTLS(t *testing.T, c *sipConn, d labDevice) {
	t.Helper()
	callID := randDigits(10) + "@lab-hop"
	status, h := c.roundTrip(registerTLS(d.User, callID, 1, ""))
	if !strings.Contains(status, " 401 ") {
		t.Fatalf("unauthenticated REGISTER over TLS: %q, want 401", status)
	}
	cred := labDigest(t, h["Www-Authenticate"], "REGISTER", "sip:"+labDomain, d.User, d.Secret)
	if status, _ = c.roundTrip(registerTLS(d.User, callID, 2, cred)); !strings.Contains(status, " 200 ") {
		t.Fatalf("authenticated REGISTER over TLS: %q, want 200", status)
	}
}
