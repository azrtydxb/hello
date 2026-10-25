package mailer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// memQueue is an in-memory queue whose Claim is atomic, as the database
// queue is: a notification is handed to one claimant at a time.
type memQueue struct {
	mu      sync.Mutex
	jobs    map[int64]*qjob
	results map[int64][]Result
	removed []string
	fanouts int
}

type qjob struct {
	Job
	status string
	due    time.Time
	remove string
}

func newMemQueue(jobs ...Job) *memQueue {
	q := &memQueue{jobs: map[int64]*qjob{}, results: map[int64][]Result{}}
	for _, j := range jobs {
		q.jobs[j.ID] = &qjob{Job: j, status: "pending"}
	}
	return q
}

func (q *memQueue) FanOut(context.Context, int) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fanouts++
	return 0, nil
}

func (q *memQueue) Claim(_ context.Context, limit int, _ time.Duration) ([]Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Job
	for id := int64(1); id <= int64(len(q.jobs)) && len(out) < limit; id++ {
		j := q.jobs[id]
		if j != nil && j.status == "pending" && !j.due.After(time.Now()) {
			j.status = "sending"
			j.Attempts++
			out = append(out, j.Job)
		}
	}
	return out, nil
}

func (q *memQueue) Finish(_ context.Context, id int64, r Result) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs[id]
	q.results[id] = append(q.results[id], r)
	switch {
	case r.Err == "":
		j.status = "sent"
		return j.remove, nil
	case r.Final:
		j.status = "failed"
	default:
		j.status, j.due = "pending", r.RetryAt
	}
	return "", nil
}

func (q *memQueue) status(id int64) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.jobs[id].status
}

type fakeObjects struct {
	mu      sync.Mutex
	data    []byte
	err     error
	removed []string
}

func (o *fakeObjects) Get(context.Context, string) ([]byte, error) { return o.data, o.err }
func (o *fakeObjects) Remove(_ context.Context, k string) error {
	o.mu.Lock()
	o.removed = append(o.removed, k)
	o.mu.Unlock()
	return nil
}

type sink struct {
	mu       sync.Mutex
	failLeft int
	sent     []string // recipients
	msgs     [][]byte
}

func (s *sink) deliver(_ context.Context, _ Settings, to string, msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failLeft != 0 {
		s.failLeft--
		return errors.New("451 try later")
	}
	s.sent = append(s.sent, to)
	s.msgs = append(s.msgs, msg)
	return nil
}

func settingsOK(context.Context) (Settings, bool, error) {
	return Settings{Host: "smtp.test", Port: 587, Security: SecuritySTARTTLS, From: "pbx@example.com"}, true, nil
}

func job(id int64) Job {
	return Job{ID: id, MessageID: id, To: "ann@example.com", Caller: "+3215123456", DurationMs: 65000,
		CreatedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC), Object: "box/1/m.wav", Attach: true, Queued: time.Now()}
}

func newWorker(q Queue, o Objects, s *sink, set SettingsFunc) *Worker {
	return New(Config{Queue: q, Objects: o, Settings: set, Deliver: s.deliver,
		Backoff: func(int) time.Duration { return 0 }, MaxAttempts: 3})
}

// TestWorkerRetries fails if a transient failure is not retried through the
// queue with backoff, a success is not recorded once, or a notification that
// keeps failing is not given up on at MaxAttempts.
func TestWorkerRetries(t *testing.T) {
	ctx := context.Background()
	t.Run("recovers", func(t *testing.T) {
		q, s := newMemQueue(job(1)), &sink{failLeft: 2}
		w := newWorker(q, &fakeObjects{data: []byte("RIFFxxxxWAVE")}, s, settingsOK)
		for range 3 {
			w.Process(ctx)
		}
		if q.status(1) != "sent" || len(s.sent) != 1 {
			t.Fatalf("status %s, sent %v", q.status(1), s.sent)
		}
		if rs := q.results[1]; len(rs) != 3 || rs[0].Err == "" || rs[0].Final || rs[0].RetryAt.IsZero() || rs[2].Err != "" {
			t.Fatalf("results = %+v", rs)
		}
	})
	t.Run("gives up", func(t *testing.T) {
		q, s := newMemQueue(job(1)), &sink{failLeft: -1}
		w := newWorker(q, &fakeObjects{data: []byte("RIFFxxxxWAVE")}, s, settingsOK)
		for range 5 {
			w.Process(ctx)
		}
		if q.status(1) != "failed" || len(q.results[1]) != 3 || !q.results[1][2].Final {
			t.Fatalf("status %s results %+v", q.status(1), q.results[1])
		}
	})
	t.Run("expired notification fails on its next attempt", func(t *testing.T) {
		j := job(1)
		j.Queued = time.Now().Add(-48 * time.Hour)
		q, s := newMemQueue(j), &sink{failLeft: -1}
		newWorker(q, nil, s, settingsOK).Process(ctx)
		if q.status(1) != "failed" {
			t.Fatalf("status %s", q.status(1))
		}
	})
	t.Run("recording fetch failure retries instead of dropping the attachment", func(t *testing.T) {
		q, s := newMemQueue(job(1)), &sink{}
		o := &fakeObjects{err: errors.New("minio down")}
		w := newWorker(q, o, s, settingsOK)
		w.Process(ctx)
		if q.status(1) != "pending" || len(s.sent) != 0 {
			t.Fatalf("status %s sent %v", q.status(1), s.sent)
		}
		w.Process(ctx)
		w.Process(ctx) // third and last attempt sends without the recording
		if q.status(1) != "sent" || bytes.Contains(s.msgs[0], []byte("attachment")) {
			t.Fatalf("status %s", q.status(1))
		}
	})
	t.Run("attachment follows the user's option", func(t *testing.T) {
		j := job(1)
		j.Attach = false
		q, s := newMemQueue(j), &sink{}
		newWorker(q, &fakeObjects{data: []byte("RIFFxxxxWAVE")}, s, settingsOK).Process(ctx)
		if bytes.Contains(s.msgs[0], []byte("attachment")) {
			t.Fatal("attached a recording the user turned off")
		}
	})
	t.Run("recording removed after a delete-after-send", func(t *testing.T) {
		q, s, o := newMemQueue(job(1)), &sink{}, &fakeObjects{data: []byte("RIFFxxxxWAVE")}
		q.jobs[1].remove = "box/1/m.wav"
		newWorker(q, o, s, settingsOK).Process(ctx)
		if len(o.removed) != 1 || o.removed[0] != "box/1/m.wav" {
			t.Fatalf("removed = %v", o.removed)
		}
	})
}

