package api

// Messaging media through the API against PostgreSQL (spec messaging-channels
// M-18): uploads, the authorised download, a chat site's logo and the erase
// of a visitor's files. Every documented status of the media routes, and the
// headers a served file carries.

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/media"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/store"
)

var mediaPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1, 2, 3}, 40)...)

// mediaUpload sends a multipart form: the named fields and, when data is not
// nil, a part "file".
func (c *client) mediaUpload(method, path, name, ctype string, data []byte, fields map[string]string) response {
	c.e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if data != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
		h.Set("Content-Type", ctype)
		part, err := mw.CreatePart(h)
		if err != nil {
			c.e.t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	_ = mw.Close()
	c.header.Set("Content-Type", mw.FormDataContentType())
	defer c.header.Del("Content-Type")
	return c.do(method, path, body.String())
}

// chatConversation makes a chat site and a visitor's conversation on it, and
// returns the conversation and the visitor.
func (r *inboxRig) chatConversation(name string) (int64, int64) {
	r.t.Helper()
	ctx := context.Background()
	site := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/chat/sites",
		map[string]any{"name": name, "challenge": "none", "allowedOrigins": []string{"https://intranet.lan"}}).json(r.t)
	s, err := r.st.GetChatSite(ctx, int64(site["id"].(float64)))
	if err != nil {
		r.t.Fatal(err)
	}
	ses, err := r.st.ChatSession(ctx, store.ChatSessionInput{Site: s, IPHash: []byte("ip-" + name)})
	if err != nil {
		r.t.Fatal(err)
	}
	m, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: s, VisitorID: ses.VisitorID, Text: "hello", ClientMsgID: "c-" + name})
	if err != nil {
		r.t.Fatal(err)
	}
	return m.ConversationID, ses.VisitorID
}

