package mailer

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime"
	"mime/multipart"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeQueue records what the worker does to the rows.
type fakeQueue struct {
	mu    sync.Mutex
	jobs  []Job
	marks map[int64]string
}

func newFakeQueue(jobs ...Job) *fakeQueue {
	return &fakeQueue{jobs: jobs, marks: map[int64]string{}}
}

func (q *fakeQueue) PendingEmails(context.Context, int) ([]Job, error) { return q.jobs, nil }

func (q *fakeQueue) MarkEmail(_ context.Context, id int64, status string) error {
	q.mu.Lock()
	q.marks[id] = status
	q.mu.Unlock()
	return nil
}

func (q *fakeQueue) status(id int64) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.marks[id]
}

// fakeSender fails the first n sends, then succeeds (n < 0: always fails).
type fakeSender struct {
	mu       sync.Mutex
	failLeft int
	sends    int
	lastTo   string
	lastMsg  []byte
}

func (s *fakeSender) Send(_ context.Context, from, to string, msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	s.lastTo, s.lastMsg = to, msg
	if s.failLeft != 0 {
		s.failLeft--
		return errors.New("smtp: 454 temporary failure")
	}
	return nil
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

type fakeObjects struct{ wav []byte }

func (f fakeObjects) Get(context.Context, string) ([]byte, error) { return f.wav, nil }

var testWAV = append([]byte("RIFF"), append([]byte{0x00, 0x00, 0x00, 0x00}, []byte("WAVEfmt ")...)...)

func testWorker(q *fakeQueue, s Sender) *Worker {
	return New(Config{
		From: "voicemail@hello.test", Queue: q, Objects: fakeObjects{testWAV}, Sender: s,
		Backoff: func(int) time.Duration { return time.Millisecond },
	})
}

func TestMailerRetries(t *testing.T) {
	ctx := context.Background()

	// Two transient failures, then success: sent after exactly three sends.
	sender := &fakeSender{failLeft: 2}
	q := newFakeQueue(Job{MessageID: 7, To: "sales@hello.test", Caller: "101", DurationMs: 34_000,
		CreatedAt: time.Now(), Object: "box/1/1-callid.wav"})
	testWorker(q, sender).process(ctx)
	if got := q.status(7); got != StatusSent {
		t.Fatalf("email marked %q, want sent", got)
	}
	if n := sender.count(); n != 3 {
		t.Fatalf("send attempts = %d, want 3 (retry twice with backoff)", n)
	}

	// A permanently failing server: failed after the third attempt, and the
	// row keeps its failed status (the voicemail itself is unaffected).
	sender = &fakeSender{failLeft: -1}
	q = newFakeQueue(Job{MessageID: 8, To: "sales@hello.test", Caller: "102", Object: "box/1/2-c.wav"})
	testWorker(q, sender).process(ctx)
	if got := q.status(8); got != StatusFailed {
		t.Fatalf("email marked %q, want failed", got)
	}
	if n := sender.count(); n != 3 {
		t.Fatalf("send attempts = %d, want exactly 3 retries", n)
	}

	// A failed send keeps the queue moving: the next job still goes out.
	sender = &fakeSender{}
	q = newFakeQueue(
		Job{MessageID: 1, To: "a@hello.test", Object: "o1"},
		Job{MessageID: 2, To: "b@hello.test", Object: "o2"},
	)
	testWorker(q, sender).process(ctx)
	if got := q.status(1) + q.status(2); got != StatusSent+StatusSent {
		t.Fatalf("statuses %q, want both sent", got)
	}
}

// TestMailerDisabled fails if a worker without SMTP configuration polls the
// queue or sends anything.
func TestMailerDisabled(t *testing.T) {
	q := &fakeQueue{jobs: []Job{{MessageID: 3, To: "x@hello.test", Object: "o"}}}
	w := New(Config{From: "voicemail@hello.test", Queue: q, Sender: nil})
	if w.Enabled() {
		t.Fatal("a worker without a sender reports enabled")
	}
	// Run returns at once and never touches the queue.
	done := make(chan struct{})
	go func() {
		w.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a disabled worker's Run does not return")
	}
	// An explicit pass marks nothing.
	w.process(context.Background())
	if len(q.marks) != 0 {
		t.Fatalf("disabled worker marked %v", q.marks)
	}
	// No From address disables it too, even with a sender.
	if New(Config{Queue: q, Sender: &fakeSender{}}).Enabled() {
		t.Fatal("a worker without a From address reports enabled")
	}
}

func TestBuildMessage(t *testing.T) {
	j := Job{MessageID: 9, To: "sales@hello.test", Caller: "101", DurationMs: 34_500,
		CreatedAt: time.Date(2026, 10, 4, 9, 30, 0, 0, time.UTC), Object: "box/1/x.wav"}
	msg, err := BuildMessage("voicemail@hello.test", j, testWAV)
	if err != nil {
		t.Fatal(err)
	}
	text := string(msg)
	if !strings.Contains(text, "From: voicemail@hello.test\r\n") || !strings.Contains(text, "To: sales@hello.test\r\n") {
		t.Fatalf("headers missing: %q", text)
	}
	if !strings.Contains(text, "Subject: New voicemail from 101") {
		t.Fatalf("subject missing: %q", text)
	}
	if !strings.Contains(text, "multipart/mixed") {
		t.Fatalf("not multipart: %q", text)
	}
	// The attachment decodes back to the WAV.
	m := regexp.MustCompile(`Content-Type: multipart/mixed; boundary=(\S+)`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no multipart content type: %q", text)
	}
	boundary, err := strconv.Unquote(m[1])
	if err != nil {
		boundary = m[1]
	}
	mr := multipart.NewReader(bytes.NewReader(msg), boundary)
	part, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(part)
	if !strings.Contains(body, "You have a new voicemail from 101") || !strings.Contains(body, "0:35") {
		t.Fatalf("summary missing: %q", body)
	}
	part, err = mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if mt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type")); mt != "audio/wav" {
		t.Fatalf("attachment type %q", mt)
	}
	b64, _ := readAll(part)
	got, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(b64, "\r", ""), "\n", ""))
	if err != nil || !bytes.Equal(got, testWAV) {
		t.Fatalf("attachment does not round-trip: %v", err)
	}
	if _, err := mr.NextPart(); err == nil {
		t.Fatal("unexpected third part")
	}
}

// TestBuildMessageNoAttachment fails if a message whose audio could not be
// fetched is silently dropped instead of sent as a summary.
func TestBuildMessageNoAttachment(t *testing.T) {
	msg, err := BuildMessage("voicemail@hello.test", Job{To: "x@hello.test", Caller: "5"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "You have a new voicemail from 5") {
		t.Fatalf("summary missing: %q", msg)
	}
	if strings.Contains(string(msg), "filename") {
		t.Fatalf("empty attachment sent: %q", msg)
	}
	if _, err := BuildMessage("", Job{To: "x"}, nil); err == nil {
		t.Fatal("no From address accepted")
	}
}

func readAll(p interface{ Read([]byte) (int, error) }) (string, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(p)
	return buf.String(), err
}

var _ = textproto.MIMEHeader{}