// TestWorkerNeedsSettings fails if the worker claims or sends while SMTP is
// not configured, or when reading the settings errors.
func TestWorkerNeedsSettings(t *testing.T) {
	q, s := newMemQueue(job(1)), &sink{}
	for _, set := range []SettingsFunc{
		func(context.Context) (Settings, bool, error) { return Settings{}, false, nil },
		func(context.Context) (Settings, bool, error) { return Settings{}, true, errors.New("db down") },
	} {
		newWorker(q, nil, s, set).Process(context.Background())
	}
	if q.status(1) != "pending" || len(s.sent) != 0 || q.fanouts != 0 {
		t.Fatalf("status %s sent %v fanouts %d", q.status(1), s.sent, q.fanouts)
	}
}

// TestExactlyOnceAcrossNodes fails if two workers sharing a queue (two
// nodes) send any notification twice or lose one.
func TestExactlyOnceAcrossNodes(t *testing.T) {
	var jobs []Job
	for i := int64(1); i <= 60; i++ {
		jobs = append(jobs, job(i))
	}
	q, s := newMemQueue(jobs...), &sink{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		w := newWorker(q, &fakeObjects{}, s, settingsOK)
		go func() {
			defer wg.Done()
			for range 10 {
				w.Process(context.Background())
			}
		}()
	}
	wg.Wait()
	if len(s.sent) != 60 {
		t.Fatalf("sent %d emails for 60 notifications", len(s.sent))
	}
	for id := int64(1); id <= 60; id++ {
		if n := len(q.results[id]); n != 1 {
			t.Fatalf("notification %d finished %d times", id, n)
		}
	}
}

// TestRunStops fails if Run does not return when its context ends.
func TestRunStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := New(Config{Queue: newMemQueue(), Settings: settingsOK, PollEvery: time.Millisecond})
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestBackoffGrows(t *testing.T) {
	if defaultBackoff(1) != 30*time.Second || defaultBackoff(2) != time.Minute || defaultBackoff(20) != time.Hour {
		t.Fatalf("backoff %v %v %v", defaultBackoff(1), defaultBackoff(2), defaultBackoff(20))
	}
}

func parse(t *testing.T, raw []byte) (*mail.Message, []string, [][]byte) {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		t.Fatalf("content type %q: %v", m.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types []string
	var bodies [][]byte
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		types = append(types, p.Header.Get("Content-Type"))
		bodies = append(bodies, b)
	}
	return m, types, bodies
}

