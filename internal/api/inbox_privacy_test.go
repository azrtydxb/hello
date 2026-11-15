package api

// Messaging item 8 against PostgreSQL: the reports and their targets, the
// export of one customer, the redaction of a message with the carrier's
// deletion, the provider part of an erase, the supervisor-view audit report
// and the inbox's events. The package's conformance validator checks every
// request and response against openapi.json.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/store"
)

// lateRedactor is the API's MsgRedactor, aimed at the rig's store once the
// rig exists.
type lateRedactor struct {
	mu sync.Mutex
	r  *messaging.Redactor
}

func (l *lateRedactor) RunBatch(ctx context.Context, batch string, budget time.Duration) {
	l.mu.Lock()
	r := l.r
	l.mu.Unlock()
	if r != nil {
		r.RunBatch(ctx, batch, budget)
	}
}

// redactCounts is how many DELETEs and Body="" redactions the fake saw.
func (f *fakeTwilio) redactCounts() (deletes, blanks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes, f.blanks
}

// recordSink records the inbox's events as the pipeline would get them.
type recordSink struct {
	mu  sync.Mutex
	got []messaging.Event
}

func (s *recordSink) Emit(_ context.Context, e messaging.Event) {
	s.mu.Lock()
	s.got = append(s.got, e)
	s.mu.Unlock()
}

func (s *recordSink) types(conv int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.got {
		if e.ConversationID == conv {
			t := e.Type
			if e.NewConversation {
				t = "+" + t
			}
			out = append(out, t)
		}
	}
	return out
}

