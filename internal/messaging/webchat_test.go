package messaging_test

// The LAN chat widget's store against PostgreSQL: sites, visitors, the one
// conversation a visitor has, the caps, what a visitor sees, blocks, email
// replies, retention and erase. They skip without HELLO_TEST_DATABASE_URL.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/store"
)

var chatSeq atomic.Int64

func (r *rig) site(name string, mod func(*store.ChatSiteInput)) store.ChatSite {
	r.t.Helper()
	in := store.ChatSiteInput{Name: name, Enabled: true, AllowedOrigins: []string{"https://intranet.lan"}, Accent: "#2563eb", Position: "right",
		Collect: "off", OfflineMode: "collect", Challenge: "none", PowBits: 16, MaxOpenUnassigned: 200}
	if mod != nil {
		mod(&in)
	}
	s, err := r.st.CreateChatSite(context.Background(), "test", in)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// visitor opens a session on site and returns the visitor's id and token.
func (r *rig) visitor(site store.ChatSite, name, email string) (int64, string) {
	r.t.Helper()
	res, err := r.st.ChatSession(context.Background(), store.ChatSessionInput{Site: site, IPHash: []byte("iphash-" + name), Name: name, Email: email, Consent: name != "" || email != ""})
	if err != nil || res.Closed || !res.Created {
		r.t.Fatalf("session: %+v %v", res, err)
	}
	return res.VisitorID, res.Token
}

func (r *rig) say(site store.ChatSite, visitor int64, text string) store.ChatSendResult {
	r.t.Helper()
	res, err := r.st.ChatSend(context.Background(), store.ChatSendInput{Site: site, VisitorID: visitor, Text: text, ClientMsgID: fmt.Sprintf("c-%d", chatSeq.Add(1))})
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

// TestChatSiteLifecycle fails if a site does not make one channel of kind
// webchat and one address, returns its Turnstile secret, shares a name, is
// deleted with history, or can have a second address.
func TestChatSiteLifecycle(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Help desk", func(in *store.ChatSiteInput) {
		in.Challenge = "turnstile"
		in.TurnstileSiteKey = "0xKEY"
		sec := "super-secret-value"
		in.TurnstileSecret = &sec
	})
	if len(site.Key) != 24 || !site.Enabled || !site.HasTurnstileSecret || site.Conversations != 0 {
		t.Fatalf("site = %+v", site)
	}
	if got, err := r.st.ChatTurnstileSecret(ctx, site.ID); err != nil || got != "super-secret-value" {
		t.Fatalf("secret = %q, %v", got, err)
	}
	var enc []byte
	if err := r.db.QueryRowContext(ctx, `SELECT turnstile_secret_enc FROM chat_sites WHERE id = $1`, site.ID).Scan(&enc); err != nil || bytes.Contains(enc, []byte("super-secret")) {
		t.Fatalf("the secret is stored in the clear: %v", err)
	}
	if n := r.count(`SELECT count(*) FROM msg_channels WHERE kind = 'webchat' AND provider = 'chat' AND id = $1`, site.ChannelID); n != 1 {
		t.Fatalf("channels = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM msg_addresses WHERE channel_id = $1 AND address = $2 AND route_kind = 'inbox'`, site.ChannelID, site.Key); n != 1 {
		t.Fatalf("addresses = %d", n)
	}
	if _, err := r.st.CreateChatSite(ctx, "test", store.ChatSiteInput{Name: "HELP DESK", Enabled: true, Accent: "#000000", Position: "left", Collect: "off", OfflineMode: "collect", Challenge: "none", PowBits: 14, MaxOpenUnassigned: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a duplicate name: %v", err)
	}
	if _, err := r.st.CreateChatSite(ctx, "test", store.ChatSiteInput{Name: "T2", Enabled: true, Accent: "#000000", Position: "left", Collect: "off", OfflineMode: "collect", Challenge: "turnstile", PowBits: 14, MaxOpenUnassigned: 1}); err == nil {
		t.Fatal("a turnstile site with no keys was accepted")
	}
	if _, err := r.st.CreateMsgAddress(ctx, "test", store.MsgAddressInput{ChannelID: site.ChannelID, Address: "+3270555555", Key: "3270555555", Enabled: true}); err == nil {
		t.Fatal("a second address on a chat site was accepted")
	}
	// An update keeps the key and the stored secret when none is sent.
	in := store.ChatSiteInput{Name: "Help desk 2", Enabled: false, Accent: "#112233", Position: "left", Collect: "required", OfflineMode: "hidden", Challenge: "turnstile",
		TurnstileSiteKey: "0xKEY", PowBits: 18, MaxOpenUnassigned: 5, Transcripts: true, StripLinks: true,
		Hours: messaging.Hours{Zone: "UTC", Days: []messaging.HoursDay{{Day: 1, From: "09:00", To: "17:00"}}}}
	up, err := r.st.UpdateChatSite(ctx, "test", site.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if up.Key != site.Key || up.Enabled || !up.HasTurnstileSecret || up.Name != "Help desk 2" || len(up.Hours.Days) != 1 || up.Collect != "required" {
		t.Fatalf("updated = %+v", up)
	}
	if n := r.count(`SELECT count(*) FROM msg_addresses WHERE id = $1 AND NOT enabled`, site.AddressID); n != 1 {
		t.Fatal("disabling a site must disable its address")
	}
	clear := ""
	in.TurnstileSecret = &clear
	in.Challenge = "pow"
	if up, err = r.st.UpdateChatSite(ctx, "test", site.ID, in); err != nil || up.HasTurnstileSecret {
		t.Fatalf("clearing the secret: %+v %v", up, err)
	}
	byKey, err := r.st.ChatSiteByKey(ctx, site.Key)
	if err != nil || byKey.ID != site.ID {
		t.Fatalf("by key: %v", err)
	}
	if _, err := r.st.ChatSiteByKey(ctx, "ffffffffffffffffffffffff"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}
	// With history it is not deleted.
	in.Enabled = true
	up, _ = r.st.UpdateChatSite(ctx, "test", site.ID, in)
	v, _ := r.visitor(up, "", "")
	r.say(up, v, "hi")
	err = r.st.DeleteChatSite(ctx, "test", site.ID)
	var used *store.InUseError
	if !errors.As(err, &used) {
		t.Fatalf("delete with history: %v", err)
	}
	empty := r.site("Empty", nil)
	if err := r.st.DeleteChatSite(ctx, "test", empty.ID); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM msg_channels WHERE id = $1`, empty.ChannelID); n != 0 {
		t.Fatal("the channel stayed after its site was deleted")
	}
	if err := r.st.DeleteChatSite(ctx, "test", empty.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

// TestChatVisitorConversation fails if a visitor's message does not open one
// conversation of kind visitor for supervisors, a retried client id makes a
// second message, the visitor's token is stored, text outside the visitor's
// view (notes, system lines) reaches them, or an agent's reply is named.
func TestChatVisitorConversation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Support", nil)
	vid, token := r.visitor(site, "Vic", "vic@example.com")
	var stored []byte
	if err := r.db.QueryRowContext(ctx, `SELECT token_hash FROM chat_visitors WHERE id = $1`, vid).Scan(&stored); err != nil || len(stored) != 32 || bytes.Contains(stored, []byte(token[:8])) {
		t.Fatalf("token hash = %x, %v", stored, err)
	}
	auth, err := r.st.ChatAuth(ctx, site.ID, token, nil)
	if err != nil || auth.ID != vid || auth.Blocked || auth.Name != "Vic" || auth.Email != "vic@example.com" {
		t.Fatalf("auth = %+v %v", auth, err)
	}
	if _, err := r.st.ChatAuth(ctx, site.ID, token+"x", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a wrong token: %v", err)
	}
	if _, err := r.st.ChatAuth(ctx, site.ID+1, token, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another site's token: %v", err)
	}

	in := store.ChatSendInput{Site: site, VisitorID: vid, Text: "I need help", ClientMsgID: "m1"}
	first, err := r.st.ChatSend(ctx, in)
	if err != nil || first.MessageID == 0 || first.ConversationID == 0 || first.Duplicate {
		t.Fatalf("first = %+v %v", first, err)
	}
	again, err := r.st.ChatSend(ctx, in)
	if err != nil || !again.Duplicate || again.MessageID != first.MessageID {
		t.Fatalf("retry = %+v %v", again, err)
	}
	second := r.say(site, vid, "second line")
	if second.ConversationID != first.ConversationID {
		t.Fatal("a visitor has one open conversation")
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE peer_key = $1 AND peer_kind = 'visitor' AND channel_id = $2`, fmt.Sprintf("visitor:%d", vid), site.ChannelID); n != 1 {
		t.Fatalf("conversations = %d", n)
	}
	c, err := r.st.GetConversation(ctx, r.admAcc(), first.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if c.ChannelKind != "webchat" || c.PeerKind != "visitor" || c.PeerName != "Vic" || c.PeerNameSource != "visitor" || c.Status != "open" || c.Unread != 2 ||
		c.Chat == nil || c.Chat.Email != "vic@example.com" || c.Chat.Offline || c.Chat.SiteID != site.ID || c.Chat.Blocked {
		t.Fatalf("conversation = %+v chat=%+v", c, c.Chat)
	}
	pool, _, err := r.st.ListConversations(ctx, r.admAcc(), store.ConvFilter{View: "unassigned"}, 50)
	if err != nil || len(pool) != 1 {
		t.Fatalf("the unassigned pool: %v %v", pool, err)
	}

	// An agent's reply: stored sent, no SIP queue, anonymous to the visitor.
	if _, err := r.st.AssignConversation(ctx, r.admAcc(), first.ConversationID, r.agent); err != nil {
		t.Fatal(err)
	}
	msg, created, err := r.st.SendConversationText(ctx, r.agentAcc(), store.SendInput{ConversationID: first.ConversationID, Body: "How can I help?", ClientMsgID: "r1"})
	if err != nil || !created || msg.Status != "sent" {
		t.Fatalf("reply = %+v %v %v", msg, created, err)
	}
	if n := r.count(`SELECT count(*) FROM messages WHERE conversation_message_id = $1`, msg.ID); n != 0 {
		t.Fatal("a chat reply went through the SIP text queue")
	}
	if _, err := r.st.AddNote(ctx, r.agentAcc(), first.ConversationID, "customer seems upset"); err != nil {
		t.Fatal(err)
	}
	lines, err := r.st.ChatHistory(ctx, vid, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.From+":"+l.Text)
	}
	if strings.Join(got, "|") != "visitor:I need help|visitor:second line|agent:How can I help?" {
		t.Fatalf("what the visitor sees = %v", got)
	}
	evs, err := r.st.ChatEvents(ctx, vid, 0, 100)
	if err != nil || len(evs) != 1 || evs[0].Type != "message" || evs[0].Line == nil || evs[0].Line.Text != "How can I help?" || evs[0].Line.From != "agent" {
		t.Fatalf("events = %+v %v", evs, err)
	}
	// A retried reply is the same message.
	if dup, created, err := r.st.SendConversationText(ctx, r.agentAcc(), store.SendInput{ConversationID: first.ConversationID, Body: "How can I help?", ClientMsgID: "r1"}); err != nil || created || dup.ID != msg.ID {
		t.Fatalf("retried reply = %+v %v %v", dup, created, err)
	}
	// Reading and typing and resolving reach the visitor's stream.
	if err := r.st.MarkConversationRead(ctx, r.agentAcc(), first.ConversationID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.ChatAgentTyping(ctx, r.agentAcc(), first.ConversationID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.ChatAgentTyping(ctx, r.agentAcc(), first.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetConversationStatus(ctx, r.agentAcc(), first.ConversationID, "resolved", nil); err != nil {
		t.Fatal(err)
	}
	evs, _ = r.st.ChatEvents(ctx, vid, evs[0].ID, 100)
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "read,typing,closed" {
		t.Fatalf("event types = %v (typing is coalesced to one per 3 seconds)", types)
	}
	// Writing again within reopen_days reopens the same conversation.
	back := r.say(site, vid, "one more thing")
	if back.ConversationID != first.ConversationID {
		t.Fatal("a resolved chat within reopen_days must reopen")
	}
	// The visitor ends the chat.
	if err := r.st.ChatEnd(ctx, site, vid); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND status = 'resolved'`, first.ConversationID); n != 1 {
		t.Fatal("ending the chat must resolve the conversation")
	}
	if err := r.st.ChatEnd(ctx, site, vid); err != nil {
		t.Fatalf("ending twice: %v", err)
	}
	if err := r.st.ChatTyping(ctx, site, vid); err != nil {
		t.Fatalf("typing with no open chat: %v", err)
	}
	// Another visitor's chat is not mine.
	other, _ := r.visitor(site, "Other", "")
	if hist, _ := r.st.ChatHistory(ctx, other, 0, 100); len(hist) != 0 {
		t.Fatalf("a new visitor sees %v", hist)
	}
	// A session resumes with the token and reports the open conversation.
	rs, err := r.st.ChatSession(ctx, store.ChatSessionInput{Site: site, Token: token})
	if err != nil || rs.Created || rs.Token != "" || rs.VisitorID != vid {
		t.Fatalf("resume = %+v %v", rs, err)
	}
}

// TestChatCaps fails if a visitor can send more than 200 messages before an
// agent replies, the unassigned cap is not applied to a new live chat, an
// offline chat without an email address is taken, or an erased or spam chat
// reaches an agent.
func TestChatCaps(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	t.Run("unanswered", func(t *testing.T) {
		site := r.site("Caps", nil)
		v, _ := r.visitor(site, "", "")
		for i := 0; i < messaging.MaxChatUnanswered; i++ {
			r.say(site, v, "x")
		}
		_, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "one too many", ClientMsgID: "over"})
		if !errors.Is(err, store.ErrChatLimit) {
			t.Fatalf("message 201 before any reply: %v", err)
		}
	})

	t.Run("queue full", func(t *testing.T) {
		site := r.site("Full", func(in *store.ChatSiteInput) { in.MaxOpenUnassigned = 2 })
		for i := 0; i < 2; i++ {
			v, _ := r.visitor(site, fmt.Sprintf("v%d", i), "")
			r.say(site, v, "hi")
		}
		v, _ := r.visitor(site, "late", "")
		_, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "hi", ClientMsgID: "late"})
		if !errors.Is(err, store.ErrChatFull) {
			t.Fatalf("a third live chat over a cap of 2: %v", err)
		}
		on, reason, err := r.st.ChatAvailability(ctx, site, time.Now())
		if err != nil || on {
			t.Fatalf("availability = %v %q %v", on, reason, err)
		}
		// An offline chat does not count against the live queue.
		res, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "message left", ClientMsgID: "off1", Offline: true})
		var verr *store.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("an offline chat with no email: %+v %v", res, err)
		}
	})

	t.Run("offline with an email", func(t *testing.T) {
		site := r.site("Offline", nil)
		v, _ := r.visitor(site, "Olga", "olga@example.com")
		res, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "leaving a message", ClientMsgID: "o1", Offline: true})
		if err != nil || res.ConversationID == 0 {
			t.Fatalf("%+v %v", res, err)
		}
		if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND offline`, res.ConversationID); n != 1 {
			t.Fatal("the conversation must be flagged offline")
		}
	})

	t.Run("hidden site starts nothing", func(t *testing.T) {
		site := r.site("Hidden", nil)
		v, _ := r.visitor(site, "", "")
		res, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "x", ClientMsgID: "h1", NoNew: true})
		if err != nil || !res.Closed || res.MessageID != 0 {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("spam and disabled", func(t *testing.T) {
		site := r.site("Spam", nil)
		v, _ := r.visitor(site, "", "")
		res := r.say(site, v, "buy now")
		if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), res.ConversationID, "spam", nil); err != nil {
			t.Fatal(err)
		}
		again, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "buy more", ClientMsgID: "s2"})
		if err != nil || !again.Closed || again.MessageID != 0 {
			t.Fatalf("a spam chat must stay closed: %+v %v", again, err)
		}
		if n := r.count(`SELECT count(*) FROM conversations WHERE channel_id = $1`, site.ChannelID); n != 1 {
			t.Fatalf("conversations = %d", n)
		}
		off, err := r.st.UpdateChatSite(ctx, "test", site.ID, store.ChatSiteInput{Name: "Spam", Enabled: false, Accent: "#2563eb", Position: "right", Collect: "off", OfflineMode: "collect", Challenge: "none", PowBits: 16, MaxOpenUnassigned: 200})
		if err != nil {
			t.Fatal(err)
		}
		w, _ := r.visitor(off, "w", "")
		if _, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: off, VisitorID: w, Text: "x", ClientMsgID: "d1"}); !errors.Is(err, store.ErrChannelUnavailable) {
			t.Fatalf("a disabled site took a message: %v", err)
		}
	})
}

