package kindha

import (
	"bytes"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A deliberately small SIP toolkit over raw UDP: the test plays the phones and
// the Hello nodes itself, so it controls every header it sends and reads every
// header Kamailio puts on the wire (Path, Record-Route, Via), which is what a
// failover has to keep valid.

type hdr struct{ k, v string }

type msg struct {
	first string
	h     []hdr
	body  []byte
	src   *net.UDPAddr
}

var compact = map[string]string{"v": "Via", "f": "From", "t": "To", "i": "Call-ID", "m": "Contact", "l": "Content-Length", "c": "Content-Type"}

func parseMsg(b []byte, src *net.UDPAddr) (*msg, bool) {
	head, body, _ := bytes.Cut(b, []byte("\r\n\r\n"))
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "SIP/2.0") {
		return nil, false
	}
	m := &msg{first: lines[0], body: body, src: src}
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		if (l[0] == ' ' || l[0] == '\t') && len(m.h) > 0 {
			m.h[len(m.h)-1].v += " " + strings.TrimSpace(l)
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return nil, false
		}
		k = strings.TrimSpace(k)
		if long, c := compact[strings.ToLower(k)]; c {
			k = long
		}
		m.h = append(m.h, hdr{k, strings.TrimSpace(v)})
	}
	return m, true
}

func (m *msg) isReq() bool { return !strings.HasPrefix(m.first, "SIP/2.0") }

func (m *msg) method() string {
	if m.isReq() {
		return strings.SplitN(m.first, " ", 2)[0]
	}
	_, cs, _ := strings.Cut(m.get("CSeq"), " ")
	return strings.TrimSpace(cs)
}