// TestInboxReportsAPI fails if the report miscounts volumes, response and
// resolution times or breaches, refuses a valid period or grouping, accepts
// an invalid one, answers anyone below operator, carries a number or text,
// or if the targets cannot be read and set by an administrator only.
func TestInboxReportsAPI(t *testing.T) {
	r := newInboxRig(t)
	ctx := context.Background()
	peerA, peerB := peerNumber(), peerNumber()
	answered := r.text(peerA, "first question")
	waiting := r.text(peerB, "second question")
	r.text(peerA, "a follow-up")
	r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", answered, "/messages"), map[string]any{"body": "an answer"})
	r.admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", answered, "/status"), map[string]any{"status": "resolved"})
	// The waiting conversation started an hour ago and nobody answered.
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET created_at = now() - interval '1 hour' WHERE id = $1`, waiting); err != nil {
		t.Fatal(err)
	}

	// The targets.
	r.operator.must(http.StatusForbidden, "GET", "/api/v1/messaging/sla", nil)
	sla := r.admin.must(http.StatusOK, "GET", "/api/v1/messaging/sla", nil).json(t)
	if sla["firstResponseSeconds"] != float64(900) || sla["resolutionSeconds"] != float64(86400) {
		t.Fatalf("default targets = %v", sla)
	}
	r.admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/sla", map[string]any{"firstResponseSeconds": -1, "resolutionSeconds": 0})
	r.admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/sla", map[string]any{"firstResponseSeconds": 60})
	r.operator.must(http.StatusForbidden, "PUT", "/api/v1/messaging/sla", map[string]any{"firstResponseSeconds": 60, "resolutionSeconds": 600})
	sla = r.admin.must(http.StatusOK, "PUT", "/api/v1/messaging/sla", map[string]any{"firstResponseSeconds": 600, "resolutionSeconds": 1800}).json(t)
	if sla["firstResponseSeconds"] != float64(600) {
		t.Fatalf("targets after PUT = %v", sla)
	}

	// The totals.
	rep := r.operator.must(http.StatusOK, "GET", "/api/v1/inbox/reports", nil)
	for _, secret := range []string{peerA, peerB, "question", "an answer"} {
		if bytes.Contains(rep.body, []byte(secret)) {
			t.Fatalf("the report carries %q: %s", secret, rep.body)
		}
	}
	tot := rep.json(t)["totals"].(map[string]any)
	if tot["conversations"] != float64(2) || tot["inboundConversations"] != float64(2) || tot["resolved"] != float64(1) ||
		tot["messagesIn"] != float64(3) || tot["messagesOut"] != float64(1) {
		t.Fatalf("totals = %v", tot)
	}
	if fr := tot["firstResponse"].(map[string]any); fr["count"] != float64(1) {
		t.Fatalf("first response = %v", fr)
	}
	if res := tot["resolution"].(map[string]any); res["count"] != float64(1) {
		t.Fatalf("resolution = %v", res)
	}
	// The waiting conversation is past both targets; the answered one met them.
	if tot["slaFirstResponseBreaches"] != float64(1) || tot["slaResolutionBreaches"] != float64(1) {
		t.Fatalf("breaches = %v", tot)
	}

	// The groupings.
	for _, by := range []string{"channel", "queue", "agent", "day"} {
		q := "?groupBy=" + by
		if by == "day" {
			q += "&timeZone=Europe%2FBrussels"
		}
		rows := r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/reports"+q, nil).json(t)["rows"].([]any)
		if len(rows) == 0 {
			t.Fatalf("groupBy=%s: no rows", by)
		}
		sum := 0.0
		for _, x := range rows {
			sum += x.(map[string]any)["conversations"].(float64)
		}
		if sum != 2 {
			t.Errorf("groupBy=%s: conversations add up to %v", by, sum)
		}
		if by == "channel" && rows[0].(map[string]any)["label"] != "Main" {
			t.Errorf("channel label = %v", rows[0])
		}
	}
	from := url.QueryEscape(time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339))
	to := url.QueryEscape(time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/reports?from="+from+"&to="+to+"&groupBy=agent", nil)
	for _, q := range []string{"?groupBy=peer", "?from=yesterday", "?from=" + to + "&to=" + from, "?timeZone=Mars%2FOlympus",
		"?from=" + url.QueryEscape(time.Now().AddDate(-2, 0, 0).UTC().Format(time.RFC3339))} {
		r.admin.must(http.StatusBadRequest, "GET", "/api/v1/inbox/reports"+q, nil)
		r.admin.must(http.StatusBadRequest, "GET", "/api/v1/inbox/reports/export"+q, nil)
	}
	r.viewer.must(http.StatusForbidden, "GET", "/api/v1/inbox/reports", nil)
	r.agent.must(http.StatusForbidden, "GET", "/api/v1/inbox/reports", nil)
	r.viewer.must(http.StatusForbidden, "GET", "/api/v1/inbox/reports/export", nil)

	csvOut := r.operator.must(http.StatusOK, "GET", "/api/v1/inbox/reports/export?groupBy=channel", nil)
	lines := strings.Split(strings.TrimSpace(string(csvOut.body)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "group,key,label,conversations") || !strings.HasPrefix(lines[1], "total,") ||
		!strings.HasPrefix(lines[2], "channel,") {
		t.Fatalf("CSV = %q", csvOut.body)
	}
	if ct := csvOut.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("CSV content type %q", ct)
	}
}

// storedMedia stores data as a ready attachment (spec M-18) of message msgID
// (0: none, an upload not sent yet by visitor) in conversation conv, the way
// the media worker or an upload leaves it, and returns its id and key.
func (r *inboxRig) storedMedia(conv, msgID, visitor int64, direction, name string, data []byte) (int64, string) {
	r.t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	key := "msg/test/" + strconv.FormatInt(inboxSeq.Add(1), 10) + "/" + sha
	if err := r.objects.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/png"); err != nil {
		r.t.Fatal(err)
	}
	var msg, vis any
	if msgID != 0 {
		msg = msgID
	}
	if visitor != 0 {
		vis = visitor
	}
	var id int64
	if err := r.db.QueryRowContext(ctx, `
		INSERT INTO message_media (message_id, conversation_id, channel_id, direction, status, object_key, content_type, size, sha256,
			filename, uploader_visitor_id, attached_at)
		SELECT $1::bigint, c.id, c.channel_id, $3::text, 'ready', $4::text, 'image/png', $5::bigint, $6::text, $7::text, $8::bigint,
			CASE WHEN $1::bigint IS NULL THEN NULL ELSE now() END
		FROM conversations c WHERE c.id = $2 RETURNING id`, msg, conv, direction, key, len(data), sha, name, vis).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id, key
}

// TestInboxExportAPI fails if the export of a customer misses a conversation
// or message, includes internal notes unasked, holds another customer, is
// not audited with counts only, answers anyone but an administrator, or
// misses an attachment (its metadata or its file, spec M-18), a chat
// visitor's unsent upload, or holds another customer's file.
func TestInboxExportAPI(t *testing.T) {
	r := newInboxRig(t)
	ctx := context.Background()
	peer, other := peerNumber(), peerNumber()
	conv := r.text(peer, "my order is late")
	r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/notes"), map[string]any{"body": "VIP customer"})
	r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": "on its way"})
	otherConv := r.text(other, "someone else")

	// The customer's photo (downloaded), a second attachment still
	// downloading, and another customer's file.
	var inMsg, otherMsg int64
	if err := r.db.QueryRowContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id = $1 AND direction = 'in'`, conv).Scan(&inMsg); err != nil {
		t.Fatal(err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id = $1 AND direction = 'in'`, otherConv).Scan(&otherMsg); err != nil {
		t.Fatal(err)
	}
	photo, _ := r.storedMedia(conv, inMsg, 0, "in", "parcel.png", mediaPNG)
	if err := r.st.AddInboundMedia(ctx, inMsg, []provider.MediaRef{{URL: "https://api.twilio.com/m", ContentType: "image/jpeg"}}); err != nil {
		t.Fatal(err)
	}
	otherFile := append([]byte("\x89PNG\r\n\x1a\nOTHER-CUSTOMER"), mediaPNG...)
	r.storedMedia(otherConv, otherMsg, 0, "in", "other.png", otherFile)

	read := func(body []byte) (map[string]any, map[string][]byte) {
		t.Helper()
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("not a zip: %v", err)
		}
		files := map[string][]byte{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			files[f.Name] = b
		}
		var idx map[string]any
		if err := json.Unmarshal(files["conversations.json"], &idx); err != nil {
			t.Fatalf("conversations.json: %v (%v)", err, files)
		}
		return idx, files
	}
	resp := r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/export?peer="+url.QueryEscape(peer), nil)
	if ct := resp.header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content type %q", ct)
	}
	idx, files := read(resp.body)
	convs := idx["conversations"].([]any)
	if len(convs) != 1 || files["README.txt"] == nil {
		t.Fatalf("export index = %v, files %d", idx, len(files))
	}
	all := ""
	msgs := 0
	for name, b := range files {
		if strings.HasPrefix(name, "messages/") {
			msgs++
			all += string(b)
		}
	}
	if !strings.Contains(all, "my order is late") || !strings.Contains(all, "on its way") || strings.Contains(all, "VIP customer") ||
		strings.Contains(all, "someone else") {
		t.Fatalf("exported messages = %s", all)
	}
	// The attachments: both listed with the message, the photo's file in
	// the archive at the path its entry names, nothing of the other
	// customer's.
	var in struct {
		Media []struct {
			ID       int64  `json:"id"`
			Status   string `json:"status"`
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
			File     string `json:"file"`
		} `json:"media"`
	}
	if err := json.Unmarshal(files["messages/"+itoa(conv)+"/"+itoa(inMsg)+".json"], &in); err != nil {
		t.Fatalf("the inbound message: %v", err)
	}
	if len(in.Media) != 2 {
		t.Fatalf("exported attachments = %+v", in.Media)
	}
	got, pending := in.Media[0], in.Media[1]
	if got.ID != photo || got.Status != "ready" || got.Filename != "parcel.png" || got.SHA256 == "" ||
		got.File != "media/"+itoa(conv)+"/"+itoa(photo)+".png" {
		t.Fatalf("the photo's entry = %+v", got)
	}
	if !bytes.Equal(files[got.File], mediaPNG) {
		t.Fatalf("the photo's file in the archive = %d bytes", len(files[got.File]))
	}
	if pending.Status != "pending" || pending.File != "" {
		t.Fatalf("the downloading attachment's entry = %+v", pending)
	}
	for name, b := range files {
		if bytes.Contains(b, []byte("OTHER-CUSTOMER")) || strings.Contains(string(b), "other.png") {
			t.Fatalf("%s holds the other customer's file", name)
		}
	}
	if string(files["uploads.json"]) != "[]\n" {
		t.Fatalf("uploads.json = %q", files["uploads.json"])
	}
	_, files = read(r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/export?peer="+url.QueryEscape(peer)+"&notes=true", nil).body)
	withNotes := ""
	for name, b := range files {
		if strings.HasPrefix(name, "messages/") {
			withNotes += string(b)
		}
	}
	if !strings.Contains(withNotes, "VIP customer") {
		t.Fatalf("notes=true left the note out")
	}
	var audit string
	if err := r.db.QueryRowContext(ctx, `SELECT action FROM audit_events WHERE action LIKE 'export %' ORDER BY id LIMIT 1`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(audit, "export conversations=1 messages=") || !strings.Contains(audit, " media=2 ") || strings.Contains(audit, peer) {
		t.Errorf("export audit = %q", audit)
	}

	// A file the media sweeper removed meanwhile is listed without one.
	if err := r.objects.Remove(ctx, func() string {
		var k string
		if err := r.db.QueryRowContext(ctx, `SELECT object_key FROM message_media WHERE id = $1`, photo).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}()); err != nil {
		t.Fatal(err)
	}
	_, files = read(r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/export?peer="+url.QueryEscape(peer), nil).body)
	if b := string(files["messages/"+itoa(conv)+"/"+itoa(inMsg)+".json"]); !strings.Contains(b, `"file": ""`) || strings.Contains(b, `"file": "media/`) {
		t.Fatalf("a removed file is still listed: %s", b)
	}

	// A chat visitor's export holds their sent file and the one not sent yet.
	chat, visitor := r.chatConversation("Export desk")
	var chatMsg int64
	if err := r.db.QueryRowContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id = $1 AND direction = 'in'`, chat).Scan(&chatMsg); err != nil {
		t.Fatal(err)
	}
	sent, _ := r.storedMedia(chat, chatMsg, visitor, "in", "sent.png", mediaPNG)
	unsent, _ := r.storedMedia(chat, 0, visitor, "in", "draft.png", mediaPNG)
	_, files = read(r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/export?peer=visitor:"+itoa(visitor), nil).body)
	if !bytes.Equal(files["media/"+itoa(chat)+"/"+itoa(sent)+".png"], mediaPNG) {
		t.Fatalf("the visitor's sent file is missing: %v", keys(files))
	}
	if !bytes.Equal(files["media/uploads/"+itoa(unsent)+".png"], mediaPNG) || !strings.Contains(string(files["uploads.json"]), "draft.png") {
		t.Fatalf("the visitor's unsent upload is missing: %v %s", keys(files), files["uploads.json"])
	}
	r.admin.must(http.StatusBadRequest, "GET", "/api/v1/inbox/export?peer=nope", nil)
	r.admin.must(http.StatusBadRequest, "GET", "/api/v1/inbox/export?peer="+url.QueryEscape(peer)+"&notes=maybe", nil)
	r.operator.must(http.StatusForbidden, "GET", "/api/v1/inbox/export?peer="+url.QueryEscape(peer), nil)
	old := store.MaxExportMessages
	store.MaxExportMessages = 1
	t.Cleanup(func() { store.MaxExportMessages = old })
	r.admin.must(http.StatusUnprocessableEntity, "GET", "/api/v1/inbox/export?peer="+url.QueryEscape(peer), nil)
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestInboxRedactAPI fails if a redacted message keeps its text anywhere
// Hello holds it, is not deleted at Twilio (or blanked there when Twilio
// cannot delete it yet), is not audited, if a system line can be redacted,
// if anyone but an administrator can redact, if a redacted message's
// attachment (its file, its row or a public link to it, spec M-18) survives,
// or if an erase does not report the carrier's part.
func TestInboxRedactAPI(t *testing.T) {
	late := &lateRedactor{}
	r := newTwilioRigConfig(t, Config{Live: noLive{}, MsgRedactor: late})
	late.mu.Lock()
	late.r = &messaging.Redactor{Store: r.st}
	late.mu.Unlock()
	ctx := context.Background()
	r.allowFirstContact(10, 10)
	peer := twilioPeer()
	conv := int64(r.start(http.StatusCreated, peer, "your card number is 4111").json(t)["id"].(float64))
	r.sendOnce()
	msgID, _, providerID, _ := r.lastMessage(conv)
	if providerID == "" {
		t.Fatal("the text was not sent")
	}
	// The text carried a file, with a public link a carrier could fetch it
	// through, and a second attachment never stored.
	file, fileKey := r.storedMedia(conv, msgID, 0, "out", "card.png", mediaPNG)
	if _, _, err := r.st.MintMediaLink(ctx, file); err != nil {
		t.Fatal(err)
	}
	var unstored int64
	if err := r.db.QueryRowContext(ctx, `
		INSERT INTO message_media (message_id, conversation_id, channel_id, direction, status, attached_at)
		VALUES ($1, $2, $3, 'out', 'rejected', now()) RETURNING id`, msgID, conv, r.tChannel).Scan(&unstored); err != nil {
		t.Fatal(err)
	}
	path := idPath("/api/v1/inbox/conversations", conv, "/messages/") + itoa(msgID) + "/redact"
	r.operator.must(http.StatusForbidden, "POST", path, nil)
	r.admin.must(http.StatusNotFound, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages/99999/redact"), nil)
	rep := r.admin.must(http.StatusOK, "POST", path, nil).json(t)
	m := rep["message"].(map[string]any)
	if m["body"] != "" || m["redactedAt"] == nil {
		t.Fatalf("redacted message = %v", m)
	}
	if media, _ := m["media"].([]any); media == nil || len(media) != 0 {
		t.Fatalf("the redacted message still has attachments: %v", m["media"])
	}
	if rep["mediaObjects"] != float64(1) || rep["mediaObjectsPending"] != float64(0) {
		t.Fatalf("redaction media counts = %v, %v", rep["mediaObjects"], rep["mediaObjectsPending"])
	}
	if ok, _ := r.objects.Exists(ctx, fileKey); ok {
		t.Fatal("the redacted message's file is still in the media store")
	}
	if n := r.count(`SELECT count(*) FROM message_media WHERE id = ANY($1::bigint[])`, []int64{file, unstored}); n != 0 {
		t.Fatalf("%d attachment rows left after the redaction", n)
	}
	if n := r.count(`SELECT count(*) FROM media_links WHERE media_id = $1`, file); n != 0 {
		t.Fatalf("%d public links left after the redaction", n)
	}
	provs := rep["providers"].([]any)
	if len(provs) != 1 || provs[0].(map[string]any)["provider"] != "twilio" || provs[0].(map[string]any)["redacted"] != true {
		t.Fatalf("provider part = %v", provs)
	}
	if d, _ := r.fake.redactCounts(); d != 1 {
		t.Errorf("Twilio DELETEs = %d, want 1", d)
	}
	var body string
	if err := r.db.QueryRowContext(ctx, `SELECT body FROM conversation_messages WHERE id = $1`, msgID).Scan(&body); err != nil || body != "" {
		t.Fatalf("stored body %q, %v", body, err)
	}
	var copies int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE conversation_message_id = $1`, msgID).Scan(&copies); err != nil || copies != 0 {
		t.Fatalf("copies in the SIP queue = %d, %v", copies, err)
	}
	var audit int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action = 'redact' AND resource_id = $1`, itoa(msgID)).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("redact audit rows = %d, %v", audit, err)
	}
	// Twice changes nothing.
	again := r.admin.must(http.StatusOK, "POST", path, nil).json(t)
	if d, _ := r.fake.redactCounts(); len(again["providers"].([]any)) != 0 || d != 1 || again["mediaObjects"] != float64(0) {
		t.Fatalf("a second redaction = %v, deletes %d", again, d)
	}
	// A system line has nothing to redact.
	var sys int64
	if err := r.db.QueryRowContext(ctx, `
		INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, body)
		VALUES ($1, $2, 'out', 'system', 'system', 'Assigned') RETURNING id`, conv, r.tChannel).Scan(&sys); err != nil {
		t.Fatal(err)
	}
	r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages/")+itoa(sys)+"/redact", nil)

	// Twilio cannot delete a message still on its way: it is blanked.
	r.fake.set(func(f *fakeTwilio) { f.deleteStatus = http.StatusBadRequest })
	r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": "second"})
	r.sendOnce()
	second, _, _, _ := r.lastMessage(conv)
	rep = r.admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages/")+itoa(second)+"/redact", nil).json(t)
	if _, b := r.fake.redactCounts(); rep["providers"].([]any)[0].(map[string]any)["done"] != float64(1) || b != 1 {
		t.Fatalf("blanked redaction = %v, blanks %d", rep, b)
	}

	// The counts.
	r.operator.must(http.StatusForbidden, "GET", "/api/v1/messaging/redactions", nil)
	counts := listItems(t, r.admin.must(http.StatusOK, "GET", "/api/v1/messaging/redactions", nil))
	if len(counts) != 1 || counts[0].(map[string]any)["done"] != float64(2) {
		t.Fatalf("redaction counts = %v", counts)
	}

	// An erase deletes the rest at Twilio and says so.
	r.fake.set(func(f *fakeTwilio) { f.deleteStatus = 0 })
	r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": "third"})
	r.sendOnce()
	er := r.admin.must(http.StatusOK, "POST", "/api/v1/inbox/erase", map[string]any{"peer": peer}).json(t)
	ps := er["providers"].([]any)
	if len(ps) != 1 {
		t.Fatalf("erase providers = %v", ps)
	}
	if p := ps[0].(map[string]any); p["provider"] != "twilio" || p["messages"] != float64(1) || p["done"] != float64(1) || p["redacted"] != true {
		t.Fatalf("erase provider part = %v", p)
	}
	var rows int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'provider_redact provider=twilio%'`).Scan(&rows); err != nil || rows != 3 {
		t.Errorf("provider audit rows = %d, %v", rows, err)
	}
}

