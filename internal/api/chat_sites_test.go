package api

// The chat site API against PostgreSQL: every documented status of every
// operation, the roles (only an administrator configures a site, an agent
// reaches only the chats they may), the email reply and what the API keeps
// private. The package's conformance validator checks each request and
// response against openapi.json.

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

type chatRig struct {
	*env
	admin, operator, agent, other, viewer *client
	agentID, otherID                      int64
	mu                                    sync.Mutex
	sent                                  []string
	failMail                              bool
}

func newChatRig(t *testing.T, smtp bool) *chatRig {
	t.Helper()
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	r := &chatRig{}
	cfg := Config{Live: noLive{}}
	if smtp {
		cfg.SMTPEnv = mailer.Settings{Host: "smtp.test", Port: 587, Security: mailer.SecuritySTARTTLS, From: "hello@example.com"}
		cfg.SMTPDeliver = func(_ context.Context, _ mailer.Settings, to string, msg []byte) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.failMail {
				return context.DeadlineExceeded
			}
			r.sent = append(r.sent, to+"\n"+string(msg))
			return nil
		}
	}
	r.env = newEnvConfig(t, cfg, box)
	r.admin = r.login()
	r.operator, _ = r.asRole("otto", auth.RoleOperator)
	r.agent, r.agentID = r.asRole("agnes", auth.RoleUser)
	r.other, r.otherID = r.asRole("olga", auth.RoleUser)
	r.viewer, _ = r.asRole("vera", auth.RoleViewer)
	return r
}

func (r *chatRig) createSite(name string, extra map[string]any) store.ChatSite {
	r.t.Helper()
	body := map[string]any{"name": name, "challenge": "none", "allowedOrigins": []string{"https://intranet.lan"}}
	for k, v := range extra {
		body[k] = v
	}
	got := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/chat/sites", body).json(r.t)
	s, err := r.st.GetChatSite(context.Background(), int64(got["id"].(float64)))
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// chat opens a visitor's chat on site and returns the conversation's id.
func (r *chatRig) chat(site store.ChatSite, name, email string, offline bool) int64 {
	r.t.Helper()
	ctx := context.Background()
	res, err := r.st.ChatSession(ctx, store.ChatSessionInput{Site: site, IPHash: []byte("ip-" + name), Name: name, Email: email, Consent: name != "" || email != ""})
	if err != nil {
		r.t.Fatal(err)
	}
	m, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: res.VisitorID, Text: "hello from " + name, ClientMsgID: "c-" + name + strconv.Itoa(len(name)), Offline: offline})
	if err != nil {
		r.t.Fatal(err)
	}
	return m.ConversationID
}

func (r *chatRig) assign(conv, user int64) {
	r.t.Helper()
	var admin int64
	if err := r.db.QueryRowContext(context.Background(), `SELECT id FROM users WHERE username = $1`, testUser).Scan(&admin); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.AssignConversation(context.Background(), store.InboxAccess{UserID: admin, All: true, Actor: "user:" + testUser, Name: testUser}, conv, user); err != nil {
		r.t.Fatal(err)
	}
}

