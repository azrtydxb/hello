package fax

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
)

type fakeQueue struct {
	mu   sync.Mutex
	jobs []MailJob
	done map[int64]string // id -> "sent" | "retry:<err>" | "failed:<err>"
}

func (q *fakeQueue) ClaimFaxMail(context.Context, int, time.Duration) ([]MailJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs
	q.jobs = nil
	return j, nil
}

func (q *fakeQueue) FinishFaxMail(_ context.Context, id int64, errText string, final bool, _ time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.done == nil {
		q.done = map[int64]string{}
	}
	switch {
	case errText == "":
		q.done[id] = "sent"
	case final:
		q.done[id] = "failed:" + errText
	default:
		q.done[id] = "retry:" + errText
	}
	return nil
}

func settings(context.Context) (mailer.Settings, bool, error) {
	return mailer.Settings{From: "hello@pbx.test"}, true, nil
}

func TestDeliveryAttachesThePDFWithTheDocumentedName(t *testing.T) {
	pdf, _ := EncodePDF([]Page{testPage(1728, 40)})
	objs := &MemObjects{}
	_ = objs.Put(context.Background(), "fax/2026/7.pdf", pdf)
	at := time.Date(2026, 11, 9, 14, 5, 0, 0, time.UTC)
	q := &fakeQueue{jobs: []MailJob{{ID: 7, To: []string{"alice@pbx.test", "bob@pbx.test"}, Peer: "+41447654321", Pages: 3, CreatedAt: at, Object: "fax/2026/7.pdf", Attempts: 1, Owner: "101"}}}
	var sent []string
	var msgs [][]byte
	d := &Delivery{Queue: q, Objects: objs, Settings: settings, Deliver: func(_ context.Context, _ mailer.Settings, to string, msg []byte) error {
		sent, msgs = append(sent, to), append(msgs, msg)
		return nil
	}}
	d.Process(context.Background())
	if q.done[7] != "sent" || len(sent) != 2 {
		t.Fatalf("done = %v, sent to %v", q.done, sent)
	}
	m, err := mail.ReadMessage(bytes.NewReader(msgs[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Header.Get("Subject"), "+41447654321") {
		t.Errorf("subject %q lacks the sender number", m.Header.Get("Subject"))
	}
	_, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	mr := multipart.NewReader(m.Body, params["boundary"])
	var gotName string
	var body string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		if p.FileName() != "" {
			gotName = p.FileName()
			continue
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(p)
		body = b.String()
	}
	if gotName != "fax-20261109-1405-7.pdf" {
		t.Errorf("attachment named %q, want fax-20261109-1405-7.pdf", gotName)
	}
	if !strings.Contains(body, "+41447654321") || !strings.Contains(body, "Pages: 3") {
		t.Errorf("body lacks the sender or the page count: %q", body)
	}
	if !bytes.Contains(msgs[0], []byte("application/pdf")) {
		t.Error("the PDF is not attached as application/pdf")
	}
}

func TestDeliveryFailuresRetryThenGiveUp(t *testing.T) {
	objs := &MemObjects{}
	_ = objs.Put(context.Background(), "fax/2026/1.pdf", []byte("%PDF-1.4"))
	boom := func(context.Context, mailer.Settings, string, []byte) error { return errors.New("smtp down") }
	for _, tc := range []struct {
		name     string
		job      MailJob
		prefix   string
		wantSent bool
	}{
		{"first attempt retries", MailJob{ID: 1, To: []string{"a@b.test"}, Object: "fax/2026/1.pdf", Attempts: 1, CreatedAt: time.Now()}, "retry:", false},
		{"last attempt gives up", MailJob{ID: 2, To: []string{"a@b.test"}, Object: "fax/2026/1.pdf", Attempts: MailAttempts, CreatedAt: time.Now()}, "failed:", false},
		{"no recipient is final", MailJob{ID: 3, Owner: "205", Object: "fax/2026/1.pdf", Attempts: 1, CreatedAt: time.Now()}, "failed:no email address for extension 205", false},
		{"missing document retries", MailJob{ID: 4, To: []string{"a@b.test"}, Object: "fax/2026/gone.pdf", Attempts: 1, CreatedAt: time.Now()}, "retry:", false},
	} {
		q := &fakeQueue{jobs: []MailJob{tc.job}}
		d := &Delivery{Queue: q, Objects: objs, Settings: settings, Deliver: boom}
		d.Process(context.Background())
		if got := q.done[tc.job.ID]; !strings.HasPrefix(got, tc.prefix) {
			t.Errorf("%s: outcome %q, want prefix %q", tc.name, got, tc.prefix)
		}
	}
}

func TestDeliveryWaitsWithoutSMTP(t *testing.T) {
	q := &fakeQueue{jobs: []MailJob{{ID: 1, To: []string{"a@b.test"}}}}
	d := &Delivery{Queue: q, Objects: &MemObjects{}, Settings: func(context.Context) (mailer.Settings, bool, error) { return mailer.Settings{}, false, nil }}
	d.Process(context.Background())
	if len(q.done) != 0 || len(q.jobs) != 1 {
		t.Fatalf("without SMTP nothing may be claimed or finished: %v", q.done)
	}
}

type fakeRetention struct {
	mu      sync.Mutex
	rows    map[int64]string // id -> object key
	deleted []int64
}

func (r *fakeRetention) ExpiredFaxes(_ context.Context, days, limit int) ([]int64, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	var keys []string
	for id := int64(1); id <= 2000 && len(ids) < limit; id++ {
		if k, ok := r.rows[id]; ok {
			ids, keys = append(ids, id), append(keys, k)
		}
	}
	return ids, keys, nil
}

func (r *fakeRetention) DeleteFaxRows(_ context.Context, ids []int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		delete(r.rows, id)
		r.deleted = append(r.deleted, id)
	}
	return nil
}

func (r *fakeRetention) ExpireStuckFaxes(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

type objectsFailingOn struct {
	Objects
	bad string
	log *[]string
}

func (o objectsFailingOn) Remove(ctx context.Context, key string) error {
	if key == o.bad {
		return errors.New("minio down")
	}
	*o.log = append(*o.log, key)
	return o.Objects.Remove(ctx, key)
}

func TestRetentionDeletesObjectsBeforeRowsInBatches(t *testing.T) {
	rows := map[int64]string{}
	for id := int64(1); id <= 1200; id++ {
		rows[id] = "fax/2026/x.pdf"
	}
	rows[7] = "fax/2026/stuck.pdf"
	st := &fakeRetention{rows: rows}
	var removed []string
	objs := objectsFailingOn{Objects: &MemObjects{}, bad: "fax/2026/stuck.pdf", log: &removed}
	r := &Retention{Store: st, Objects: objs, Days: 90}
	n := r.Purge(context.Background())
	if n != 1199 {
		t.Fatalf("purged %d, want 1199", n)
	}
	if _, kept := st.rows[7]; !kept || len(st.rows) != 1 {
		t.Fatalf("a fax whose object could not be deleted must keep its row, and only that one: %d rows left", len(st.rows))
	}
	if len(removed) != 1199 {
		t.Errorf("%d objects removed", len(removed))
	}
}

func TestRetentionSkipsWhenAnotherNodeHoldsTheLock(t *testing.T) {
	st := &fakeRetention{rows: map[int64]string{1: ""}}
	r := &Retention{Store: st, Locker: lockerFunc(func() (func(), bool, error) { return nil, false, nil }), Days: 90}
	if n := r.Purge(context.Background()); n != 0 || len(st.rows) != 1 {
		t.Fatalf("purged under someone else's lock: %d", n)
	}
}

type lockerFunc func() (func(), bool, error)

func (f lockerFunc) TryLock(context.Context, string) (func(), bool, error) { return f() }

func TestAttachmentNameAndObjectKey(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := AttachmentName(42, at); got != "fax-20260304-0506-42.pdf" {
		t.Errorf("attachment %q", got)
	}
	if got := ObjectKey(42, at); got != "fax/2026/42.pdf" {
		t.Errorf("object key %q", got)
	}
	for _, key := range []string{"rec/1.wav", "fax/../x", "fax/a\\b"} {
		if checkKey(key) == nil {
			t.Errorf("object key %q accepted", key)
		}
	}
}

func TestNumberValidation(t *testing.T) {
	for n, want := range map[string]bool{"+41441234567": true, "+3225551234": true, "0041441234567": false, "+0123456789": false, "+123": false, "": false, "+41 44 123": false} {
		if ValidDestinationNumber(n) != want {
			t.Errorf("ValidDestinationNumber(%q) = %v", n, !want)
		}
	}
	if Ident("+41 (44) 123-4567") != "+41441234567" || len(Ident("123456789012345678901234")) != 20 {
		t.Error("Ident does not reduce to digits")
	}
}

func TestResultFor(t *testing.T) {
	for _, c := range []struct {
		dir   string
		ok    bool
		pages int
		want  string
	}{
		{DirInbound, true, 3, StatusReceived}, {DirOutbound, true, 3, StatusSent},
		{DirInbound, false, 2, StatusPartial}, {DirOutbound, false, 0, StatusFailed}, {DirInbound, false, 0, StatusFailed},
	} {
		if got := ResultFor(c.dir, c.ok, c.pages); got != c.want {
			t.Errorf("ResultFor(%v) = %s", c, got)
		}
	}
}