// TestSupervisorViewsAPI fails if a supervisor's look at someone else's
// conversation is missing from the report, a filter or page misbehaves, or
// anyone but an administrator reads it.
func TestSupervisorViewsAPI(t *testing.T) {
	r := newInboxRig(t)
	conv := r.text(peerNumber(), "help")
	r.assign(conv, r.agentID)
	r.operator.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", conv, ""), nil)
	conv2 := r.text(peerNumber(), "more help")
	r.assign(conv2, r.agentID)
	r.operator.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", conv2, ""), nil)
	r.admin.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", conv2, ""), nil)

	rep := r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/audit/supervisor-views", nil).json(t)
	items := rep["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("views = %v", items)
	}
	totals := rep["totals"].([]any)
	if top := totals[0].(map[string]any); top["actor"] != "user:otto" || top["views"] != float64(2) || top["conversations"] != float64(2) {
		t.Fatalf("totals = %v", totals)
	}
	page := r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/audit/supervisor-views?actor=user%3Aotto&limit=1", nil).json(t)
	if len(page["items"].([]any)) != 1 || page["nextBefore"] == float64(0) {
		t.Fatalf("first page = %v", page)
	}
	next := r.admin.must(http.StatusOK, "GET", "/api/v1/inbox/audit/supervisor-views?actor=user%3Aotto&limit=1&before="+
		itoa(int64(page["nextBefore"].(float64))), nil).json(t)
	if it := next["items"].([]any); len(it) != 1 || it[0].(map[string]any)["conversationId"] != float64(conv) {
		t.Fatalf("second page = %v", next)
	}
	for _, q := range []string{"?from=x", "?limit=0", "?before=-1", "?from=" + url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))} {
		r.admin.must(http.StatusBadRequest, "GET", "/api/v1/inbox/audit/supervisor-views"+q, nil)
	}
	r.operator.must(http.StatusForbidden, "GET", "/api/v1/inbox/audit/supervisor-views", nil)
}