// TestChatAvailability fails if the chat is online outside its hours or with
// no supervisor present.
func TestChatAvailability(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	hours := messaging.Hours{Zone: "UTC", Days: []messaging.HoursDay{{Day: 1, From: "09:00", To: "17:00"}}}
	site := r.site("Hours", func(in *store.ChatSiteInput) { in.Hours = hours })
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	if on, reason, _ := r.st.ChatAvailability(ctx, site, monday.Add(8*time.Hour)); on || reason != "hours" {
		t.Fatalf("after hours: %v %q", on, reason)
	}
	if on, reason, _ := r.st.ChatAvailability(ctx, site, monday); on || reason != "no_agent" {
		t.Fatalf("nobody present: %v %q", on, reason)
	}
	if err := r.st.TouchInboxPresence(ctx, r.admin); err != nil {
		t.Fatal(err)
	}
	if on, reason, err := r.st.ChatAvailability(ctx, site, monday); err != nil || !on {
		t.Fatalf("a supervisor present: %v %q %v", on, reason, err)
	}
	// A plain agent's presence does not make the pool answerable.
	if _, err := r.db.ExecContext(ctx, `DELETE FROM inbox_presence`); err != nil {
		t.Fatal(err)
	}
	if err := r.st.TouchInboxPresence(ctx, r.agent); err != nil {
		t.Fatal(err)
	}
	if on, _, _ := r.st.ChatAvailability(ctx, site, monday); on {
		t.Fatal("a user with no supervisor role counted as available")
	}
}

