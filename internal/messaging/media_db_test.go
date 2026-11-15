package messaging_test

// The attachment pipeline against PostgreSQL (spec messaging-channels M-18 to
// M-20): inbound carrier media downloaded by the worker, who may read an
// attachment, a chat's uploads, the public links, retention, the sweeper and
// erase, and what a desk phone is told. They skip without
// HELLO_TEST_DATABASE_URL.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/media"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

var (
	dbPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1, 2, 3}, 40)...)
	dbPDF = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n%%EOF\n")
)

// mediaCarrier is a carrier whose media are served from a map by URL.
type mediaCarrier struct {
	files map[string][]byte
	gone  map[string]bool
	// during, when set, runs while a download is under way.
	during func()
}

func (mediaCarrier) Kind() string                   { return "fake" }
func (mediaCarrier) Caps() provider.Caps            { return provider.Caps{} }
func (mediaCarrier) Validate(context.Context) error { return nil }
func (mediaCarrier) Status(context.Context, string) (provider.State, error) {
	return provider.State{}, nil
}
func (mediaCarrier) Numbers(context.Context) ([]provider.Number, error) { return nil, nil }
func (mediaCarrier) Send(context.Context, provider.Outbound) (provider.Sent, error) {
	return provider.Sent{}, nil
}
func (c mediaCarrier) FetchMedia(_ context.Context, ref provider.MediaRef) (provider.Media, error) {
	if c.gone[ref.URL] {
		return provider.Media{}, provider.ErrNotFound
	}
	if c.during != nil {
		c.during()
	}
	return provider.Media{Body: io.NopCloser(bytes.NewReader(c.files[ref.URL])), ContentType: ref.ContentType}, nil
}