// TestBuildMessage fails if the email lacks the caller, time or duration,
// attaches nothing or the wrong type when asked, or lets a hostile caller ID
// or address inject headers.
func TestBuildMessage(t *testing.T) {
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVE"), bytes.Repeat([]byte{1}, 200)...)
	raw, err := BuildMessage("pbx@example.com", job(1), wav)
	if err != nil {
		t.Fatal(err)
	}
	m, types, bodies := parse(t, raw)
	if len(types) != 2 || !strings.HasPrefix(types[1], "audio/wav") {
		t.Fatalf("parts = %v", types)
	}
	dec, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(bodies[1])))
	if !bytes.Equal(dec, wav) {
		t.Fatal("attachment differs from the recording")
	}
	body := string(bodies[0])
	for _, want := range []string{"+3215123456", "1:05", "01 Mar 2026 10:00:00"} {
		if !strings.Contains(body+m.Header.Get("Subject"), want) {
			t.Errorf("email lacks %q:\n%s", want, body)
		}
	}
	_, types, _ = parse(t, mustBuild(t, job(1), []byte("ID3\x03\x00")))
	if !strings.HasPrefix(types[1], "audio/mpeg") {
		t.Errorf("mp3 sent as %q", types[1])
	}
	_, types, _ = parse(t, mustBuild(t, job(1), nil))
	if len(types) != 1 {
		t.Errorf("no recording but %d parts", len(types))
	}
	evil := job(1)
	evil.Caller = "x\r\nBcc: victim@example.com"
	raw = mustBuild(t, evil, nil)
	m, _, _ = parse(t, raw)
	if m.Header.Get("Bcc") != "" || strings.Contains(strings.SplitN(string(raw), "\r\n\r\n", 2)[0], "\r\nBcc:") {
		t.Fatal("caller ID injected a header")
	}
	for _, from := range []string{"", "a@b.c\r\nBcc: x@y.z"} {
		if _, err := BuildMessage(from, job(1), nil); err == nil {
			t.Errorf("From %q accepted", from)
		}
	}
	bad := job(1)
	bad.To = "a@b.c\r\nBcc: x@y.z"
	if _, err := BuildMessage("pbx@example.com", bad, nil); err == nil {
		t.Error("To with a line break accepted")
	}
}

func mustBuild(t *testing.T, j Job, audio []byte) []byte {
	t.Helper()
	b, err := BuildMessage("pbx@example.com", j, audio)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSettingsValidate(t *testing.T) {
	ok := Settings{Host: "smtp.example.com", Port: 587, Security: SecuritySTARTTLS, From: "pbx@example.com"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Settings){
		"no host":        func(s *Settings) { s.Host = "" },
		"host with path": func(s *Settings) { s.Host = "a/b" },
		"port 0":         func(s *Settings) { s.Port = 0 },
		"port high":      func(s *Settings) { s.Port = 70000 },
		"security":       func(s *Settings) { s.Security = "ssl" },
		"from":           func(s *Settings) { s.From = "not an address" },
		"from name":      func(s *Settings) { s.From = "PBX <pbx@example.com>" },
		"user newline":   func(s *Settings) { s.Username = "a\nb" },
	} {
		s := ok
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// fakeSMTP is a minimal SMTP server for one connection at a time.
type fakeSMTP struct {
	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool
	mu       sync.Mutex
	auth     string
	rcpt     []string
	data     string
	tlsUsed  bool
}

func newFakeSMTP(t *testing.T, implicit bool) *fakeSMTP {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	leaf, _ := x509.ParseCertificate(der)
	pool.AddCert(leaf)
	tlsHook = func(c *tls.Config) { c.RootCAs = pool }
	t.Cleanup(func() { tlsHook = nil })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, tlsCfg: &tls.Config{Certificates: []tls.Certificate{cert}}, implicit: implicit}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	if f.implicit {
		c = tls.Server(c, f.tlsCfg)
		f.mu.Lock()
		f.tlsUsed = true
		f.mu.Unlock()
	}
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			_, _ = io.WriteString(c, "250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
		case up == "STARTTLS":
			say("220 go")
			c = tls.Server(c, f.tlsCfg)
			r = bufio.NewReader(c)
			f.mu.Lock()
			f.tlsUsed = true
			f.mu.Unlock()
		case strings.HasPrefix(up, "AUTH PLAIN"):
			f.mu.Lock()
			f.auth = line
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(up, "MAIL"):
			say("250 ok")
		case strings.HasPrefix(up, "RCPT"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, line)
			f.mu.Unlock()
			say("250 ok")
		case up == "DATA":
			say("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// TestDeliver fails if a message is not handed to an SMTP server with the
// configured transport security, if STARTTLS or implicit TLS is skipped, or
// if credentials are offered without it.
func TestDeliver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg := []byte("Subject: hi\r\n\r\nbody\r\n")
	for _, mode := range []string{SecuritySTARTTLS, SecurityTLS} {
		f := newFakeSMTP(t, mode == SecurityTLS)
		err := Deliver(ctx, Settings{Host: "localhost", Port: f.port(), Security: mode, Username: "u", Password: "pw", From: "pbx@example.com"},
			"ann@example.com", msg)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		f.mu.Lock()
		ok := f.tlsUsed && strings.Contains(f.data, "body") && len(f.rcpt) == 1 && strings.Contains(f.rcpt[0], "ann@example.com") && f.auth != ""
		f.mu.Unlock()
		if !ok {
			t.Errorf("%s: tls %v data %q rcpt %v auth %q", mode, f.tlsUsed, f.data, f.rcpt, f.auth)
		}
	}
	// An untrusted certificate is refused, not skipped.
	f := newFakeSMTP(t, true)
	tlsHook = nil
	if err := Deliver(ctx, Settings{Host: "localhost", Port: f.port(), Security: SecurityTLS, From: "pbx@example.com"}, "ann@example.com", msg); err == nil {
		t.Error("untrusted certificate accepted")
	}
	if err := Deliver(ctx, Settings{Host: "localhost", Port: f.port(), Security: SecurityNone, From: "pbx@example.com"}, "bad address", msg); err == nil {
		t.Error("bad recipient accepted")
	}
}