// TestChatBlocks fails if a block does not stop the visitor, the same-day
// address hash, or lapses early, if blocking leaves the conversation open, a
// block is allowed on an SMS conversation, or a lifted block still holds.
func TestChatBlocks(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Blocks", nil)
	ip := []byte("ip-hash-of-today")
	res, err := r.st.ChatSession(ctx, store.ChatSessionInput{Site: site, IPHash: ip, Name: "Troll"})
	if err != nil {
		t.Fatal(err)
	}
	msg := r.say(site, res.VisitorID, "spam spam")
	if _, err := r.st.BlockChatVisitor(ctx, r.agentAcc(), msg.ConversationID, time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an agent blocking a chat they do not reach: %v", err)
	}
	blk, err := r.st.BlockChatVisitor(ctx, r.admAcc(), msg.ConversationID, 48*time.Hour)
	if err != nil || blk.VisitorID == nil || *blk.VisitorID != res.VisitorID || !blk.ByIP || !blk.Until.After(time.Now().Add(47*time.Hour)) {
		t.Fatalf("block = %+v %v", blk, err)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND status = 'resolved'`, msg.ConversationID); n != 1 {
		t.Fatal("blocking must resolve the conversation")
	}
	a, err := r.st.ChatAuth(ctx, site.ID, res.Token, nil)
	if err != nil || !a.Blocked {
		t.Fatalf("the blocked visitor: %+v %v", a, err)
	}
	// Same address, new token: still blocked, and nothing is stored.
	before := r.count(`SELECT count(*) FROM chat_visitors`)
	again, err := r.st.ChatSession(ctx, store.ChatSessionInput{Site: site, IPHash: ip})
	if err != nil || !again.Closed {
		t.Fatalf("a new token from a blocked address: %+v %v", again, err)
	}
	if r.count(`SELECT count(*) FROM chat_visitors`) != before {
		t.Fatal("a blocked session stored a visitor")
	}
	// Another address is fine.
	if ok, err := r.st.ChatSession(ctx, store.ChatSessionInput{Site: site, IPHash: []byte("another")}); err != nil || ok.Closed {
		t.Fatalf("another address: %+v %v", ok, err)
	}
	// The address block never outlives the UTC day even with a long ttl.
	var until time.Time
	if err := r.db.QueryRowContext(ctx, `SELECT until FROM chat_blocks WHERE ip_hash IS NOT NULL`).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if next := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour); until.After(next.Add(time.Second)) {
		t.Fatalf("the address block lasts until %v, past the end of the UTC day %v", until, next)
	}
	blocks, err := r.st.ListChatBlocks(ctx, site.ID)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("blocks = %v %v", blocks, err)
	}
	if _, err := r.st.ListChatBlocks(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("blocks of an unknown site: %v", err)
	}
	for _, b := range blocks {
		if err := r.st.DeleteChatBlock(ctx, "test", b.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.st.DeleteChatBlock(ctx, "test", blocks[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lifting twice: %v", err)
	}
	if a, _ := r.st.ChatAuth(ctx, site.ID, res.Token, nil); a.Blocked {
		t.Fatal("a lifted block still holds")
	}
	// A block only applies to a chat.
	sms := r.ingest(customer, "hello").ConversationID
	if _, err := r.st.BlockChatVisitor(ctx, r.admAcc(), sms, time.Hour); !errors.Is(err, store.ErrNotChat) {
		t.Fatalf("blocking an SMS conversation: %v", err)
	}
	if err := r.st.ChatAgentTyping(ctx, r.admAcc(), sms); !errors.Is(err, store.ErrNotChat) {
		t.Fatalf("typing in an SMS conversation: %v", err)
	}
}

// TestChatIPHash fails if the address hash is not stable within a UTC day,
// does not change at midnight, or contains the address.
func TestChatIPHash(t *testing.T) {
	r := newRig(t)
	noon := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	a, _ := r.st.ChatIPHash("192.0.2.7", noon)
	b, _ := r.st.ChatIPHash("192.0.2.7", noon.Add(11*time.Hour))
	c, _ := r.st.ChatIPHash("192.0.2.7", noon.Add(13*time.Hour))
	d, _ := r.st.ChatIPHash("192.0.2.8", noon)
	if !bytes.Equal(a, b) || bytes.Equal(a, c) || bytes.Equal(a, d) || len(a) != 32 || bytes.Contains(a, []byte("192.0.2.7")) {
		t.Fatal("the address hash must be stable within the day, change at 00:00 UTC and tell addresses apart")
	}
}

// TestChatEmailReply fails if an offline chat cannot be answered by email, a
// live chat can, the reply is not kept, the visitor does not see it only once
// the email went out, or a retry sends twice.
func TestChatEmailReply(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Mail", nil)
	v, _ := r.visitor(site, "Olga", "olga@example.com")
	msg, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "please call back", ClientMsgID: "o1", Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	in := store.SendInput{ConversationID: msg.ConversationID, Body: "We will call you tomorrow.", ClientMsgID: "e1"}
	rep, created, err := r.st.PrepareChatEmailReply(ctx, r.admAcc(), in)
	if err != nil || !created || rep.To != "olga@example.com" || rep.SiteName != "Mail" || rep.Message.Status != "sending" {
		t.Fatalf("prepare = %+v %v %v", rep, created, err)
	}
	if hist, _ := r.st.ChatHistory(ctx, v, 0, 100); len(hist) != 1 {
		t.Fatalf("the visitor sees the reply before the email went out: %v", hist)
	}
	dup, created, err := r.st.PrepareChatEmailReply(ctx, r.admAcc(), in)
	if err != nil || created || dup.Message.ID != rep.Message.ID {
		t.Fatalf("retry = %+v %v %v", dup, created, err)
	}
	if err := r.st.FinishChatEmailReply(ctx, rep.Message.ID, true); err != nil {
		t.Fatal(err)
	}
	hist, _ := r.st.ChatHistory(ctx, v, 0, 100)
	if len(hist) != 2 || hist[1].From != "agent" || hist[1].Text != "We will call you tomorrow." {
		t.Fatalf("history = %v", hist)
	}
	if evs, _ := r.st.ChatEvents(ctx, v, 0, 10); len(evs) != 1 || evs[0].Type != "message" {
		t.Fatalf("events = %+v", evs)
	}
	if n := r.count(`SELECT count(*) FROM conversation_messages WHERE id = $1 AND status = 'sent'`, rep.Message.ID); n != 1 {
		t.Fatal("the reply must be sent")
	}
	// A failed email is kept as failed and is not shown to the visitor.
	rep2, _, err := r.st.PrepareChatEmailReply(ctx, r.admAcc(), store.SendInput{ConversationID: msg.ConversationID, Body: "second", ClientMsgID: "e2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.FinishChatEmailReply(ctx, rep2.Message.ID, false); err != nil {
		t.Fatal(err)
	}
	if hist, _ := r.st.ChatHistory(ctx, v, 0, 100); len(hist) != 2 {
		t.Fatalf("a failed reply shows: %v", hist)
	}
	// A live chat, an SMS and a visitor with no email are refused.
	live, _ := r.visitor(site, "Liv", "liv@example.com")
	lm := r.say(site, live, "hi")
	if _, _, err := r.st.PrepareChatEmailReply(ctx, r.admAcc(), store.SendInput{ConversationID: lm.ConversationID, Body: "x"}); !errors.Is(err, store.ErrChatNoEmail) {
		t.Fatalf("emailing a live chat: %v", err)
	}
	noMail, _ := r.visitor(site, "", "")
	nm, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: noMail, Text: "x", ClientMsgID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.PrepareChatEmailReply(ctx, r.admAcc(), store.SendInput{ConversationID: nm.ConversationID, Body: "x"}); !errors.Is(err, store.ErrChatNoEmail) {
		t.Fatalf("no email: %v", err)
	}
	sms := r.ingest(customer, "hello").ConversationID
	if _, _, err := r.st.PrepareChatEmailReply(ctx, r.admAcc(), store.SendInput{ConversationID: sms, Body: "x"}); !errors.Is(err, store.ErrNotChat) {
		t.Fatalf("emailing an SMS conversation: %v", err)
	}
	// An agent who does not reach the conversation cannot answer it.
	if _, _, err := r.st.PrepareChatEmailReply(ctx, r.otherAcc(), store.SendInput{ConversationID: msg.ConversationID, Body: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another agent: %v", err)
	}
	// A transcript carries the visitor's own lines and their address.
	email, lines, err := r.st.ChatTranscript(ctx, v)
	if err != nil || email != "olga@example.com" || len(lines) != 2 {
		t.Fatalf("transcript = %q %v %v", email, lines, err)
	}
}

// TestChatSendIdempotentAcrossPods fails if the same client id sent at once
// to two pods makes two messages, or messages of one new visitor make more
// than one conversation.
func TestChatSendIdempotentAcrossPods(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("HA", nil)
	pods := []*store.Store{r.st, r.newStoreOn()}
	v, _ := r.visitor(site, "", "")
	var wg sync.WaitGroup
	results := make([]store.ChatSendResult, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = pods[i%2].ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v, Text: "same", ClientMsgID: "dup"})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if n := r.count(`SELECT count(*) FROM conversation_messages WHERE channel_id = $1 AND direction = 'in'`, site.ChannelID); n != 1 {
		t.Fatalf("messages = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE channel_id = $1`, site.ChannelID); n != 1 {
		t.Fatalf("conversations = %d", n)
	}
	// Different messages of one new visitor, at once.
	w, _ := r.visitor(site, "", "")
	for i := range 8 {
		wg.Go(func() {
			_, errs[i] = pods[i%2].ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: w, Text: "x", ClientMsgID: fmt.Sprintf("m%d", i)})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE peer_key = $1`, fmt.Sprintf("visitor:%d", w)); n != 1 {
		t.Fatalf("conversations of one visitor = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id WHERE c.peer_key = $1`, fmt.Sprintf("visitor:%d", w)); n != 8 {
		t.Fatalf("messages = %d", n)
	}
}

