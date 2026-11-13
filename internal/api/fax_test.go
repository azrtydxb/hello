package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/store"
)

// fakeFaxEngine stands in for hello-fax: a PDF holding REJECT cannot be
// rasterised, one holding LONG has too many pages, any other has two pages.
type fakeFaxEngine struct{}

func (fakeFaxEngine) Rasterise(_ context.Context, pdf []byte) (int, error) {
	switch {
	case bytes.Contains(pdf, []byte("REJECT")):
		return 0, fmt.Errorf("%w: not a PDF", fax.ErrRejected)
	case bytes.Contains(pdf, []byte("LONG")):
		return 0, fmt.Errorf("%w: more than 50 pages", fax.ErrTooManyPages)
	}
	return 2, nil
}

func faxUpload(c *client, path string, fields map[string]string, file []byte) response {
	c.e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if file != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="doc.pdf"`)
		h.Set("Content-Type", "application/pdf")
		part, err := mw.CreatePart(h)
		if err != nil {
			c.e.t.Fatal(err)
		}
		_, _ = part.Write(file)
	}
	_ = mw.Close()
	c.header.Set("Content-Type", mw.FormDataContentType())
	defer c.header.Del("Content-Type")
	return c.do("POST", path, body.String())
}

func (r response) rawJSONItems(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, it := range r.json(t)["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

// TestFaxAPI walks the fax routes against PostgreSQL: destinations and their
// use by routes, sending a PDF in both body forms with every refusal, the
// list and its filters, the PDF download, deleting and cancelling, and what a
// user with an extension sees. It fails if a destination a route uses can be
// deleted, a PDF that cannot be rasterised or is too large is queued, a fax
// is visible to a user who does not own it, or a fax in progress is deleted.
func TestFaxAPI(t *testing.T) {
	objs := &fax.MemObjects{}
	e := newEnvConfig(t, Config{Fax: FaxConfig{Enabled: true, Engine: fakeFaxEngine{}, Objects: objs}}, nil)
	c := e.login()
	ext101 := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"})

	// Fax destinations.
	for name, body := range map[string]map[string]any{
		"bad name":      {"name": "has space", "extension": "101"},
		"no extension":  {"name": "x"},
		"extension nan": {"name": "x", "extension": "abc"},
		"unknown ext":   {"name": "x", "extension": "999"},
		"bad email":     {"name": "x", "extension": "101", "email": "not an address"},
	} {
		if r := c.do("POST", "/api/v1/fax-destinations", body); r.code != http.StatusBadRequest {
			t.Errorf("destination %s: status %d, want 400", name, r.code)
		}
	}
	dest := c.must(http.StatusCreated, "POST", "/api/v1/fax-destinations", map[string]any{"name": "main", "extension": "101", "email": "fax@hello.test"}).json(t)
	if dest["extension"] != "101" || dest["email"] != "fax@hello.test" {
		t.Fatalf("destination = %v", dest)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/fax-destinations", map[string]any{"name": "main", "extension": "102"})
	did := rowID(dest)
	if len(listItems(t, c.must(http.StatusOK, "GET", "/api/v1/fax-destinations", nil))) != 1 {
		t.Fatal("the destination is not listed")
	}
	c.must(http.StatusOK, "GET", "/api/v1/fax-destinations/"+did, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/fax-destinations/99999", nil)
	if got := c.must(http.StatusOK, "PUT", "/api/v1/fax-destinations/"+did, map[string]any{"extension": "102", "email": ""}).json(t); got["extension"] != "102" || got["email"] != "" {
		t.Fatalf("updated destination = %v", got)
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/fax-destinations/"+did, map[string]any{"name": "renamed"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/fax-destinations/"+did, map[string]any{"email": "nope"})
	c.must(http.StatusNotFound, "PUT", "/api/v1/fax-destinations/99999", map[string]any{"email": ""})
	c.must(http.StatusNotFound, "DELETE", "/api/v1/fax-destinations/99999", nil)
	c.must(http.StatusOK, "PUT", "/api/v1/fax-destinations/"+did, map[string]any{"extension": "101"})

	// A route and an IVR key that name the destination hold it.
	route := c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{"name": "faxin", "destinationKind": "fax", "destination": "main"}).json(t)
	c.must(http.StatusBadRequest, "POST", "/api/v1/routes/inbound", map[string]any{"name": "faxbad", "destinationKind": "fax", "destination": "missing"})
	c.must(http.StatusConflict, "DELETE", "/api/v1/fax-destinations/"+did, nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/routes/inbound/"+rowID(route), nil)

	// Sending: multipart and JSON, and every refusal.
	pdf := []byte("%PDF-1.4\n% a small document\n")
	sent := faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567", "callerId": "+41441110000"}, pdf)
	if sent.code != http.StatusCreated {
		t.Fatalf("send = %d %s", sent.code, sent.body)
	}
	fx := sent.json(t)
	if fx["status"] != "queued" || fx["direction"] != "outbound" || fx["peerNumber"] != "+41441234567" || fx["pages"] != float64(2) || fx["hasPdf"] != true {
		t.Fatalf("queued fax = %v", fx)
	}
	key := fmt.Sprintf("fax/%04d/%s.pdf", time.Now().UTC().Year(), rowID(fx))
	if stored, err := objs.Get(context.Background(), key); err != nil || !bytes.Equal(stored, pdf) {
		t.Fatalf("the PDF is not stored under %s: %v", key, err)
	}
	b64 := base64.StdEncoding.EncodeToString(pdf)
	viaJSON := c.must(http.StatusCreated, "POST", "/api/v1/faxes", map[string]any{"to": "+3225551234", "pdfBase64": b64}).json(t)
	for name, r := range map[string]response{
		"no number":         faxUpload(c, "/api/v1/faxes", nil, pdf),
		"national number":   faxUpload(c, "/api/v1/faxes", map[string]string{"to": "0441234567"}, pdf),
		"bad caller id":     faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567", "callerId": "abc"}, pdf),
		"no file":           faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567"}, nil),
		"not a pdf":         faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567"}, []byte("hello")),
		"json without file": c.do("POST", "/api/v1/faxes", map[string]any{"to": "+41441234567"}),
		"json unknown":      c.do("POST", "/api/v1/faxes", map[string]any{"to": "+41441234567", "pdfBase64": b64, "x": 1}),
		"wrong body type":   c.do("POST", "/api/v1/faxes", "plain text"),
	} {
		if r.code != http.StatusBadRequest {
			t.Errorf("send %s: status %d %s, want 400", name, r.code, r.body)
		}
	}
	if r := faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567"}, []byte("%PDF-1.4 REJECT")); r.code != http.StatusUnprocessableEntity {
		t.Errorf("a PDF the engine rejects: status %d, want 422", r.code)
	}
	if r := faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567"}, []byte("%PDF-1.4 LONG")); r.code != http.StatusUnprocessableEntity {
		t.Errorf("a PDF of too many pages: status %d, want 422", r.code)
	}
	huge := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), fax.MaxSendBytes+1024)...)
	if r := faxUpload(c, "/api/v1/faxes", map[string]string{"to": "+41441234567"}, huge); r.code != http.StatusRequestEntityTooLarge {
		t.Errorf("an 11 MB PDF: status %d, want 413", r.code)
	}
	if n := len(objs.M); n != 2 {
		t.Errorf("%d documents stored, want 2: a refused fax must store nothing", n)
	}

	// A received fax, kept with its pages, and one that failed with none.
	ctx := context.Background()
	fs := e.st.Faxes()
	recvID, err := fs.BeginReceive(ctx, fax.Receive{PeerNumber: "+4144999", LocalNumber: "+41441110000", OwnerExtension: "101", CorrelationID: "corr-1"})
	if err != nil {
		t.Fatal(err)
	}
	// While it is being received it cannot be deleted.
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/faxes/%d", recvID), nil)
	rkey := fax.ObjectKey(recvID, time.Now())
	_ = objs.Put(ctx, rkey, []byte("%PDF-1.4 received"))
	if err := fs.Finish(ctx, recvID, fax.Finish{Status: fax.StatusReceived, Pages: 3, Mode: "t38", BitRate: 14400, ResultText: "OK", ObjectKey: rkey, MailTo: store.MailToOwner}); err != nil {
		t.Fatal(err)
	}
	foreignID, err := fs.BeginReceive(ctx, fax.Receive{PeerNumber: "+4144888", OwnerExtension: "102"})
	if err != nil {
		t.Fatal(err)
	}
	fkey := fax.ObjectKey(foreignID, time.Now())
	_ = objs.Put(ctx, fkey, []byte("%PDF-1.4 foreign"))
	if err := fs.Finish(ctx, foreignID, fax.Finish{Status: fax.StatusPartial, Pages: 1, ResultText: "dropped", ResultCode: 22, ObjectKey: fkey}); err != nil {
		t.Fatal(err)
	}
	emptyID, err := fs.BeginReceive(ctx, fax.Receive{PeerNumber: "+4144777", OwnerExtension: "101"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Finish(ctx, emptyID, fax.Finish{Status: fax.StatusFailed, ResultText: "no carrier"}); err != nil {
		t.Fatal(err)
	}

	got := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/faxes/%d", recvID), nil).json(t)
	if got["status"] != "received" || got["pages"] != float64(3) || got["mode"] != "t38" || got["hasPdf"] != true || got["correlationId"] != "corr-1" || got["mailStatus"] != "pending" {
		t.Fatalf("received fax = %v", got)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/faxes/99999", nil)
	if n := len(c.must(http.StatusOK, "GET", "/api/v1/faxes", nil).rawJSONItems(t)); n != 5 {
		t.Errorf("%d faxes listed, want 5", n)
	}
	if n := len(c.must(http.StatusOK, "GET", "/api/v1/faxes?direction=outbound", nil).rawJSONItems(t)); n != 2 {
		t.Errorf("%d outbound faxes, want 2", n)
	}
	if n := len(c.must(http.StatusOK, "GET", "/api/v1/faxes?status=partial", nil).rawJSONItems(t)); n != 1 {
		t.Errorf("%d partial faxes, want 1", n)
	}
	first := c.must(http.StatusOK, "GET", "/api/v1/faxes?limit=2", nil).json(t)
	if len(first["items"].([]any)) != 2 || first["next"] == "" {
		t.Fatalf("first page = %v", first)
	}
	second := c.must(http.StatusOK, "GET", "/api/v1/faxes?limit=2&cursor="+first["next"].(string), nil).rawJSONItems(t)
	if len(second) == 0 || second[0]["id"] == first["items"].([]any)[0].(map[string]any)["id"] {
		t.Errorf("the second page repeats the first: %v", second)
	}
	for _, q := range []string{"?direction=sideways", "?status=bogus", "?limit=0", "?cursor=abc"} {
		c.must(http.StatusBadRequest, "GET", "/api/v1/faxes"+q, nil)
	}

	// The PDF.
	r := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/faxes/%d/pdf", recvID), nil)
	if string(r.body) != "%PDF-1.4 received" || r.header.Get("Content-Type") != "application/pdf" ||
		!strings.Contains(r.header.Get("Content-Disposition"), "fax-") || !strings.HasSuffix(r.header.Get("Content-Disposition"), fmt.Sprintf("-%d.pdf\"", recvID)) {
		t.Errorf("pdf = %q %v", r.body, r.header)
	}
	c.must(http.StatusNotFound, "GET", fmt.Sprintf("/api/v1/faxes/%d/pdf", emptyID), nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/faxes/99999/pdf", nil)

	// A user with extension 101 sees the faxes it owns and nothing else.
	hash, _ := auth.HashPassword(testPassword)
	if _, err := e.st.CreateAccount(ctx, "test", store.NewAccount{Username: "pat", Role: auth.RoleUser, PasswordHash: hash,
		ExtensionIDs: []int64{int64(ext101["id"].(float64))}}); err != nil {
		t.Fatal(err)
	}
	u := e.client()
	u.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": "pat", "password": testPassword})
	mine := u.must(http.StatusOK, "GET", "/api/v1/me/faxes", nil).rawJSONItems(t)
	if len(mine) != 2 {
		t.Fatalf("the user sees %d faxes, want the 2 owned by 101: %v", len(mine), mine)
	}
	for _, f := range mine {
		if f["ownerExtension"] != "101" {
			t.Errorf("the user sees a fax owned by %v", f["ownerExtension"])
		}
	}
	u.must(http.StatusBadRequest, "GET", "/api/v1/me/faxes?limit=0", nil)
	if r := u.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/me/faxes/%d/pdf", recvID), nil); string(r.body) != "%PDF-1.4 received" {
		t.Errorf("own pdf = %q", r.body)
	}
	u.must(http.StatusNotFound, "GET", fmt.Sprintf("/api/v1/me/faxes/%d/pdf", foreignID), nil)
	u.must(http.StatusNotFound, "GET", fmt.Sprintf("/api/v1/me/faxes/%d/pdf", emptyID), nil)
	u.must(http.StatusNotFound, "GET", "/api/v1/me/faxes/99999/pdf", nil)
	u.must(http.StatusForbidden, "GET", "/api/v1/faxes", nil) // the user role reaches only /me

	// Deleting: a queued fax goes with its document; the others too.
	qid := rowID(fx)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/faxes/"+qid, nil)
	if _, err := objs.Get(ctx, key); err == nil {
		t.Error("the document of a deleted fax is still stored")
	}
	c.must(http.StatusNotFound, "DELETE", "/api/v1/faxes/"+qid, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/faxes/%d", emptyID), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/faxes/"+rowID(viaJSON), nil)

	// With the route gone the destination can go; the faxes it received stay.
	c.must(http.StatusNoContent, "DELETE", "/api/v1/fax-destinations/"+did, nil)
	if got := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/faxes/%d", recvID), nil).json(t); got["destination"] != "" || got["ownerExtension"] != "101" {
		t.Errorf("a received fax lost its owner with its destination: %v", got)
	}
}

// TestFaxQueueAndRetention drives the store the SIP node and the retention
// job use: claiming under a lease, renewing, retrying, cancelling, the email
// queue and the purge.
func TestFaxQueueAndRetention(t *testing.T) {
	objs := &fax.MemObjects{}
	e := newEnvConfig(t, Config{}, nil)
	ctx := context.Background()
	fs := e.st.Faxes()
	mk := func(to string) fax.Fax {
		f, err := e.st.CreateOutboundFax(ctx, store.OutboundFax{Peer: to, CallerID: "+41441110000", Actor: "test", Pages: 2})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	f1 := mk("+41440000001")
	// Not claimable before its document is stored.
	if got, err := fs.ClaimOutbound(ctx, "n1", time.Minute, 5); err != nil || len(got) != 0 {
		t.Fatalf("claimed a fax with no document: %v %v", got, err)
	}
	if err := e.st.SetFaxObject(ctx, f1.ID, fax.ObjectKey(f1.ID, f1.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	got, err := fs.ClaimOutbound(ctx, "n1", time.Minute, 5)
	if err != nil || len(got) != 1 || got[0].ID != f1.ID || got[0].Attempts != 1 || got[0].Status != "sending" {
		t.Fatalf("claim = %+v %v", got, err)
	}
	if again, _ := fs.ClaimOutbound(ctx, "n2", time.Minute, 5); len(again) != 0 {
		t.Fatalf("a second node claimed a fax under lease: %v", again)
	}
	if ok, err := fs.Renew(ctx, f1.ID, "n1", time.Minute); err != nil || !ok {
		t.Fatalf("renew by the holder = %v %v", ok, err)
	}
	if ok, _ := fs.Renew(ctx, f1.ID, "n2", time.Minute); ok {
		t.Fatal("another node renewed the lease")
	}
	if err := fs.Retry(ctx, f1.ID, time.Hour, "busy"); err != nil {
		t.Fatal(err)
	}
	if again, _ := fs.ClaimOutbound(ctx, "n1", time.Minute, 5); len(again) != 0 {
		t.Fatalf("a fax retried in an hour was claimed now: %v", again)
	}
	// An operator cancels it while a node sends it: the next renewal fails.
	if _, err := e.db.ExecContext(ctx, `UPDATE faxes SET next_attempt_at = now() WHERE id = $1`, f1.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = fs.ClaimOutbound(ctx, "n1", time.Minute, 5); len(got) != 1 || got[0].Attempts != 2 {
		t.Fatalf("second attempt = %+v", got)
	}
	if err := e.st.CancelFax(ctx, "test", f1.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := fs.Renew(ctx, f1.ID, "n1", time.Minute); ok {
		t.Fatal("a cancelled fax kept its lease")
	}
	if err := e.st.CancelFax(ctx, "test", f1.ID); err == nil {
		t.Fatal("cancelled a fax twice")
	}

	// Email queue: a received fax is claimed once, retried, then sent.
	id, _ := fs.BeginReceive(ctx, fax.Receive{PeerNumber: "+4144", OwnerExtension: "101"})
	if err := fs.Finish(ctx, id, fax.Finish{Status: fax.StatusReceived, Pages: 1, ObjectKey: "fax/2026/x.pdf", MailTo: "ops@hello.test"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := e.st.ClaimFaxMail(ctx, 5, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].To[0] != "ops@hello.test" || jobs[0].Attempts != 1 {
		t.Fatalf("mail claim = %+v %v", jobs, err)
	}
	if again, _ := e.st.ClaimFaxMail(ctx, 5, time.Minute); len(again) != 0 {
		t.Fatalf("a mail claimed twice: %v", again)
	}
	if err := e.st.FinishFaxMail(ctx, id, "smtp down", false, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.st.ClaimFaxMail(ctx, 5, time.Minute); len(again) != 1 || again[0].Attempts != 2 {
		t.Fatalf("retry claim = %+v", again)
	}
	if err := e.st.FinishFaxMail(ctx, id, "", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.st.GetFax(ctx, id); f.MailStatus != "sent" {
		t.Fatalf("mail status = %q", f.MailStatus)
	}

	// Retention: only finished faxes older than the period go, objects first.
	_ = objs.Put(ctx, "fax/2026/x.pdf", []byte("x"))
	old, _ := fs.BeginReceive(ctx, fax.Receive{PeerNumber: "+4155"})
	_ = fs.Finish(ctx, old, fax.Finish{Status: fax.StatusReceived, ObjectKey: "fax/2026/x.pdf"})
	// A fax still being received stays recent, and so is never purged.
	live, _ := fs.BeginReceive(ctx, fax.Receive{PeerNumber: "+4166"})
	for _, i := range []int64{old, id} {
		if _, err := e.db.ExecContext(ctx, `UPDATE faxes SET created_at = now() - interval '100 days' WHERE id = $1`, i); err != nil {
			t.Fatal(err)
		}
	}
	r := &fax.Retention{Store: e.st, Objects: objs, Locker: e.st, Days: 90}
	if n := r.Purge(ctx); n != 2 {
		t.Fatalf("purged %d, want 2 (the finished ones)", n)
	}
	if _, err := e.st.GetFax(ctx, live); err != nil {
		t.Fatalf("a fax still being received was purged: %v", err)
	}
	if _, err := objs.Get(ctx, "fax/2026/x.pdf"); err == nil {
		t.Fatal("the object of a purged fax is still stored")
	}
}