// TestInboxEventsPipeline fails if the inbox does not emit an event for a
// new conversation, a received or sent message, an assignment and a
// resolution, if an event's data misses its channel, number or hashed
// customer, or if the content a webhook may ask for is given for a note or
// after a redaction.
func TestInboxEventsPipeline(t *testing.T) {
	r := newInboxRig(t)
	ctx := context.Background()
	sink := &recordSink{}
	r.st.WithMessagingEvents(sink)
	t.Cleanup(func() { r.st.WithMessagingEvents(nil) })
	peer := peerNumber()
	conv := r.text(peer, "hello shop")
	r.admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", conv, "/claim"), nil)
	sent := r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": "hi"}).json(t)
	r.admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", conv, "/status"), map[string]any{"status": "resolved"})
	got := strings.Join(sink.types(conv), ",")
	if got != "+message.received,conversation.assigned,message.sent,conversation.resolved" {
		t.Fatalf("events = %s", got)
	}
	sink.mu.Lock()
	first := sink.got[0]
	sink.mu.Unlock()
	d, found, err := r.st.InboxEventData(ctx, first)
	if err != nil || !found || d.ChannelID != r.channel || d.Address != inboxDID || d.ChannelKind != "sms" || d.Provider != "sip_trunk" ||
		d.Direction != "in" || !strings.HasPrefix(d.PeerKey, "pk_") || d.PeerKind != "phone" || d.Peer != "" || d.Body != "" {
		t.Fatalf("event data = %+v, %v, %v", d, found, err)
	}
	if strings.Contains(d.PeerKey, strings.TrimPrefix(peer, "+")) {
		t.Fatalf("peerKey %q carries the number", d.PeerKey)
	}
	p, b, err := r.st.InboxEventContent(ctx, conv, first.MessageID, true, true)
	if err != nil || p == "" || b != "hello shop" {
		t.Fatalf("content = %q %q %v", p, b, err)
	}
	note := r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", conv, "/notes"), map[string]any{"body": "internal"}).json(t)
	if _, b, _ := r.st.InboxEventContent(ctx, conv, int64(note["id"].(float64)), false, true); b != "" {
		t.Fatalf("a note's text was given: %q", b)
	}
	sentID := int64(sent["id"].(float64))
	r.admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages/")+itoa(sentID)+"/redact", nil)
	if _, b, _ := r.st.InboxEventContent(ctx, conv, sentID, false, true); b != "" {
		t.Fatalf("a redacted text was given: %q", b)
	}
	ev, _, _ := r.st.InboxEventData(ctx, messaging.Event{Type: events.ConversationAssigned, ConversationID: conv, AssigneeUserID: 77})
	if ev.AssigneeUserID != 77 {
		t.Errorf("an assignment event's assignee = %d", ev.AssigneeUserID)
	}
	if _, found, err := r.st.InboxEventData(ctx, messaging.Event{Type: events.MessageSent, ConversationID: 999999}); found || err != nil {
		t.Errorf("a missing conversation = %v, %v", found, err)
	}
}