// TestChatSitesAPI fails if a chat site route does not answer each
// documented status, shows the Turnstile secret, lets anyone but an
// administrator configure a site, or accepts a setting the widget cannot use.
func TestChatSitesAPI(t *testing.T) {
	r := newChatRig(t, false)
	admin := r.admin

	t.Run("create and read", func(t *testing.T) {
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/chat/sites", map[string]any{})
		for name, body := range map[string]map[string]any{
			"name":        {"name": ""},
			"accent":      {"name": "A1", "accent": "blue"},
			"position":    {"name": "A2", "position": "top"},
			"collect":     {"name": "A3", "collect": "always"},
			"origin":      {"name": "A4", "allowedOrigins": []string{"https://x.lan/path"}},
			"origin list": {"name": "A5", "allowedOrigins": []string{"https://x.lan;script-src *"}},
			"challenge":   {"name": "A6", "challenge": "captcha"},
			"bits":        {"name": "A7", "powBits": 30},
			"low bits":    {"name": "A8", "powBits": 3},
			"turnstile":   {"name": "A9", "challenge": "turnstile"},
			"hours":       {"name": "B1", "hours": map[string]any{"zone": "Nowhere/Land", "days": []map[string]any{{"day": 1, "from": "09:00", "to": "17:00"}}}},
			"privacy":     {"name": "B2", "privacyUrl": "javascript:alert(1)"},
			"cap":         {"name": "B3", "maxOpenUnassigned": 0},
			"retention":   {"name": "B4", "retentionDays": 9999},
			"unknown":     {"name": "B5", "surprise": true},
		} {
			if got := admin.do("POST", "/api/v1/messaging/chat/sites", body); got.code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s", name, got.code, got.body)
			}
		}
		site := admin.must(http.StatusCreated, "POST", "/api/v1/messaging/chat/sites", map[string]any{
			"name": "Support", "allowedOrigins": []string{"HTTPS://Intranet.LAN", "https://intranet.lan", "http://10.0.0.5:8080"}, "greeting": "Hi!",
			"challenge": "turnstile", "turnstileSiteKey": "0xAAA", "turnstileSecret": "cf-secret-value", "hours": map[string]any{"zone": "UTC", "days": []map[string]any{{"day": 1, "from": "09:00", "to": "17:00"}}},
			"retentionDays": 30,
		}).json(t)
		if len(site["siteKey"].(string)) != 24 || site["hasTurnstileSecret"] != true || site["enabled"] != true || site["position"] != "right" || site["powBits"].(float64) != 16 {
			t.Fatalf("site = %v", site)
		}
		if o := site["allowedOrigins"].([]any); len(o) != 2 || o[0] != "https://intranet.lan" {
			t.Fatalf("origins are lower-cased and de-duplicated: %v", o)
		}
		if w := site["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "Turnstile") {
			t.Fatalf("warnings = %v", w)
		}
		for k, v := range site {
			if s, ok := v.(string); ok && strings.Contains(s, "cf-secret-value") {
				t.Fatalf("the Turnstile secret is returned in %s", k)
			}
		}
		id := int64(site["id"].(float64))
		admin.must(http.StatusConflict, "POST", "/api/v1/messaging/chat/sites", map[string]any{"name": "support"})
		list := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/chat/sites", nil))
		if len(list) != 1 {
			t.Fatalf("sites = %v", list)
		}
		got := admin.must(http.StatusOK, "GET", idPath("/api/v1/messaging/chat/sites", id, ""), nil).json(t)
		if got["siteKey"] != site["siteKey"] {
			t.Fatal("the key changed")
		}
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/chat/sites/99999", nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/chat/sites/zero", nil)
		// The chat channel also shows in the channel list, with no trunk.
		var found bool
		for _, c := range listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/channels", nil)) {
			if c.(map[string]any)["kind"] == "webchat" {
				found = true
			}
		}
		if !found {
			t.Fatal("the chat channel is missing from the channel list")
		}
		// A chat site's address is managed with the site.
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": int64(site["channelId"].(float64)), "address": "+3270999999"})
	})

	t.Run("update", func(t *testing.T) {
		s := r.createSite("Update me", map[string]any{"challenge": "turnstile", "turnstileSiteKey": "0xBBB", "turnstileSecret": "kept-secret"})
		path := idPath("/api/v1/messaging/chat/sites", s.ID, "")
		got := admin.must(http.StatusOK, "PUT", path, map[string]any{"greeting": "Changed", "enabled": false, "collect": "required", "retentionDays": 0}).json(t)
		if got["greeting"] != "Changed" || got["enabled"] != false || got["collect"] != "required" || got["hasTurnstileSecret"] != true || got["siteKey"] != s.Key || got["name"] != "Update me" {
			t.Fatalf("a partial update must keep the rest: %v", got)
		}
		if got["retentionDays"] != nil {
			t.Fatalf("retentionDays 0 follows the settings: %v", got["retentionDays"])
		}
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"accent": "red"})
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"turnstileSecret": ""}) // a turnstile site cannot lose its secret
		admin.must(http.StatusOK, "PUT", path, map[string]any{"challenge": "none", "turnstileSecret": ""})
		other := r.createSite("Another", nil)
		admin.must(http.StatusConflict, "PUT", idPath("/api/v1/messaging/chat/sites", other.ID, ""), map[string]any{"name": "update me"})
		admin.must(http.StatusNotFound, "PUT", "/api/v1/messaging/chat/sites/99999", map[string]any{"name": "x"})
		got = admin.must(http.StatusOK, "PUT", path, map[string]any{"challenge": "none", "allowedOrigins": []string{"https://www.example.com"}}).json(t)
		if w := got["warnings"].([]any); len(w) != 2 {
			t.Fatalf("a public origin and no challenge both warn: %v", w)
		}
	})

	t.Run("roles", func(t *testing.T) {
		s := r.createSite("Roles", nil)
		for _, who := range []*client{r.operator, r.agent, r.viewer} {
			who.must(http.StatusForbidden, "GET", "/api/v1/messaging/chat/sites", nil)
			who.must(http.StatusForbidden, "POST", "/api/v1/messaging/chat/sites", map[string]any{"name": "Nope"})
			who.must(http.StatusForbidden, "GET", idPath("/api/v1/messaging/chat/sites", s.ID, ""), nil)
			who.must(http.StatusForbidden, "PUT", idPath("/api/v1/messaging/chat/sites", s.ID, ""), map[string]any{"name": "Nope"})
			who.must(http.StatusForbidden, "DELETE", idPath("/api/v1/messaging/chat/sites", s.ID, ""), nil)
			who.must(http.StatusForbidden, "GET", idPath("/api/v1/messaging/chat/sites", s.ID, "/blocks"), nil)
			who.must(http.StatusForbidden, "DELETE", "/api/v1/messaging/chat/blocks/1", nil)
		}
	})

	t.Run("blocks and delete", func(t *testing.T) {
		s := r.createSite("Blocks", nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/chat/sites/99999/blocks", nil)
		if got := listItems(t, admin.must(http.StatusOK, "GET", idPath("/api/v1/messaging/chat/sites", s.ID, "/blocks"), nil)); len(got) != 0 {
			t.Fatalf("blocks = %v", got)
		}
		conv := r.chat(s, "Troll", "", false)
		blk := admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", conv, "/block"), map[string]any{"hours": 2}).json(t)
		if blk["siteId"].(float64) != float64(s.ID) || blk["visitorId"] == nil {
			t.Fatalf("block = %v", blk)
		}
		list := listItems(t, admin.must(http.StatusOK, "GET", idPath("/api/v1/messaging/chat/sites", s.ID, "/blocks"), nil))
		if len(list) != 2 {
			t.Fatalf("blocks = %v", list)
		}
		admin.must(http.StatusConflict, "DELETE", idPath("/api/v1/messaging/chat/sites", s.ID, ""), nil) // it has a conversation
		for _, b := range list {
			admin.must(http.StatusNoContent, "DELETE", idPath("/api/v1/messaging/chat/blocks", int64(b.(map[string]any)["id"].(float64)), ""), nil)
		}
		admin.must(http.StatusNotFound, "DELETE", "/api/v1/messaging/chat/blocks/99999", nil)
		empty := r.createSite("Empty", nil)
		admin.must(http.StatusNoContent, "DELETE", idPath("/api/v1/messaging/chat/sites", empty.ID, ""), nil)
		admin.must(http.StatusNotFound, "DELETE", idPath("/api/v1/messaging/chat/sites", empty.ID, ""), nil)
	})

	t.Run("erase a visitor", func(t *testing.T) {
		s := r.createSite("Erase", nil)
		res, err := r.st.ChatSession(context.Background(), store.ChatSessionInput{Site: s, IPHash: []byte("e")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.ChatSend(context.Background(), store.ChatSendInput{Site: s, VisitorID: res.VisitorID, Text: "x", ClientMsgID: "e1"}); err != nil {
			t.Fatal(err)
		}
		rep := admin.must(http.StatusOK, "POST", "/api/v1/inbox/erase", map[string]any{"peer": "visitor:" + strconv.FormatInt(res.VisitorID, 10)}).json(t)
		if rep["conversations"].(float64) != 1 {
			t.Fatalf("erase = %v", rep)
		}
		admin.must(http.StatusBadRequest, "POST", "/api/v1/inbox/erase", map[string]any{"peer": "visitor:abc"})
	})
}

// TestChatInboxActionsAPI fails if blocking, answering by email and the
// typing signal do not answer each documented status for supervisors and for
// agents (who reach only their own chats), if an SMS conversation takes them,
// if the email carries a link or goes to anyone but the visitor's address,
// or if emailing works without email configured.
func TestChatInboxActionsAPI(t *testing.T) {
	r := newChatRig(t, true)
	site := r.createSite("Desk", nil)
	live := r.chat(site, "Liv", "liv@example.com", false)
	offline := r.chat(site, "Olga", "olga@example.com", true)
	noMail := r.chat(site, "Nomail", "", false)
	r.assign(offline, r.agentID)
	r.assign(live, r.agentID)

	// An SMS conversation exists too.
	var trunk int64
	if err := r.db.QueryRowContext(context.Background(), `INSERT INTO trunks (name, mode, messaging) VALUES ('carrier', 'ip', TRUE) RETURNING id`).Scan(&trunk); err != nil {
		t.Fatal(err)
	}
	ch := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "SMS", "trunkId": trunk}).json(t)
	r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": ch["id"], "address": inboxDID})
	toKey, _ := phonebook.Key(inboxDID)
	suffix, _ := phonebook.Suffix(toKey)
	fromKey, _ := phonebook.Key("+32475000111")
	ing, err := r.st.IngestTrunkText(context.Background(), store.TrunkText{TrunkID: trunk, ToKey: toKey, ToSuffix: suffix, FromKey: fromKey, Body: "sms", EventKey: "sms-1"})
	if err != nil || !ing.Handled {
		t.Fatalf("ingest: %+v %v", ing, err)
	}
	sms := ing.ConversationID
	r.assign(sms, r.agentID)

	t.Run("typing", func(t *testing.T) {
		r.admin.must(http.StatusNoContent, "POST", idPath("/api/v1/inbox/conversations", live, "/typing"), nil)
		r.operator.must(http.StatusNoContent, "POST", idPath("/api/v1/inbox/conversations", live, "/typing"), nil)
		r.agent.must(http.StatusNoContent, "POST", idPath("/api/v1/me/inbox/conversations", live, "/typing"), nil)
		r.other.must(http.StatusNotFound, "POST", idPath("/api/v1/me/inbox/conversations", live, "/typing"), nil)
		r.viewer.must(http.StatusForbidden, "POST", idPath("/api/v1/inbox/conversations", live, "/typing"), nil)
		r.viewer.must(http.StatusForbidden, "POST", idPath("/api/v1/me/inbox/conversations", live, "/typing"), nil)
		r.agent.must(http.StatusForbidden, "POST", idPath("/api/v1/inbox/conversations", live, "/typing"), nil)
		r.admin.must(http.StatusNotFound, "POST", "/api/v1/inbox/conversations/99999/typing", nil)
		r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", sms, "/typing"), nil)
		r.agent.must(http.StatusConflict, "POST", idPath("/api/v1/me/inbox/conversations", sms, "/typing"), nil)
		r.agent.must(http.StatusNotFound, "POST", idPath("/api/v1/me/inbox/conversations", noMail, "/typing"), nil)
	})

	t.Run("block", func(t *testing.T) {
		r.admin.must(http.StatusBadRequest, "POST", idPath("/api/v1/inbox/conversations", noMail, "/block"), map[string]any{"hours": 9999})
		r.admin.must(http.StatusBadRequest, "POST", idPath("/api/v1/inbox/conversations", noMail, "/block"), map[string]any{"hours": -1})
		r.admin.must(http.StatusNotFound, "POST", "/api/v1/inbox/conversations/99999/block", map[string]any{})
		r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", sms, "/block"), map[string]any{})
		r.viewer.must(http.StatusForbidden, "POST", idPath("/api/v1/inbox/conversations", noMail, "/block"), map[string]any{})
		r.viewer.must(http.StatusForbidden, "POST", idPath("/api/v1/me/inbox/conversations", live, "/block"), map[string]any{})
		r.agent.must(http.StatusForbidden, "POST", idPath("/api/v1/inbox/conversations", live, "/block"), map[string]any{})
		r.other.must(http.StatusNotFound, "POST", idPath("/api/v1/me/inbox/conversations", live, "/block"), map[string]any{})
		// An agent's own SMS conversation is not a chat.
		r.agent.must(http.StatusConflict, "POST", idPath("/api/v1/me/inbox/conversations", sms, "/block"), map[string]any{})
		blk := r.agent.must(http.StatusOK, "POST", idPath("/api/v1/me/inbox/conversations", live, "/block"), map[string]any{"hours": 1}).json(t)
		if blk["id"] == nil {
			t.Fatalf("block = %v", blk)
		}
		r.operator.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", noMail, "/block"), map[string]any{})
	})

	t.Run("email reply", func(t *testing.T) {
		path := idPath("/api/v1/inbox/conversations", offline, "/email-reply")
		mine := idPath("/api/v1/me/inbox/conversations", offline, "/email-reply")
		r.admin.must(http.StatusBadRequest, "POST", path, map[string]any{"body": ""})
		r.admin.must(http.StatusBadRequest, "POST", path, map[string]any{"body": "x", "clientMsgId": "has space"})
		r.admin.must(http.StatusNotFound, "POST", "/api/v1/inbox/conversations/99999/email-reply", map[string]any{"body": "x"})
		r.viewer.must(http.StatusForbidden, "POST", path, map[string]any{"body": "x"})
		r.viewer.must(http.StatusForbidden, "POST", mine, map[string]any{"body": "x"})
		r.agent.must(http.StatusForbidden, "POST", path, map[string]any{"body": "x"})
		r.other.must(http.StatusNotFound, "POST", mine, map[string]any{"body": "x"})
		r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", sms, "/email-reply"), map[string]any{"body": "x"})
		r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", noMail, "/email-reply"), map[string]any{"body": "x"})
		r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", live, "/email-reply"), map[string]any{"body": "x"})
		if len(r.sent) != 0 {
			t.Fatalf("an email went out for a refused reply: %v", r.sent)
		}
		r.agent.must(http.StatusConflict, "POST", idPath("/api/v1/me/inbox/conversations", sms, "/email-reply"), map[string]any{"body": "x"})
		r.agent.must(http.StatusConflict, "POST", idPath("/api/v1/me/inbox/conversations", live, "/email-reply"), map[string]any{"body": "x"})
		got := r.agent.must(http.StatusCreated, "POST", mine, map[string]any{"body": "We will call you tomorrow.", "clientMsgId": "e1"}).json(t)
		if got["status"] != "sent" || got["body"] != "We will call you tomorrow." {
			t.Fatalf("reply = %v", got)
		}
		if len(r.sent) != 1 || !strings.HasPrefix(r.sent[0], "olga@example.com\n") || !strings.Contains(r.sent[0], "We will call you tomorrow.") ||
			strings.Contains(strings.ToLower(r.sent[0]), "http") {
			t.Fatalf("the email = %q", r.sent)
		}
		// A retry sends nothing again.
		r.agent.must(http.StatusOK, "POST", mine, map[string]any{"body": "We will call you tomorrow.", "clientMsgId": "e1"})
		if len(r.sent) != 1 {
			t.Fatalf("a retry sent %d emails", len(r.sent))
		}
		// The supervisor's form sends and retries the same way.
		r.admin.must(http.StatusCreated, "POST", path, map[string]any{"body": "Anything else?", "clientMsgId": "e3"})
		r.admin.must(http.StatusOK, "POST", path, map[string]any{"body": "Anything else?", "clientMsgId": "e3"})
		if len(r.sent) != 2 {
			t.Fatalf("emails = %d", len(r.sent))
		}
		// A mail failure is a 502 and the reply is kept as failed.
		r.failMail = true
		r.admin.must(http.StatusBadGateway, "POST", path, map[string]any{"body": "second", "clientMsgId": "e2"})
		r.agent.must(http.StatusBadGateway, "POST", mine, map[string]any{"body": "third", "clientMsgId": "e4"})
		r.failMail = false
		if n := scalar(t, r.env, `SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND status = 'failed'`, offline); n != 2 {
			t.Fatalf("failed replies = %d", n)
		}
		// The status says email works.
		if st := r.admin.must(http.StatusOK, "GET", "/api/v1/me/inbox/status", nil).json(t); st["emailReply"] != true {
			t.Fatalf("status = %v", st)
		}
	})

	t.Run("conversation shows the chat", func(t *testing.T) {
		got := r.admin.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", offline, ""), nil).json(t)
		chat, _ := got["chat"].(map[string]any)
		if chat == nil || chat["offline"] != true || chat["email"] != "olga@example.com" || chat["siteId"].(float64) != float64(site.ID) {
			t.Fatalf("chat = %v", got["chat"])
		}
		sm := r.admin.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", sms, ""), nil).json(t)
		if sm["chat"] != nil {
			t.Fatalf("an SMS conversation has chat info: %v", sm["chat"])
		}
		// An agent's reply in the inbox reaches the visitor, not a carrier.
		again := r.chat(site, "Again", "", false)
		r.assign(again, r.agentID)
		reply := r.agent.must(http.StatusCreated, "POST", idPath("/api/v1/me/inbox/conversations", again, "/messages"), map[string]any{"body": "Hello Again"}).json(t)
		if reply["status"] != "sent" {
			t.Fatalf("reply = %v", reply)
		}
	})
}

func TestChatEmailReplyNeedsEmail(t *testing.T) {
	r := newChatRig(t, false)
	site := r.createSite("NoMail", nil)
	conv := r.chat(site, "Olga", "olga@example.com", true)
	r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", conv, "/email-reply"), map[string]any{"body": "hi"})
	if st := r.admin.must(http.StatusOK, "GET", "/api/v1/me/inbox/status", nil).json(t); st["emailReply"] != false {
		t.Fatalf("status = %v", st)
	}
}

func scalar(t *testing.T, e *env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