// carrierText makes an inbound text on a Twilio channel as the public
// listener's ingest would (no carrier delivers inbound texts on the LAN), and
// returns its conversation and message.
func (r *rig) carrierText(peer, body string) (conv, msg int64) {
	r.t.Helper()
	ctx := context.Background()
	var ch, addr int64
	err := r.db.QueryRowContext(ctx, `SELECT c.id, a.id FROM msg_channels c JOIN msg_addresses a ON a.channel_id = c.id WHERE c.provider = 'twilio'`).Scan(&ch, &addr)
	if err != nil {
		c, err := r.st.CreateMsgChannel(ctx, "test", store.MsgChannelInput{Kind: "sms", Provider: "twilio", Name: "Twilio", Enabled: true,
			Credentials: &provider.Config{AccountSID: "AC" + strings.Repeat("ab", 16), AuthToken: "test-token"}})
		if err != nil {
			r.t.Fatal(err)
		}
		key, _ := phonebook.Key("+3270999000")
		suffix, _ := phonebook.Suffix(key)
		a, err := r.st.CreateMsgAddress(ctx, "test", store.MsgAddressInput{ChannelID: c.ID, Address: "+3270999000", Key: key, Suffix: suffix, Enabled: true})
		if err != nil {
			r.t.Fatal(err)
		}
		ch, addr = c.ID, a.ID
	}
	if err := r.db.QueryRowContext(ctx, `INSERT INTO conversations (channel_id, address_id, peer_key, peer_kind, last_inbound_at)
		VALUES ($1, $2, $3, 'phone', now()) RETURNING id`, ch, addr, peer).Scan(&conv); err != nil {
		r.t.Fatal(err)
	}
	if err := r.db.QueryRowContext(ctx, `INSERT INTO conversation_messages (conversation_id, channel_id, direction, sender_kind, body)
		VALUES ($1, $2, 'in', 'customer', $3) RETURNING id`, conv, ch, body).Scan(&msg); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET last_message_id = $2 WHERE id = $1`, conv, msg); err != nil {
		r.t.Fatal(err)
	}
	return conv, msg
}

func (r *rig) mediaWorker(objs media.Objects, c mediaCarrier) *media.Worker {
	return &media.Worker{Store: r.st, Objects: objs, New: func(string, provider.Config) (provider.SMSProvider, error) { return c, nil }}
}

// TestMediaPipelineStore fails if an inbound attachment is not downloaded,
// sniffed and stored by exactly one worker, a refused or vanished file is
// stored, someone who cannot see the conversation reads an attachment, a
// carrier conversation takes an agent's file, or a public link works after
// it expired or was used up (the store half of TestMediaPipeline).
func TestMediaPipelineStore(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	in := store.IngestResult{}
	in.ConversationID, in.MessageID = r.carrierText("+32475123456", "see the photo")
	if err := r.st.AddInboundMedia(ctx, in.MessageID, []provider.MediaRef{
		{URL: "https://m/1", ContentType: "image/png"},
		{URL: "https://m/svg", ContentType: "image/svg+xml"},
		{URL: "https://m/gone"},
	}); err != nil {
		t.Fatal(err)
	}
	// The message is visible at once, its attachments still downloading.
	msgs, _, err := r.st.ListConversationMessages(ctx, r.admAcc(), in.ConversationID, 0, 10)
	if err != nil || len(msgs) != 1 || len(msgs[0].Media) != 3 || msgs[0].Media[0].Status != media.StatusPending {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	objs := &media.MemObjects{}
	c := mediaCarrier{files: map[string][]byte{"https://m/1": dbPNG, "https://m/svg": []byte(`<svg/>`)}, gone: map[string]bool{"https://m/gone": true}}
	// Two workers ("pods") at once: each attachment is downloaded once.
	w1, w2 := r.mediaWorker(objs, c), r.newStoreOn()
	w2w := &media.Worker{Store: w2, Objects: objs, New: w1.New}
	done := make(chan int, 2)
	go func() { n, _ := w1.FetchOnce(ctx, 10); done <- n }()
	go func() { n, _ := w2w.FetchOnce(ctx, 10); done <- n }()
	if n := <-done + <-done; n != 3 {
		t.Fatalf("the two workers handled %d downloads, want 3", n)
	}
	msgs, _, _ = r.st.ListConversationMessages(ctx, r.admAcc(), in.ConversationID, 0, 10)
	got := msgs[0].Media
	if got[0].Status != media.StatusReady || got[0].ContentType != "image/png" || got[0].Size != int64(len(dbPNG)) ||
		got[1].Status != media.StatusRejected || got[1].Reason != media.ReasonActiveContent ||
		got[2].Status != media.StatusExpired || got[2].Reason != media.ReasonGone {
		t.Fatalf("after the download: %+v", got)
	}
	if objs.Len() != 1 {
		t.Fatalf("%d objects stored, want only the PNG", objs.Len())
	}
	var key, src string
	if err := r.db.QueryRowContext(ctx, `SELECT object_key, source_ref::text FROM message_media WHERE id = $1`, got[0].ID).Scan(&key, &src); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "msg/") || src != "{}" {
		t.Fatalf("key %q, source kept %q", key, src)
	}
	if n := r.count(`SELECT count(*) FROM conversation_events WHERE type = 'message.status' AND message_id = $1`, in.MessageID); n == 0 {
		t.Error("nobody was told the attachments arrived")
	}

	// Who may read it: a supervisor, and an agent only once the
	// conversation is theirs.
	if f, err := r.st.InboxMedia(ctx, r.admAcc(), got[0].ID); err != nil || f.Key != key || f.ContentType != "image/png" {
		t.Fatalf("supervisor: %+v, %v", f, err)
	}
	if _, err := r.st.InboxMedia(ctx, r.agentAcc(), got[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an agent who cannot see the conversation: %v", err)
	}
	if _, err := r.st.AssignConversation(ctx, r.admAcc(), in.ConversationID, r.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.InboxMedia(ctx, r.agentAcc(), got[0].ID); err != nil {
		t.Fatalf("the assignee: %v", err)
	}
	if _, err := r.st.InboxMedia(ctx, r.otherAcc(), got[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another agent: %v", err)
	}
	if _, err := r.st.InboxMedia(ctx, r.admAcc(), got[1].ID); !errors.Is(err, store.ErrMediaNotReady) {
		t.Fatalf("a refused attachment: %v", err)
	}

	// A carrier conversation carries no agent's file in this build, and
	// neither does a SIP trunk's.
	up := store.MediaUploadInput{Inspected: media.Inspected{ContentType: "image/png", Size: 3, SHA256: strings.Repeat("a", 64)}, Filename: "x.png"}
	if _, err := r.st.BeginInboxUpload(ctx, r.admAcc(), in.ConversationID, up); !errors.Is(err, store.ErrMediaUnsupported) {
		t.Fatalf("upload to an SMS conversation: %v", err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: in.ConversationID, Body: "x", MediaIDs: []int64{got[0].ID}}); !errors.Is(err, store.ErrMediaUnsupported) {
		t.Fatalf("send with media on an SMS conversation: %v", err)
	}

	// Public links: only an outbound file, 5 uses, 15 minutes.
	if _, _, err := r.st.MintMediaLink(ctx, got[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a link to an inbound file: %v", err)
	}
	var out int64
	if err := r.db.QueryRowContext(ctx, `INSERT INTO message_media (message_id, conversation_id, channel_id, direction, status, object_key,
		content_type, size, sha256, filename, attached_at) VALUES ($1, $2, $3, 'out', 'ready', 'msg/1/2026/9/abc', 'image/png', 3, $4, 'out.png', now())
		RETURNING id`, in.MessageID, in.ConversationID, r.channel.ID, strings.Repeat("b", 64)).Scan(&out); err != nil {
		t.Fatal(err)
	}
	tok, exp, err := r.st.MintMediaLink(ctx, out)
	if err != nil || time.Until(exp) > media.LinkTTL+time.Minute || time.Until(exp) < media.LinkTTL-time.Minute {
		t.Fatalf("mint = %v, %v", exp, err)
	}
	for i := 0; i < media.LinkMaxUses; i++ {
		if f, err := r.st.RedeemMediaLink(ctx, media.LinkHash(tok)); err != nil || f.Key != "msg/1/2026/9/abc" {
			t.Fatalf("use %d: %+v, %v", i+1, f, err)
		}
	}
	if _, err := r.st.RedeemMediaLink(ctx, media.LinkHash(tok)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a used-up link: %v", err)
	}
	tok2, _, _ := r.st.MintMediaLink(ctx, out)
	if _, err := r.db.ExecContext(ctx, `UPDATE media_links SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, media.LinkHash(tok2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.RedeemMediaLink(ctx, media.LinkHash(tok2)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an expired link: %v", err)
	}
	if n := r.count(`SELECT count(*) FROM media_links WHERE token_hash = convert_to($1, 'UTF8')`, tok); n != 0 {
		t.Fatal("a link token is stored as it is")
	}
}

// TestChatMediaStore fails if a visitor's upload does not join their
// message, an agent's upload goes to another conversation or is sent twice,
// a visitor reads a file that is not in their chat, or an attachment is
// missing from what the visitor and the agent see.
func TestChatMediaStore(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Help desk", func(in *store.ChatSiteInput) { in.Attachments = true })
	if !site.Attachments {
		t.Fatal("the attachments switch was not kept")
	}
	v1, _ := r.visitor(site, "Vic", "")
	v2, _ := r.visitor(site, "Val", "")
	in := store.MediaUploadInput{Inspected: media.Inspected{ContentType: "image/png", Size: int64(len(dbPNG)), SHA256: strings.Repeat("c", 64)}, Filename: "../../screen.png"}
	up, err := r.st.ChatBeginUpload(ctx, site, v1, in)
	if err != nil || !strings.HasPrefix(up.Key, "msg/") {
		t.Fatalf("begin = %+v, %v", up, err)
	}
	if _, err := r.st.FinishMediaUpload(ctx, up.ID); err != nil {
		t.Fatal(err)
	}
	// Another visitor cannot send it.
	if _, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v2, ClientMsgID: "x1", MediaIDs: []int64{up.ID}}); err == nil {
		t.Fatal("a visitor sent another visitor's upload")
	}
	res, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v1, Text: "", ClientMsgID: "x2", MediaIDs: []int64{up.ID}})
	if err != nil {
		t.Fatal(err)
	}
	lines, err := r.st.ChatHistory(ctx, v1, 0, 10)
	if err != nil || len(lines) != 1 || len(lines[0].Attachments) != 1 || lines[0].Attachments[0].Name != "screen.png" {
		t.Fatalf("history = %+v, %v", lines, err)
	}
	if f, err := r.st.ChatMedia(ctx, v1, up.ID); err != nil || f.Key != up.Key {
		t.Fatalf("own file: %+v, %v", f, err)
	}
	if _, err := r.st.ChatMedia(ctx, v2, up.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another visitor's file: %v", err)
	}

	// The agent sends a file back.
	other := r.say(site, v2, "hello").ConversationID
	agentUp, err := r.st.BeginInboxUpload(ctx, r.admAcc(), res.ConversationID, store.MediaUploadInput{
		Inspected: media.Inspected{ContentType: "application/pdf", Size: int64(len(dbPDF)), SHA256: strings.Repeat("d", 64)}, Filename: "guide.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.FinishMediaUpload(ctx, agentUp.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: other, Body: "x", MediaIDs: []int64{agentUp.ID}}); err == nil {
		t.Fatal("an upload was sent into another conversation")
	}
	if _, _, err := r.st.SendConversationText(ctx, r.otherAcc(), store.SendInput{ConversationID: res.ConversationID, Body: "x", MediaIDs: []int64{agentUp.ID}}); err == nil {
		t.Fatal("someone else's upload was sent")
	}
	m, created, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: res.ConversationID, Body: "", ClientMsgID: "a1", MediaIDs: []int64{agentUp.ID}})
	if err != nil || !created || len(m.Media) != 1 || m.Media[0].Filename != "guide.pdf" {
		t.Fatalf("send = %+v, %v", m, err)
	}
	// The retry returns the same message with its file, and sends nothing.
	again, created, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: res.ConversationID, Body: "", ClientMsgID: "a1", MediaIDs: []int64{agentUp.ID}})
	if err != nil || created || again.ID != m.ID || len(again.Media) != 1 {
		t.Fatalf("retry = %+v, %v, %v", again, created, err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: res.ConversationID, Body: "y", ClientMsgID: "a2", MediaIDs: []int64{agentUp.ID}}); err == nil {
		t.Fatal("an upload was sent twice")
	}
	if f, err := r.st.ChatMedia(ctx, v1, agentUp.ID); err != nil || f.Filename != "guide.pdf" {
		t.Fatalf("the visitor reads the agent's file: %+v, %v", f, err)
	}
	evs, err := r.st.ChatEvents(ctx, v1, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Line != nil && e.Line.ID == m.ID && len(e.Line.Attachments) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the visitor's stream does not carry the file: %+v", evs)
	}
	all, err := r.st.ConversationMedia(ctx, res.ConversationID)
	if err != nil || len(all) != 2 || all[0].Key == "" || all[1].SHA256 == "" {
		t.Fatalf("conversation media = %+v, %v", all, err)
	}

	// The logo.
	if s, err := r.st.SetChatSiteLogo(ctx, "test", site.ID, "image/png"); err != nil || !s.Logo || s.LogoType != "image/png" || s.LogoVersion == 0 {
		t.Fatalf("logo = %+v, %v", s, err)
	}
	if err := r.st.ClearChatSiteLogo(ctx, "test", site.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.ClearChatSiteLogo(ctx, "test", site.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("clearing no logo: %v", err)
	}
}