// TestInboxMediaAPI fails if an upload or a download route does not answer
// each documented status, a file the allowlist refuses is stored, someone who
// cannot see a conversation reads its file, a served file lacks nosniff or
// the private no-store cache rule, a text channel takes an agent's file, or
// an erase leaves a visitor's file in the store.
func TestInboxMediaAPI(t *testing.T) {
	r := newInboxRig(t)
	ctx := context.Background()
	conv, visitor := r.chatConversation("Help desk")
	sms := r.text(peerNumber(), "photo attached")
	cid := strconv.FormatInt(conv, 10)
	smsID := strconv.FormatInt(sms, 10)

	t.Run("upload as a supervisor", func(t *testing.T) {
		up := "/api/v1/inbox/media"
		r.viewer.must(http.StatusForbidden, "POST", up, nil)
		got := r.operator.mediaUpload("POST", up, "../screen.png", "image/png", mediaPNG, map[string]string{"conversationId": cid})
		if got.code != http.StatusCreated {
			t.Fatalf("upload = %d %s", got.code, got.body)
		}
		m := got.json(t)
		if m["status"] != "ready" || m["contentType"] != "image/png" || m["filename"] != "screen.png" || m["messageId"] != nil {
			t.Fatalf("upload = %v", m)
		}
		if r.objects.Len() != 1 {
			t.Fatalf("%d objects stored", r.objects.Len())
		}
		r.operator.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, nil).expect(t, http.StatusBadRequest)
		r.operator.mediaUpload("POST", up, "", "", nil, map[string]string{"conversationId": cid}).expect(t, http.StatusBadRequest)
		r.operator.must(http.StatusBadRequest, "POST", up, map[string]any{"conversationId": conv})
		r.operator.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, map[string]string{"conversationId": "999999"}).expect(t, http.StatusNotFound)
		r.operator.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, map[string]string{"conversationId": smsID}).expect(t, http.StatusConflict)
		big := append(append([]byte{}, mediaPNG...), make([]byte, 5<<20)...)
		r.operator.mediaUpload("POST", up, "big.png", "image/png", big, map[string]string{"conversationId": cid}).expect(t, http.StatusRequestEntityTooLarge)
		r.operator.mediaUpload("POST", up, "x.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), map[string]string{"conversationId": cid}).expect(t, http.StatusUnprocessableEntity)
		r.operator.mediaUpload("POST", up, "x.gif", "image/gif", []byte("GIF89a<script>alert(1)</script>"), map[string]string{"conversationId": cid}).expect(t, http.StatusUnprocessableEntity)
		if r.objects.Len() != 1 {
			t.Fatalf("a refused file was stored: %d objects", r.objects.Len())
		}
	})

	t.Run("send and read", func(t *testing.T) {
		got := r.operator.mediaUpload("POST", "/api/v1/inbox/media", "guide.pdf", "application/pdf", []byte("%PDF-1.4\n%%EOF\n"), map[string]string{"conversationId": cid}).json(t)
		id := int64(got["id"].(float64))
		// A text channel takes no agent's file.
		r.operator.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", sms, "/messages"), map[string]any{"body": "x", "mediaIds": []int64{id}})
		r.operator.must(http.StatusBadRequest, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": ""})
		msg := r.operator.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"),
			map[string]any{"body": "", "clientMsgId": "m1", "mediaIds": []int64{id}}).json(t)
		files := msg["media"].([]any)
		if len(files) != 1 || files[0].(map[string]any)["id"].(float64) != float64(id) {
			t.Fatalf("sent = %v", msg)
		}
		path := idPath("/api/v1/inbox/media", id, "")
		res := r.operator.must(http.StatusOK, "GET", path, nil)
		if res.header.Get("X-Content-Type-Options") != "nosniff" || res.header.Get("Content-Type") != "application/pdf" ||
			res.header.Get("Cache-Control") != "private, no-store" || !strings.HasPrefix(res.header.Get("Content-Disposition"), "attachment") ||
			!bytes.HasPrefix(res.body, []byte("%PDF")) {
			t.Fatalf("download headers %v", res.header)
		}
		r.operator.header.Set("Range", "bytes=0-3")
		part := r.operator.must(http.StatusPartialContent, "GET", path, nil)
		r.operator.header.Del("Range")
		if string(part.body) != "%PDF" {
			t.Fatalf("range = %q", part.body)
		}
		r.viewer.must(http.StatusForbidden, "GET", path, nil)
		r.operator.must(http.StatusNotFound, "GET", "/api/v1/inbox/media/999999", nil)

		// An agent reads a file once the conversation is theirs.
		mine := idPath("/api/v1/me/inbox/media", id, "")
		r.agent.must(http.StatusNotFound, "GET", mine, nil)
		r.assign(conv, r.agentID)
		r.agent.must(http.StatusOK, "GET", mine, nil)
		r.agent.header.Set("Range", "bytes=1-2")
		r.agent.must(http.StatusPartialContent, "GET", mine, nil)
		r.agent.header.Del("Range")
		r.other.must(http.StatusNotFound, "GET", mine, nil)
		r.viewer.must(http.StatusForbidden, "GET", mine, nil)

		// A carrier attachment still downloading has no file yet.
		var smsMsg int64
		if err := r.db.QueryRowContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id = $1 AND direction = 'in'`, sms).Scan(&smsMsg); err != nil {
			t.Fatal(err)
		}
		if err := r.st.AddInboundMedia(ctx, smsMsg, []provider.MediaRef{{URL: "https://api.twilio.com/x", ContentType: "image/jpeg"}}); err != nil {
			t.Fatal(err)
		}
		var pending int64
		if err := r.db.QueryRowContext(ctx, `SELECT id FROM message_media WHERE message_id = $1`, smsMsg).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		r.operator.must(http.StatusConflict, "GET", idPath("/api/v1/inbox/media", pending, ""), nil)
		r.assign(sms, r.agentID)
		r.agent.must(http.StatusConflict, "GET", idPath("/api/v1/me/inbox/media", pending, ""), nil)
		thread := r.agent.must(http.StatusOK, "GET", idPath("/api/v1/me/inbox/conversations", sms, "/messages"), nil).json(t)
		var first []any
		for _, it := range thread["items"].([]any) {
			if m := it.(map[string]any); m["direction"] == "in" {
				first = m["media"].([]any)
			}
		}
		if len(first) != 1 || first[0].(map[string]any)["status"] != "pending" {
			t.Fatalf("the thread shows %v", first)
		}
	})

	t.Run("upload as an agent", func(t *testing.T) {
		up := "/api/v1/me/inbox/media"
		r.viewer.must(http.StatusForbidden, "POST", up, nil)
		if got := r.agent.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, map[string]string{"conversationId": cid}); got.code != http.StatusCreated {
			t.Fatalf("agent upload = %d %s", got.code, got.body)
		}
		r.other.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, map[string]string{"conversationId": cid}).expect(t, http.StatusNotFound)
		r.agent.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, nil).expect(t, http.StatusBadRequest)
		r.agent.mediaUpload("POST", up, "a.png", "image/png", mediaPNG, map[string]string{"conversationId": smsID}).expect(t, http.StatusConflict)
		big := append(append([]byte{}, mediaPNG...), make([]byte, 5<<20)...)
		r.agent.mediaUpload("POST", up, "big.png", "image/png", big, map[string]string{"conversationId": cid}).expect(t, http.StatusRequestEntityTooLarge)
		r.agent.mediaUpload("POST", up, "a.png", "application/pdf", mediaPNG, map[string]string{"conversationId": cid}).expect(t, http.StatusUnprocessableEntity)
	})

	t.Run("erase removes the files", func(t *testing.T) {
		if r.objects.Len() == 0 {
			t.Fatal("setup: no files stored")
		}
		rep := r.admin.must(http.StatusOK, "POST", "/api/v1/inbox/erase", map[string]any{"peer": "visitor:" + strconv.FormatInt(visitor, 10)}).json(t)
		if rep["mediaObjects"].(float64) < 2 || rep["mediaObjectsPending"].(float64) != 0 {
			t.Fatalf("erase = %v", rep)
		}
		if r.objects.Len() != 0 {
			t.Fatalf("%d files left after the erase", r.objects.Len())
		}
		if n := r.count(`SELECT count(*) FROM message_media WHERE uploader_user_id IS NOT NULL`); n != 0 {
			t.Fatalf("%d upload rows left", n)
		}
	})
}

// TestChatSiteLogoAPI fails if a logo route does not answer each documented
// status, takes an SVG or an oversize image, or lets anyone but an
// administrator change it.
func TestChatSiteLogoAPI(t *testing.T) {
	r := newInboxRig(t)
	site := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/chat/sites",
		map[string]any{"name": "Logo site", "challenge": "none", "allowedOrigins": []string{"https://intranet.lan"}}).json(t)
	id := int64(site["id"].(float64))
	if site["logo"] != false || site["attachments"] != false {
		t.Fatalf("a new site = %v", site)
	}
	logo := idPath("/api/v1/messaging/chat/sites", id, "/logo")
	r.admin.must(http.StatusNotFound, "GET", logo, nil)
	r.admin.must(http.StatusNotFound, "DELETE", logo, nil)
	r.operator.must(http.StatusForbidden, "GET", logo, nil)
	r.operator.must(http.StatusForbidden, "DELETE", logo, nil)
	r.operator.mediaUpload("PUT", logo, "logo.png", "image/png", mediaPNG, nil).expect(t, http.StatusForbidden)
	r.admin.mediaUpload("PUT", idPath("/api/v1/messaging/chat/sites", 999999, "/logo"), "logo.png", "image/png", mediaPNG, nil).expect(t, http.StatusNotFound)
	r.admin.mediaUpload("PUT", logo, "", "", nil, nil).expect(t, http.StatusBadRequest)
	r.admin.mediaUpload("PUT", logo, "big.png", "image/png", append(append([]byte{}, mediaPNG...), make([]byte, 256<<10)...), nil).expect(t, http.StatusRequestEntityTooLarge)
	r.admin.mediaUpload("PUT", logo, "logo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), nil).expect(t, http.StatusUnprocessableEntity)
	r.admin.mediaUpload("PUT", logo, "doc.pdf", "application/pdf", []byte("%PDF-1.4\n"), nil).expect(t, http.StatusUnprocessableEntity)
	got := r.admin.mediaUpload("PUT", logo, "logo.png", "image/png", mediaPNG, nil)
	if got.code != http.StatusOK || got.json(t)["logo"] != true {
		t.Fatalf("set logo = %d %s", got.code, got.body)
	}
	img := r.admin.must(http.StatusOK, "GET", logo, nil)
	if img.header.Get("Content-Type") != "image/png" || img.header.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(img.body, mediaPNG) {
		t.Fatalf("logo = %v", img.header)
	}
	r.admin.must(http.StatusNoContent, "DELETE", logo, nil)
	if r.objects.Len() != 0 {
		t.Fatalf("%d objects left after removing the logo", r.objects.Len())
	}
	r.admin.must(http.StatusNotFound, "GET", logo, nil)
	// Attachments are an administrator's switch, kept on update.
	if s := r.admin.must(http.StatusOK, "PUT", idPath("/api/v1/messaging/chat/sites", id, ""), map[string]any{"attachments": true}).json(t); s["attachments"] != true {
		t.Fatalf("site = %v", s)
	}
}

// expect fails the test unless the response has status want.
func (r response) expect(t *testing.T, want int) {
	t.Helper()
	if r.code != want {
		t.Fatalf("status %d %s, want %d", r.code, r.body, want)
	}
}

// count runs a count query.
func (r *inboxRig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// putHookObjects is a media store that runs a hook once, before the next
// object is stored.
type putHookObjects struct {
	*media.MemObjects
	mu   sync.Mutex
	hook func()
}

func (o *putHookObjects) Put(ctx context.Context, key string, r io.Reader, size int64, ctype string) error {
	o.mu.Lock()
	h := o.hook
	o.hook = nil
	o.mu.Unlock()
	if h != nil {
		h()
	}
	return o.MemObjects.Put(ctx, key, r, size, ctype)
}

// TestInboxUploadErasedMeanwhile fails if the file of an upload whose row an
// erase removed while the file was being stored stays in the media store.
func TestInboxUploadErasedMeanwhile(t *testing.T) {
	objs := &putHookObjects{MemObjects: &media.MemObjects{}}
	r := newInboxRigConfig(t, Config{Live: noLive{}, MediaObjects: objs})
	ctx := context.Background()
	conv, visitor := r.chatConversation("Erase desk")
	objs.mu.Lock()
	objs.hook = func() {
		// The erase, and the API's removal of what it names, between the
		// upload's row and its file.
		rep, err := r.st.ErasePeer(ctx, "user:admin", "visitor:"+strconv.FormatInt(visitor, 10))
		if err != nil || len(rep.Media) != 1 {
			t.Errorf("erase = %+v, %v", rep, err)
		}
		for _, g := range rep.Media {
			_ = media.RemoveVerified(ctx, objs, g.Key)
			if err := r.st.DropMedia(ctx, g); err != nil {
				t.Error(err)
			}
		}
	}
	objs.mu.Unlock()
	r.operator.mediaUpload("POST", "/api/v1/inbox/media", "late.png", "image/png", mediaPNG,
		map[string]string{"conversationId": strconv.FormatInt(conv, 10)}).expect(t, http.StatusNotFound)
	if n := objs.Len(); n != 0 {
		t.Fatalf("%d files left in the media store after the erase", n)
	}
}