func (m *msg) status() int {
	if m.isReq() {
		return 0
	}
	f := strings.Fields(m.first)
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

func (m *msg) ruri() string {
	f := strings.Fields(m.first)
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

func (m *msg) all(name string) []string {
	var out []string
	for _, h := range m.h {
		if strings.EqualFold(h.k, name) {
			out = append(out, splitList(name, h.v)...)
		}
	}
	return out
}

func (m *msg) get(name string) string {
	if v := m.all(name); len(v) > 0 {
		return v[0]
	}
	return ""
}

// splitList splits a header that may carry several comma-separated values
// (Via, Record-Route, Route, Path); other headers are one value.
func splitList(name, v string) []string {
	switch strings.ToLower(name) {
	case "via", "record-route", "route", "path":
		var out []string
		depth := 0
		start := 0
		for i, r := range v {
			switch r {
			case '<':
				depth++
			case '>':
				depth--
			case ',':
				if depth == 0 {
					out = append(out, strings.TrimSpace(v[start:i]))
					start = i + 1
				}
			}
		}
		return append(out, strings.TrimSpace(v[start:]))
	}
	return []string{v}
}

func (m *msg) bytes() []byte {
	var b bytes.Buffer
	b.WriteString(m.first + "\r\n")
	for _, h := range m.h {
		if strings.EqualFold(h.k, "Content-Length") {
			continue
		}
		b.WriteString(h.k + ": " + h.v + "\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(m.body))
	b.Write(m.body)
	return b.Bytes()
}

func (m *msg) String() string {
	return fmt.Sprintf("[from %v] %s | %s", m.src, m.first, summarise(m))
}

func summarise(m *msg) string {
	var parts []string
	for _, n := range []string{"Via", "Route", "Record-Route", "Path", "Contact"} {
		if v := m.all(n); len(v) > 0 {
			parts = append(parts, n+"="+strings.Join(v, " ; "))
		}
	}
	return strings.Join(parts, " | ")
}

var (
	reTag    = regexp.MustCompile(`;\s*tag=([^;>\s]+)`)
	reBranch = regexp.MustCompile(`;\s*branch=([^;,\s]+)`)
	reHP     = regexp.MustCompile(`sip:(?:[^@>;]*@)?(\[[^\]]+\]|[^:;>\s]+)(?::(\d+))?`)
	reAngle  = regexp.MustCompile(`<([^>]*)>`)
)

func tagOf(v string) string {
	if m := reTag.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

func branchOf(via string) string {
	if m := reBranch.FindStringSubmatch(via); m != nil {
		return m[1]
	}
	return ""
}

// hostPort returns the host and port (default 5060) of a SIP URI or of a
// name-addr that contains one.
func hostPort(v string) (string, int) {
	m := reHP.FindStringSubmatch(v)
	if m == nil {
		return "", 0
	}
	p := 5060
	if m[2] != "" {
		p, _ = strconv.Atoi(m[2])
	}
	return m[1], p
}

func uriOf(v string) string {
	if m := reAngle.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	v, _, _ = strings.Cut(v, ";")
	return strings.TrimSpace(v)
}

func rnd(n int) string {
	b := make([]byte, n)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// ua is one UDP SIP endpoint. Its reader answers what needs no test logic
// (OPTIONS) and hands requests to onRequest, and responses to the transaction
// that is waiting for them.
type ua struct {
	t    *testing.T
	name string
	conn *net.UDPConn
	host string // the address peers reach this endpoint at (Via, Contact)
	port int

	mu        sync.Mutex
	pending   map[string]chan *msg // by our Via branch
	onRequest func(*msg)
	log       []string
	closed    bool
	options   map[string]int // OPTIONS answered, by source address (the dispatcher's probes)
}

func newUA(t *testing.T, name, host string, port int) *ua {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		t.Fatalf("%s: listen: %v", name, err)
	}
	u := &ua{t: t, name: name, conn: conn, host: host, port: conn.LocalAddr().(*net.UDPAddr).Port, pending: map[string]chan *msg{}, options: map[string]int{}}
	go u.read()
	t.Cleanup(u.close)
	return u
}

func (u *ua) close() {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
	_ = u.conn.Close()
}

func (u *ua) addr() string { return net.JoinHostPort(u.host, strconv.Itoa(u.port)) }

func (u *ua) record(dir string, m *msg) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.log = append(u.log, fmt.Sprintf("%s %s %s %s", time.Now().Format("15:04:05.000"), u.name, dir, m))
	if len(u.log) > 400 {
		u.log = u.log[len(u.log)-400:]
	}
}

func (u *ua) dump() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, l := range u.log {
		u.t.Log(l)
	}
}

func (u *ua) read() {
	buf := make([]byte, 65535)
	for {
		n, src, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		m, ok := parseMsg(append([]byte(nil), buf[:n]...), src)
		if !ok {
			continue
		}
		u.record("<-", m)
		if !m.isReq() {
			via := m.get("Via")
			u.mu.Lock()
			ch := u.pending[branchOf(via)]
			u.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
			continue
		}
		if m.method() == "OPTIONS" {
			u.mu.Lock()
			u.options[src.IP.String()]++
			u.mu.Unlock()
			u.reply(m, 200, "OK", "", nil, nil)
			continue
		}
		u.mu.Lock()
		h := u.onRequest
		u.mu.Unlock()
		if h != nil {
			go h(m)
		}
	}
}

func (u *ua) sendTo(dst string, m *msg) {
	a, err := net.ResolveUDPAddr("udp", dst)
	if err != nil {
		u.t.Errorf("%s: resolve %s: %v", u.name, dst, err)
		return
	}
	u.record("->", &msg{first: m.first, h: m.h, src: a})
	if _, err := u.conn.WriteToUDP(m.bytes(), a); err != nil {
		u.mu.Lock()
		closed := u.closed
		u.mu.Unlock()
		if !closed {
			u.t.Errorf("%s: send to %s: %v", u.name, dst, err)
		}
	}
}

// reply answers req at the address it came from: the datagram's source, as
// Hello does, so it works whichever instance relayed it.
func (u *ua) reply(req *msg, code int, reason, toTag string, extra []hdr, body []byte) {
	r := &msg{first: fmt.Sprintf("SIP/2.0 %d %s", code, reason), body: body}
	for _, h := range req.h {
		switch strings.ToLower(h.k) {
		case "via", "from", "call-id", "cseq":
			r.h = append(r.h, h)
		case "record-route":
			if code >= 100 && code < 300 && req.method() == "INVITE" {
				r.h = append(r.h, h)
			}
		case "to":
			if toTag != "" && tagOf(h.v) == "" {
				h.v += ";tag=" + toTag
			}
			r.h = append(r.h, h)
		}
	}
	r.h = append(r.h, extra...)
	if len(body) > 0 {
		r.h = append(r.h, hdr{"Content-Type", "application/sdp"})
	}
	u.sendTo(req.src.String(), r)
}

func (u *ua) sdp() []byte {
	return []byte("v=0\r\no=- 1 1 IN IP4 " + u.host + "\r\ns=-\r\nc=IN IP4 " + u.host + "\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n")
}

// request builds a request with a fresh Via branch.
func (u *ua) request(method, ruri string, routes []string, from, to, callID string, cseq int, contact string, body []byte) (*msg, string) {
	branch := "z9hG4bK" + rnd(6)
	m := &msg{first: method + " " + ruri + " SIP/2.0", body: body}
	m.h = append(m.h, hdr{"Via", fmt.Sprintf("SIP/2.0/UDP %s;branch=%s;rport", u.addr(), branch)})
	m.h = append(m.h, hdr{"Max-Forwards", "70"})
	for _, r := range routes {
		m.h = append(m.h, hdr{"Route", r})
	}
	m.h = append(m.h, hdr{"From", from}, hdr{"To", to}, hdr{"Call-ID", callID}, hdr{"CSeq", fmt.Sprintf("%d %s", cseq, method)})
	if contact != "" {
		m.h = append(m.h, hdr{"Contact", contact})
	}
	m.h = append(m.h, hdr{"User-Agent", "kindha"})
	if len(body) > 0 {
		m.h = append(m.h, hdr{"Content-Type", "application/sdp"})
	}
	return m, branch
}

// transact sends req to dst and waits for its final response, retransmitting
// as RFC 3261 does for UDP (T1 500ms, doubling to T2 4s), so a request sent
// while the VIP is between nodes is delivered once it settles.
func (u *ua) transact(dst string, req *msg, branch string, timeout time.Duration) (*msg, error) {
	ch := make(chan *msg, 16)
	u.mu.Lock()
	u.pending[branch] = ch
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		delete(u.pending, branch)
		u.mu.Unlock()
	}()
	deadline := time.Now().Add(timeout)
	interval := 500 * time.Millisecond
	provisional := false
	for {
		if !provisional || req.method() != "INVITE" {
			u.sendTo(dst, req)
		}
		wait := time.NewTimer(min(interval, time.Until(deadline)))
	inner:
		for {
			select {
			case r := <-ch:
				if r.status() >= 200 {
					wait.Stop()
					return r, nil
				}
				provisional = true
			case <-wait.C:
				break inner
			}
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("%s: no final response to %s within %s", u.name, req.first, timeout)
		}
		interval = min(interval*2, 4*time.Second)
	}
}

// optionsFrom is how many OPTIONS this endpoint has answered from ip.
func (u *ua) optionsFrom(ip string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.options[ip]
}