// TestInboxEventsHidden fails if the event history gives the inbox's events
// to a reader without the messages scope or below operator, or keeps them
// from an administrator.
func TestInboxEventsHidden(t *testing.T) {
	e := newEvEnv(t)
	e.mem.Append(callEvent(events.CallRinging, "102"))
	e.mem.Append(events.Event{APIVersion: events.APIVersion, ID: "evt_inbox_1", Type: events.MessageReceived, CreatedAt: time.Now().UTC(),
		Data: events.Data{Inbox: &events.InboxData{ConversationID: 4, MessageID: 5, ChannelKind: "sms", Direction: "in", PeerKey: "pk_1"}}})
	get := func(who, bearer string) []string {
		t.Helper()
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events", who, bearer))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct{ Items []events.Entry }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		var ts []string
		for _, x := range out.Items {
			ts = append(ts, x.Type)
		}
		return ts
	}
	if got := strings.Join(get("carol", ""), ","); got != "call.ringing,message.received" {
		t.Errorf("an administrator's history = %s", got)
	}
	if got := strings.Join(get("", "tok-events-102"), ","); got != "call.ringing" {
		t.Errorf("a token without the messages scope sees %s", got)
	}
	if got := strings.Join(get("dave", ""), ","); strings.Contains(got, "message.") {
		t.Errorf("a viewer sees %s", got)
	}
}