// TestMediaRetentionAndErase fails if a file past the media retention keeps
// its object, the message loses its row, a purged or erased conversation
// leaves an object or a row behind, an unsent upload outlives a day, or the
// erase report misses the files (spec M-19, M-20).
func TestMediaRetentionAndErase(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	objs := &media.MemObjects{}
	c := mediaCarrier{files: map[string][]byte{"https://m/a": dbPNG, "https://m/b": dbPDF}}
	w := r.mediaWorker(objs, c)

	var keep, gone, purge store.IngestResult
	keep.ConversationID, keep.MessageID = r.carrierText("+32475000066", "keep")
	if err := r.st.AddInboundMedia(ctx, keep.MessageID, []provider.MediaRef{{URL: "https://m/a"}}); err != nil {
		t.Fatal(err)
	}
	gone.ConversationID, gone.MessageID = r.carrierText("+32475000077", "erase me")
	if err := r.st.AddInboundMedia(ctx, gone.MessageID, []provider.MediaRef{{URL: "https://m/a"}, {URL: "https://m/b"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := w.FetchOnce(ctx, 10); err != nil || n != 3 || objs.Len() != 3 {
		t.Fatalf("fetch = %d, %v; %d objects", n, err, objs.Len())
	}

	// Retention: an old file goes, its message stays and says so.
	if _, err := r.db.ExecContext(ctx, `UPDATE message_media SET created_at = now() - interval '91 days' WHERE message_id = $1`, keep.MessageID); err != nil {
		t.Fatal(err)
	}
	if n, err := w.CollectOnce(ctx, 100); err != nil || n != 1 || objs.Len() != 2 {
		t.Fatalf("retention sweep = %d, %v; %d objects", n, err, objs.Len())
	}
	msgs, _, _ := r.st.ListConversationMessages(ctx, r.admAcc(), keep.ConversationID, 0, 10)
	if len(msgs) != 1 || len(msgs[0].Media) != 1 || msgs[0].Media[0].Status != media.StatusExpired || msgs[0].Media[0].Reason != media.ReasonRetention {
		t.Fatalf("after retention: %+v", msgs)
	}

	// Erase: the report names the files; removing them leaves nothing.
	var peerKey string
	if err := r.db.QueryRowContext(ctx, `SELECT peer_key FROM conversations WHERE id = $1`, gone.ConversationID).Scan(&peerKey); err != nil {
		t.Fatal(err)
	}
	rep, err := r.st.ErasePeer(ctx, "user:admin", peerKey)
	if err != nil || len(rep.Media) != 2 {
		t.Fatalf("erase = %+v, %v", rep, err)
	}
	for _, g := range rep.Media {
		if err := media.RemoveVerified(ctx, objs, g.Key); err != nil {
			t.Fatal(err)
		}
		if err := r.st.DropMedia(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if objs.Len() != 0 || r.count(`SELECT count(*) FROM message_media WHERE message_id IS NULL`) != 0 {
		t.Fatalf("after erase: %d objects, %d loose rows", objs.Len(), r.count(`SELECT count(*) FROM message_media WHERE message_id IS NULL`))
	}

	// A purge leaves orphans the sweeper removes; an unsent upload goes
	// after a day.
	purge.ConversationID, purge.MessageID = r.carrierText("+32475000088", "old")
	if err := r.st.AddInboundMedia(ctx, purge.MessageID, []provider.MediaRef{{URL: "https://m/b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.FetchOnce(ctx, 10); err != nil || objs.Len() != 1 {
		t.Fatalf("fetch: %v, %d", err, objs.Len())
	}
	if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), purge.ConversationID, "resolved", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET resolved_at = now() - interval '400 days' WHERE id = $1`, purge.ConversationID); err != nil {
		t.Fatal(err)
	}
	if m, err := r.st.MaintainMessaging(ctx); err != nil || m.Conversations != 1 {
		t.Fatalf("purge = %+v, %v", m, err)
	}
	if n := r.count(`SELECT count(*) FROM message_media WHERE message_id IS NULL AND attached_at IS NOT NULL`); n != 1 {
		t.Fatalf("%d orphans after the purge, want 1 (kept until its object is gone)", n)
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO message_media (channel_id, direction, status, object_key, uploader_user_id, created_at)
		VALUES ($1, 'out', 'ready', 'msg/1/2026/upload-1/x', $2, now() - interval '25 hours')`, r.channel.ID, r.admin); err != nil {
		t.Fatal(err)
	}
	_ = objs.Put(ctx, "msg/1/2026/upload-1/x", bytes.NewReader([]byte("x")), 1, "text/plain")
	objs.FailRemove = errors.New("minio down")
	if n, _ := w.CollectOnce(ctx, 100); n != 0 || r.count(`SELECT count(*) FROM message_media WHERE message_id IS NULL`) != 2 {
		t.Fatal("a row was dropped although its object could not be removed")
	}
	objs.FailRemove = nil
	if n, err := w.CollectOnce(ctx, 100); err != nil || n != 2 || objs.Len() != 0 || r.count(`SELECT count(*) FROM message_media WHERE message_id IS NULL`) != 0 {
		t.Fatalf("sweep = %d, %v; %d objects left", n, err, objs.Len())
	}
	if n := r.count(`SELECT count(*) FROM audit_events WHERE action LIKE '%png%' OR action LIKE '%msg/%'`); n != 0 {
		t.Fatalf("%d audit rows name a file", n)
	}
}

// TestDeskPhoneMediaNote fails if a desk phone is not told a customer's text
// came with attachments it cannot show.
func TestDeskPhoneMediaNote(t *testing.T) {
	d := newDeskRig(t)
	ctx := context.Background()
	in := d.ingest(customer, "two photos")
	if err := d.st.AddInboundMedia(ctx, in.MessageID, []provider.MediaRef{{URL: "https://m/1"}, {URL: "https://m/2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), in.ConversationID, d.agent); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := d.db.QueryRowContext(ctx, `SELECT body_text FROM messages WHERE recipient = $1 ORDER BY id DESC LIMIT 1`, d.ext).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != messaging.WithMediaNote("two photos", 2) {
		t.Fatalf("desk phone got %q", body)
	}
}

// TestMediaErasedDuringDownload fails if a file a download stores after an
// erase or a redaction took its attachment stays in the media store, if the
// worker removes a file another row of the message still names, or if the
// orphan sweep misses an old object no row names, removes one a row names,
// or removes one too young to be an orphan.
func TestMediaErasedDuringDownload(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	objs := &media.MemObjects{}

	t.Run("erase", func(t *testing.T) {
		conv, msg := r.carrierText("+32475000101", "photo")
		if err := r.st.AddInboundMedia(ctx, msg, []provider.MediaRef{{URL: "https://m/e"}}); err != nil {
			t.Fatal(err)
		}
		var peerKey string
		if err := r.db.QueryRowContext(ctx, `SELECT peer_key FROM conversations WHERE id = $1`, conv).Scan(&peerKey); err != nil {
			t.Fatal(err)
		}
		c := mediaCarrier{files: map[string][]byte{"https://m/e": dbPNG}, during: func() {
			// The erase and the API's removal of what it names (no file
			// yet: the row's key is empty) run while the file downloads.
			rep, err := r.st.ErasePeer(ctx, "user:admin", peerKey)
			if err != nil || len(rep.Media) != 1 || rep.Media[0].Key != "" {
				t.Errorf("erase = %+v, %v", rep, err)
			}
			for _, g := range rep.Media {
				if err := r.st.DropMedia(ctx, g); err != nil {
					t.Error(err)
				}
			}
		}}
		if n, err := r.mediaWorker(objs, c).FetchOnce(ctx, 10); err != nil || n != 1 {
			t.Fatalf("fetch = %d, %v", n, err)
		}
		if objs.Len() != 0 {
			t.Fatalf("%d files left in the media store after the erase", objs.Len())
		}
	})

	t.Run("redaction", func(t *testing.T) {
		conv, msg := r.carrierText("+32475000102", "photo")
		if err := r.st.AddInboundMedia(ctx, msg, []provider.MediaRef{{URL: "https://m/r"}}); err != nil {
			t.Fatal(err)
		}
		c := mediaCarrier{files: map[string][]byte{"https://m/r": dbPDF}, during: func() {
			// The redaction takes the attachment off the message; the
			// API's removal has not run yet, so the row is still there.
			if _, err := r.st.RedactConversationMessage(ctx, "user:admin", conv, msg); err != nil {
				t.Error(err)
			}
		}}
		if _, err := r.mediaWorker(objs, c).FetchOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		if objs.Len() != 0 {
			t.Fatalf("%d files left in the media store after the redaction", objs.Len())
		}
		if n := r.count(`SELECT count(*) FROM message_media WHERE object_key <> '' AND message_id IS NULL`); n != 0 {
			t.Fatalf("%d detached rows name a file", n)
		}
	})

	t.Run("a second worker", func(t *testing.T) {
		// A worker whose lease lapsed finishes after another stored the
		// same file under the same key: the file stays.
		_, msg := r.carrierText("+32475000103", "photo")
		if err := r.st.AddInboundMedia(ctx, msg, []provider.MediaRef{{URL: "https://m/s"}}); err != nil {
			t.Fatal(err)
		}
		c := mediaCarrier{files: map[string][]byte{"https://m/s": dbPNG}}
		jobs, err := r.st.ClaimMediaFetch(ctx, 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim = %v, %v", jobs, err)
		}
		if n, err := r.mediaWorker(objs, c).FetchOnce(ctx, 10); err != nil || n != 0 {
			t.Fatalf("fetch while claimed = %d, %v", n, err)
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE message_media SET next_attempt_at = now() WHERE id = $1`, jobs[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.mediaWorker(objs, c).FetchOnce(ctx, 10); err != nil || objs.Len() != 1 {
			t.Fatalf("fetch = %v, %d files", err, objs.Len())
		}
		var key string
		if err := r.db.QueryRowContext(ctx, `SELECT object_key FROM message_media WHERE id = $1`, jobs[0].ID).Scan(&key); err != nil || key == "" {
			t.Fatalf("key %q, %v", key, err)
		}
		late := media.FetchResult{Status: media.StatusReady, Key: key, Inspected: media.Inspected{ContentType: "image/png", Size: 1, SHA256: strings.Repeat("a", 64)}}
		if err := r.st.FinishMediaFetch(ctx, jobs[0], late); err != nil {
			t.Fatalf("the late worker's result: %v", err)
		}
		other := media.FetchResult{Status: media.StatusReady, Key: key + "x", Inspected: late.Inspected}
		if err := r.st.FinishMediaFetch(ctx, jobs[0], other); !errors.Is(err, media.ErrGone) {
			t.Fatalf("a late file no row names: %v", err)
		}
	})

	t.Run("orphan sweep", func(t *testing.T) {
		before := objs.Len()
		old := time.Now().Add(-2 * media.OrphanAge)
		orphans := []string{"msg/1/2026/424242/" + strings.Repeat("c", 64), "msg/1/2026/upload-424243/" + strings.Repeat("d", 64)}
		young := "msg/1/2026/424244/" + strings.Repeat("e", 64)
		logo := media.LogoKey(1)
		for _, k := range append(orphans, young, logo) {
			if err := objs.Put(ctx, k, bytes.NewReader([]byte("x")), 1, "text/plain"); err != nil {
				t.Fatal(err)
			}
		}
		// Everything is old but one; a key a row names stays whatever its
		// age.
		for k := range objs.M {
			objs.Modified[k] = old
		}
		objs.Modified[young] = time.Now()
		w := r.mediaWorker(objs, mediaCarrier{})
		n, err := w.SweepOrphans(ctx)
		if err != nil || n != len(orphans) {
			t.Fatalf("sweep = %d, %v", n, err)
		}
		for _, k := range orphans {
			if ok, _ := objs.Exists(ctx, k); ok {
				t.Errorf("orphan %s is still there", k)
			}
		}
		for _, k := range []string{young, logo} {
			if ok, _ := objs.Exists(ctx, k); !ok {
				t.Errorf("%s was removed", k)
			}
		}
		if objs.Len() != before+2 {
			t.Fatalf("%d objects, want the %d a row names and two more", objs.Len(), before)
		}
	})
}