// TestChatEventsResume fails if events come out of order or twice, or a
// visitor reads another visitor's events.
func TestChatEventsResume(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Events", nil)
	v1, _ := r.visitor(site, "", "")
	v2, _ := r.visitor(site, "", "")
	c1 := r.say(site, v1, "one").ConversationID
	c2 := r.say(site, v2, "two").ConversationID
	for i := 0; i < 5; i++ {
		if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: c1, Body: fmt.Sprintf("a%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: c2, Body: "for two"}); err != nil {
		t.Fatal(err)
	}
	all, err := r.st.ChatEvents(ctx, v1, 0, 100)
	if err != nil || len(all) != 5 {
		t.Fatalf("events = %v %v", all, err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID || all[i].Line.Text != fmt.Sprintf("a%d", i) {
			t.Fatalf("order: %+v", all)
		}
	}
	rest, _ := r.st.ChatEvents(ctx, v1, all[2].ID, 100)
	if len(rest) != 2 || rest[0].Line.Text != "a3" || rest[1].Line.Text != "a4" {
		t.Fatalf("resume from the third: %+v", rest)
	}
	if latest, _ := r.st.ChatLatestEventID(ctx, v1); latest != all[4].ID {
		t.Fatalf("latest = %d", latest)
	}
	for _, e := range all {
		if e.Line.Text == "for two" {
			t.Fatal("a visitor got another visitor's event")
		}
	}
}

// TestChatErasePurgeAndRetention fails if an erase leaves a visitor, a
// message or an event, a site's own retention is ignored, an open chat is
// purged by age, or visitors with no chat are kept past 30 days.
func TestChatErasePurgeAndRetention(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	short := 1
	site := r.site("Retention", func(in *store.ChatSiteInput) { in.RetentionDays = &short })
	longSite := r.site("Long", nil)

	done, _ := r.visitor(site, "Done", "")
	dc := r.say(site, done, "bye").ConversationID
	open, _ := r.visitor(site, "Open", "")
	oc := r.say(site, open, "still here").ConversationID
	other, _ := r.visitor(longSite, "Other", "")
	ot := r.say(longSite, other, "kept").ConversationID
	for _, id := range []int64{dc, ot} {
		if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), id, "resolved", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET resolved_at = now() - interval '3 days', last_message_at = now() - interval '3 days' WHERE id IN ($1, $2)`, dc, ot); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET last_message_at = now() - interval '400 days' WHERE id = $1`, oc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.MaintainMessaging(ctx); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1`, dc); n != 0 {
		t.Fatal("a chat past its site's retention was kept")
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1`, oc); n != 1 {
		t.Fatal("an open chat was purged by age")
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1`, ot); n != 1 {
		t.Fatal("a chat within the default chat retention was purged")
	}
	// Visitors without a conversation go after 30 days.
	if _, err := r.db.ExecContext(ctx, `UPDATE chat_visitors SET last_seen = now() - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.MaintainMessaging(ctx); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM chat_visitors WHERE id = $1`, done); n != 0 {
		t.Fatal("a visitor with no chat was kept past 30 days")
	}
	if n := r.count(`SELECT count(*) FROM chat_visitors WHERE id IN ($1, $2)`, open, other); n != 2 {
		t.Fatal("a visitor with a chat was purged")
	}
	// Old events and lapsed blocks.
	r.say(site, open, "again")
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: oc, Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE chat_events SET at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.MaintainMessaging(ctx); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM chat_events`); n != 0 {
		t.Fatal("events older than 7 days were kept")
	}

	// Erase: the visitor, the chat, the messages and the events go.
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: oc, Body: "last words"}); err != nil {
		t.Fatal(err)
	}
	rep, err := r.st.ErasePeer(ctx, "test", fmt.Sprintf("visitor:%d", open))
	if err != nil || rep.Conversations != 1 || rep.Messages < 2 {
		t.Fatalf("erase = %+v %v", rep, err)
	}
	if n := r.count(`SELECT count(*) FROM chat_visitors WHERE id = $1`, open); n != 0 {
		t.Fatalf("the visitor was kept: %d", n)
	}
	if n := r.count(`SELECT count(*) FROM chat_events WHERE visitor_id = $1`, open); n != 0 {
		t.Fatalf("events were kept: %d", n)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE peer_key = $1`, fmt.Sprintf("visitor:%d", open)); n != 0 {
		t.Fatalf("conversations were kept: %d", n)
	}
	if n := r.count(`SELECT count(*) FROM chat_visitors WHERE id = $1`, other); n != 1 {
		t.Fatal("an erase removed another visitor")
	}
}

// TestChatNoContentInAudit fails if a visitor's text, name or email reaches an
// audit row.
func TestChatNoContentInAudit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	site := r.site("Audit", nil)
	v, _ := r.visitor(site, "Secret Name", "secret.person@example.com")
	c := r.say(site, v, "secret words").ConversationID
	if _, err := r.st.BlockChatVisitor(ctx, r.admAcc(), c, time.Hour); err != nil {
		t.Fatal(err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT actor || ' ' || action || ' ' || resource || ' ' || resource_id FROM audit_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"secret words", "Secret Name", "secret.person"} {
			if strings.Contains(s, bad) {
				t.Fatalf("audit row %q contains %q", s, bad)
			}
		}
	}
}
