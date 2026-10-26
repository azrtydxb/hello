package api

// The messaging inbox API against PostgreSQL: every documented status of
// every operation, the roles (a viewer reads no customer text, a user only
// their own conversations, an operator changes no configuration), the
// stream and the privacy of what the inbox keeps. The package's conformance
// validator checks each request and response against openapi.json.

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

const inboxDID = "+3270123456"

var inboxSeq atomic.Int64

// inboxRig is an API with a SIP trunk, a channel and one number, an admin
// (alice), an operator, two agents (users) and a viewer.
type inboxRig struct {
	*env
	trunk                     int64
	channel, address          int64
	admin, operator           *client
	agent, other, viewer      *client
	adminID, agentID, otherID int64
	viewerID                  int64
}

func newInboxRig(t *testing.T) *inboxRig {
	t.Helper()
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvConfig(t, Config{Live: noLive{}}, box)
	r := &inboxRig{env: e}
	ctx := context.Background()
	r.admin = e.login()
	if err := e.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, testUser).Scan(&r.adminID); err != nil {
		t.Fatal(err)
	}
	r.operator, _ = e.asRole("otto", auth.RoleOperator)
	r.agent, r.agentID = e.asRole("agnes", auth.RoleUser)
	r.other, r.otherID = e.asRole("olga", auth.RoleUser)
	r.viewer, r.viewerID = e.asRole("vera", auth.RoleViewer)
	if err := e.db.QueryRowContext(ctx, `INSERT INTO trunks (name, mode, messaging) VALUES ('carrier', 'ip', TRUE) RETURNING id`).Scan(&r.trunk); err != nil {
		t.Fatal(err)
	}
	ch := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/channels",
		map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "Main", "trunkId": r.trunk}).json(t)
	r.channel = int64(ch["id"].(float64))
	ad := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses",
		map[string]any{"channelId": r.channel, "address": inboxDID, "label": "Main line"}).json(t)
	r.address = int64(ad["id"].(float64))
	return r
}

func (r *inboxRig) adminAcc() store.InboxAccess {
	return store.InboxAccess{UserID: r.adminID, All: true, Actor: "user:" + testUser, Name: testUser}
}

// text delivers a customer text as the SIP node would, returning the
// conversation's id.
func (r *inboxRig) text(from, body string) int64 {
	r.t.Helper()
	toKey, _ := phonebook.Key(inboxDID)
	suffix, _ := phonebook.Suffix(toKey)
	fromKey, _ := phonebook.Key(from)
	res, err := r.st.IngestTrunkText(context.Background(), store.TrunkText{
		TrunkID: r.trunk, ToKey: toKey, ToSuffix: suffix, FromKey: fromKey, Body: body,
		EventKey: fmt.Sprintf("ev-%d", inboxSeq.Add(1)),
	})
	if err != nil || !res.Handled {
		r.t.Fatalf("ingest: %+v, %v", res, err)
	}
	return res.ConversationID
}

func (r *inboxRig) assign(conv, user int64) {
	r.t.Helper()
	if _, err := r.st.AssignConversation(context.Background(), r.adminAcc(), conv, user); err != nil {
		r.t.Fatal(err)
	}
}

func peerNumber() string { return fmt.Sprintf("+3247510%04d", inboxSeq.Add(1)) }

func idPath(base string, id int64, tail string) string {
	return base + "/" + strconv.FormatInt(id, 10) + tail
}

// TestMessagingConfigAPI fails if the channel, number, settings, opt-out and
// erase routes do not answer each documented status, refuse an operator, or
// show a credential.
func TestMessagingConfigAPI(t *testing.T) {
	r := newInboxRig(t)
	admin := r.admin

	t.Run("channels", func(t *testing.T) {
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "", "trunkId": r.trunk})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "whatsapp", "provider": "sip_trunk", "name": "W", "trunkId": r.trunk})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "twilio", "name": "T", "trunkId": r.trunk})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "No trunk"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "Ghost", "trunkId": 99999})
		admin.must(http.StatusConflict, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "main", "trunkId": r.trunk})
		second := admin.must(http.StatusCreated, "POST", "/api/v1/messaging/channels", map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "Second", "trunkId": r.trunk}).json(t)
		if second["hasCredentials"] != false || second["status"] != "ok" || second["trunkName"] != "carrier" {
			t.Fatalf("channel = %v", second)
		}
		for k := range second {
			if strings.Contains(strings.ToLower(k), "secret") || strings.Contains(strings.ToLower(k), "password") || k == "credentials" || k == "hookId" {
				t.Errorf("a channel shows %q", k)
			}
		}
		id := int64(second["id"].(float64))
		if got := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/channels", nil)); len(got) != 2 {
			t.Fatalf("channels = %v", got)
		}
		admin.must(http.StatusOK, "GET", idPath("/api/v1/messaging/channels", id, ""), nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/channels/99999", nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/channels/zero", nil)
		admin.must(http.StatusOK, "PUT", idPath("/api/v1/messaging/channels", id, ""), map[string]any{"name": "Renamed", "enabled": false})
		admin.must(http.StatusBadRequest, "PUT", idPath("/api/v1/messaging/channels", id, ""), map[string]any{"kind": "webchat"})
		admin.must(http.StatusBadRequest, "PUT", idPath("/api/v1/messaging/channels", id, ""), map[string]any{"provider": "twilio"})
		admin.must(http.StatusConflict, "PUT", idPath("/api/v1/messaging/channels", id, ""), map[string]any{"name": "MAIN"})
		admin.must(http.StatusNotFound, "PUT", "/api/v1/messaging/channels/99999", map[string]any{"name": "x"})
		test := admin.must(http.StatusOK, "POST", idPath("/api/v1/messaging/channels", id, "/test"), nil).json(t)
		if test["ok"] != false {
			t.Fatalf("a disabled channel with no number passed its test: %v", test)
		}
		main := admin.must(http.StatusOK, "POST", idPath("/api/v1/messaging/channels", r.channel, "/test"), nil).json(t)
		if main["ok"] != true {
			t.Fatalf("the working channel failed its test: %v", main)
		}
		admin.must(http.StatusNotFound, "POST", "/api/v1/messaging/channels/99999/test", nil)
		// A trunk with SMS over SIP off fails the test.
		if _, err := r.db.ExecContext(context.Background(), `UPDATE trunks SET messaging = FALSE`); err != nil {
			t.Fatal(err)
		}
		if off := admin.must(http.StatusOK, "POST", idPath("/api/v1/messaging/channels", r.channel, "/test"), nil).json(t); off["ok"] != false {
			t.Fatalf("a trunk without SMS over SIP passed: %v", off)
		}
		if _, err := r.db.ExecContext(context.Background(), `UPDATE trunks SET messaging = TRUE`); err != nil {
			t.Fatal(err)
		}
		admin.must(http.StatusNoContent, "DELETE", idPath("/api/v1/messaging/channels", id, ""), nil)
		admin.must(http.StatusNotFound, "DELETE", idPath("/api/v1/messaging/channels", id, ""), nil)
	})

	t.Run("addresses", func(t *testing.T) {
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.channel, "address": "12"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.channel, "address": "not a number"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"address": "+3270999999"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": 99999, "address": "+3270999999"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.channel, "address": "+3270999999", "optOutKeywords": []string{"two words"}})
		admin.must(http.StatusConflict, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.channel, "address": inboxDID})
		a := admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses", map[string]any{
			"channelId": r.channel, "address": "+3270999999", "label": "Sales", "optOutKeywords": []string{"Arret"}, "helpText": "Call us"}).json(t)
		id := int64(a["id"].(float64))
		if words := a["optOutKeywords"].([]any); len(words) != 1 || words[0] != "arret" || a["routeKind"] != "inbox" {
			t.Fatalf("address = %v", a)
		}
		if got := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/addresses", nil)); len(got) != 2 {
			t.Fatalf("addresses = %v", got)
		}
		if got := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/addresses?channelId="+strconv.FormatInt(r.channel, 10), nil)); len(got) != 2 {
			t.Fatalf("a channel's addresses = %v", got)
		}
		admin.must(http.StatusBadRequest, "GET", "/api/v1/messaging/addresses?channelId=x", nil)
		admin.must(http.StatusOK, "GET", idPath("/api/v1/messaging/addresses", id, ""), nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/addresses/99999", nil)
		admin.must(http.StatusOK, "PUT", idPath("/api/v1/messaging/addresses", id, ""), map[string]any{"label": "Sales team", "enabled": false})
		admin.must(http.StatusBadRequest, "PUT", idPath("/api/v1/messaging/addresses", id, ""), map[string]any{"address": "+3270111111"})
		admin.must(http.StatusBadRequest, "PUT", idPath("/api/v1/messaging/addresses", id, ""), map[string]any{"channelId": 99999})
		admin.must(http.StatusNotFound, "PUT", "/api/v1/messaging/addresses/99999", map[string]any{"label": "x"})
		// A number with a conversation is not deleted.
		r.text(peerNumber(), "hello")
		admin.must(http.StatusConflict, "DELETE", idPath("/api/v1/messaging/addresses", r.address, ""), nil)
		admin.must(http.StatusConflict, "DELETE", idPath("/api/v1/messaging/channels", r.channel, ""), nil)
		admin.must(http.StatusNoContent, "DELETE", idPath("/api/v1/messaging/addresses", id, ""), nil)
		admin.must(http.StatusNotFound, "DELETE", idPath("/api/v1/messaging/addresses", id, ""), nil)
	})

	t.Run("settings", func(t *testing.T) {
		got := admin.must(http.StatusOK, "GET", "/api/v1/messaging/settings", nil).json(t)
		if got["retentionSmsDays"] != float64(365) || got["retentionChatDays"] != float64(90) || got["reopenDays"] != float64(7) || got["emailNotify"] != true {
			t.Fatalf("defaults = %v", got)
		}
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/settings", map[string]any{"retentionSmsDays": 0})
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/settings", map[string]any{"reopenDays": 366})
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/settings", map[string]any{"bogus": 1})
		put := admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{"retentionSmsDays": 30, "emailNotify": false}).json(t)
		if put["retentionSmsDays"] != float64(30) || put["retentionChatDays"] != float64(90) || put["emailNotify"] != false {
			t.Fatalf("partial update = %v", put)
		}
		admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{"retentionSmsDays": 365, "emailNotify": true})
	})

	t.Run("opt-outs and erase", func(t *testing.T) {
		peer := peerNumber()
		conv := r.text(peer, "STOP")
		list := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/optouts", nil))
		if len(list) != 1 || list[0].(map[string]any)["peer"] != peer || list[0].(map[string]any)["source"] != "keyword" {
			t.Fatalf("opt-outs = %v", list)
		}
		admin.must(http.StatusBadRequest, "GET", "/api/v1/messaging/optouts?addressId=x", nil)
		admin.must(http.StatusBadRequest, "GET", "/api/v1/messaging/optouts?q="+strings.Repeat("1", 40), nil)
		if got := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/optouts?q="+url.QueryEscape(peer[len(peer)-4:]), nil)); len(got) != 1 {
			t.Fatalf("search by the end of the number = %v", got)
		}
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/optouts", map[string]any{"addressId": r.address, "peer": "x"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/optouts", map[string]any{"addressId": 99999, "peer": peerNumber()})
		made := admin.must(http.StatusCreated, "POST", "/api/v1/messaging/optouts", map[string]any{"addressId": r.address, "peer": peerNumber(), "note": "asked by phone"}).json(t)
		if made["source"] != "admin" || made["state"] != "out" {
			t.Fatalf("recorded opt-out = %v", made)
		}
		id := int64(made["id"].(float64))
		admin.must(http.StatusBadRequest, "POST", idPath("/api/v1/messaging/optouts", id, "/reconsent"), map[string]any{"note": ""})
		admin.must(http.StatusNotFound, "POST", "/api/v1/messaging/optouts/99999/reconsent", map[string]any{"note": "verbal"})
		back := admin.must(http.StatusOK, "POST", idPath("/api/v1/messaging/optouts", id, "/reconsent"), map[string]any{"note": "verbal, today"}).json(t)
		if back["state"] != "in" {
			t.Fatalf("after re-consent = %v", back)
		}
		if got := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/optouts?all=true", nil)); len(got) != 2 {
			t.Fatalf("all opt-outs = %v", got)
		}
		// An agent cannot text the opted-out customer, whoever they are.
		r.assign(conv, r.agentID)
		r.agent.must(http.StatusUnprocessableEntity, "POST", idPath("/api/v1/me/inbox/conversations", conv, "/messages"), map[string]any{"body": "hello"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/inbox/erase", map[string]any{"peer": "x"})
		rep := admin.must(http.StatusOK, "POST", "/api/v1/inbox/erase", map[string]any{"peer": peer}).json(t)
		if rep["conversations"] != float64(1) || rep["optOutsKept"] != float64(1) {
			t.Fatalf("erase report = %v", rep)
		}
		r.agent.must(http.StatusNotFound, "GET", idPath("/api/v1/me/inbox/conversations", conv, ""), nil)
	})
}

// scenario runs the conversation operations as one caller on one prefix.
// mine is the /me form: the caller only reaches conversations assigned to
// them or replied to, so each one the scenario makes is assigned first.
type scenario struct {
	r    *inboxRig
	base string
	c    *client
	self int64
	mine bool
	// peers holds the customers the scenario made.
	peers []string
}

func (s *scenario) newConv(body string) (int64, string) {
	peer := peerNumber()
	id := s.r.text(peer, body)
	if s.mine {
		s.r.assign(id, s.self)
	}
	s.peers = append(s.peers, peer)
	return id, peer
}

func (s *scenario) path(id int64, tail string) string {
	return idPath(s.base+"/conversations", id, tail)
}

func (s *scenario) run(t *testing.T) {
	c, r := s.c, s.r
	conv, peer := s.newConv("I have a question about my invoice")

	t.Run("list and get", func(t *testing.T) {
		got := c.must(http.StatusOK, "GET", s.base+"/conversations", nil).json(t)
		found := false
		for _, it := range got["items"].([]any) {
			found = found || it.(map[string]any)["id"] == float64(conv)
		}
		if !found || got["next"] == nil {
			t.Fatalf("the list does not show the conversation: %v", got)
		}
		c.must(http.StatusBadRequest, "GET", s.base+"/conversations?view=bogus", nil)
		c.must(http.StatusBadRequest, "GET", s.base+"/conversations?status=bogus", nil)
		c.must(http.StatusBadRequest, "GET", s.base+"/conversations?channelId=x", nil)
		c.must(http.StatusBadRequest, "GET", s.base+"/conversations?cursor=x", nil)
		c.must(http.StatusBadRequest, "GET", s.base+"/conversations?limit=0", nil)
		c.must(http.StatusBadRequest, "GET", s.base+"/conversations?q="+strings.Repeat("x", 101), nil)
		if hits := listItems(t, c.must(http.StatusOK, "GET", s.base+"/conversations?q=invoice&status=any&view=all", nil)); len(hits) == 0 {
			t.Error("the search found nothing")
		}
		page := c.must(http.StatusOK, "GET", s.base+"/conversations?limit=1&status=any", nil).json(t)
		if len(page["items"].([]any)) != 1 {
			t.Fatalf("page = %v", page)
		}
		one := c.must(http.StatusOK, "GET", s.path(conv, ""), nil).json(t)
		if one["peer"] != peer || one["status"] != "open" || one["address"] != inboxDID || one["unread"] != float64(1) {
			t.Fatalf("conversation = %v", one)
		}
		c.must(http.StatusNotFound, "GET", s.path(99999, ""), nil)
		c.must(http.StatusNotFound, "GET", s.base+"/conversations/zero", nil)
	})

	t.Run("messages", func(t *testing.T) {
		ms := listItems(t, c.must(http.StatusOK, "GET", s.path(conv, "/messages"), nil))
		if len(ms) == 0 || ms[len(ms)-1].(map[string]any)["body"] != "I have a question about my invoice" {
			t.Fatalf("messages = %v", ms)
		}
		c.must(http.StatusBadRequest, "GET", s.path(conv, "/messages?cursor=x"), nil)
		c.must(http.StatusBadRequest, "GET", s.path(conv, "/messages?limit=101"), nil)
		c.must(http.StatusNotFound, "GET", s.path(99999, "/messages"), nil)
		c.must(http.StatusOK, "GET", s.path(conv, "/messages?limit=1"), nil)
	})

	t.Run("send", func(t *testing.T) {
		body := map[string]any{"body": "Let me look", "clientMsgId": "send-1"}
		first := c.must(http.StatusCreated, "POST", s.path(conv, "/messages"), body).json(t)
		if first["status"] != "sending" || first["direction"] != "out" || first["senderKind"] != "user" {
			t.Fatalf("sent = %v", first)
		}
		again := c.must(http.StatusOK, "POST", s.path(conv, "/messages"), body).json(t)
		if again["id"] != first["id"] {
			t.Fatalf("a retried send made a new message: %v", again)
		}
		var queued int
		if err := r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM messages WHERE conversation_message_id = $1`, int64(first["id"].(float64))).Scan(&queued); err != nil || queued != 1 {
			t.Fatalf("queued %d, %v; want 1", queued, err)
		}
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/messages"), map[string]any{"body": "  "})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/messages"), map[string]any{"body": "a\x00b"})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/messages"), map[string]any{"body": strings.Repeat("x", 2001)})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/messages"), map[string]any{"body": "x", "clientMsgId": "has space"})
		c.must(http.StatusNotFound, "POST", s.path(99999, "/messages"), map[string]any{"body": "x"})
		// A long reply is accepted and split.
		c.must(http.StatusCreated, "POST", s.path(conv, "/messages"), map[string]any{"body": strings.Repeat("word ", 390)})
		// 409: a spam conversation cannot be texted.
		spam, _ := s.newConv("buy now")
		c.must(http.StatusOK, "POST", s.path(spam, "/status"), map[string]any{"status": "spam"})
		c.must(http.StatusConflict, "POST", s.path(spam, "/messages"), map[string]any{"body": "x"})
		// 422: an opted-out customer cannot be texted.
		stopped, _ := s.newConv("STOP")
		c.must(http.StatusUnprocessableEntity, "POST", s.path(stopped, "/messages"), map[string]any{"body": "x"})
		// The customer's own reply now shows as a conversation of its own.
		if hits := listItems(t, c.must(http.StatusOK, "GET", s.base+"/conversations?q=STOP", nil)); len(hits) == 0 {
			t.Error("the opted-out conversation is not listed")
		}
	})

	t.Run("notes", func(t *testing.T) {
		n := c.must(http.StatusCreated, "POST", s.path(conv, "/notes"), map[string]any{"body": "Customer is a VIP"}).json(t)
		if n["kind"] != "note" {
			t.Fatalf("note = %v", n)
		}
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/notes"), map[string]any{"body": ""})
		c.must(http.StatusNotFound, "POST", s.path(99999, "/notes"), map[string]any{"body": "x"})
		var queued int
		if err := r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM messages WHERE body_text = 'Customer is a VIP'`).Scan(&queued); err != nil || queued != 0 {
			t.Fatalf("a note reached the SIP queue: %d, %v", queued, err)
		}
	})

	t.Run("claim", func(t *testing.T) {
		c.must(http.StatusNotFound, "POST", s.path(99999, "/claim"), nil)
		if !s.mine {
			pool, _ := s.newConv("anyone there?")
			got := c.must(http.StatusOK, "POST", s.path(pool, "/claim"), nil).json(t)
			if got["assigneeUserId"] == nil {
				t.Fatalf("claimed = %v", got)
			}
			c.must(http.StatusConflict, "POST", s.path(pool, "/claim"), nil)
			return
		}
		// An agent claims from the pool only a conversation they replied to
		// and a supervisor then returned there.
		pool, _ := s.newConv("anyone there?")
		c.must(http.StatusCreated, "POST", s.path(pool, "/messages"), map[string]any{"body": "yes"})
		r.assign(pool, 0)
		got := c.must(http.StatusOK, "POST", s.path(pool, "/claim"), nil).json(t)
		if got["assigneeUserId"] != float64(s.self) {
			t.Fatalf("claimed = %v", got)
		}
		c.must(http.StatusConflict, "POST", s.path(pool, "/claim"), nil)
	})

	t.Run("assign", func(t *testing.T) {
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/assign"), map[string]any{"userId": -1})
		c.must(http.StatusNotFound, "POST", s.path(99999, "/assign"), map[string]any{"userId": r.agentID})
		c.must(http.StatusUnprocessableEntity, "POST", s.path(conv, "/assign"), map[string]any{"userId": r.viewerID})
		c.must(http.StatusUnprocessableEntity, "POST", s.path(conv, "/assign"), map[string]any{"userId": 99999})
		done, _ := s.newConv("resolved soon")
		c.must(http.StatusOK, "POST", s.path(done, "/status"), map[string]any{"status": "resolved"})
		c.must(http.StatusConflict, "POST", s.path(done, "/assign"), map[string]any{"userId": r.otherID})
		moved, _ := s.newConv("hand me over")
		got := c.must(http.StatusOK, "POST", s.path(moved, "/assign"), map[string]any{"userId": r.otherID}).json(t)
		if got["assigneeUserId"] != float64(r.otherID) {
			t.Fatalf("assigned = %v", got)
		}
		if s.mine {
			c.must(http.StatusNotFound, "GET", s.path(moved, ""), nil) // no longer theirs
		}
		if !s.mine {
			got = c.must(http.StatusOK, "POST", s.path(moved, "/assign"), map[string]any{"userId": 0}).json(t)
			if got["assigneeUserId"] != nil {
				t.Fatalf("returned to the pool = %v", got)
			}
		}
	})

	t.Run("status", func(t *testing.T) {
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/status"), map[string]any{"status": "bogus"})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/status"), map[string]any{"status": "snoozed"})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/status"), map[string]any{"status": "snoozed", "snoozedUntil": time.Now().Add(-time.Hour).Format(time.RFC3339)})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/status"), map[string]any{"status": "open", "snoozedUntil": time.Now().Add(time.Hour).Format(time.RFC3339)})
		c.must(http.StatusNotFound, "POST", s.path(99999, "/status"), map[string]any{"status": "open"})
		snoozed := c.must(http.StatusOK, "POST", s.path(conv, "/status"), map[string]any{"status": "snoozed", "snoozedUntil": time.Now().Add(time.Hour).Format(time.RFC3339)}).json(t)
		if snoozed["status"] != "snoozed" || snoozed["snoozedUntil"] == nil {
			t.Fatalf("snoozed = %v", snoozed)
		}
		pending := c.must(http.StatusOK, "POST", s.path(conv, "/status"), map[string]any{"status": "pending"}).json(t)
		if pending["status"] != "pending" || pending["snoozedUntil"] != nil {
			t.Fatalf("pending = %v", pending)
		}
		// 409: reopening a resolved conversation beside a newer open one of
		// the same customer.
		r.admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{"reopenDays": 0})
		defer r.admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{"reopenDays": 7})
		old, oldPeer := s.newConv("first matter")
		resolved := c.must(http.StatusOK, "POST", s.path(old, "/status"), map[string]any{"status": "resolved"}).json(t)
		if resolved["status"] != "resolved" || resolved["resolvedAt"] == nil {
			t.Fatalf("resolved = %v", resolved)
		}
		newer := r.text(oldPeer, "second matter")
		if newer == old {
			t.Fatal("a text after resolve with reopenDays 0 reopened the old conversation")
		}
		c.must(http.StatusConflict, "POST", s.path(old, "/status"), map[string]any{"status": "open"})
		back := c.must(http.StatusOK, "POST", s.path(conv, "/status"), map[string]any{"status": "open"}).json(t)
		if back["status"] != "open" {
			t.Fatalf("reopened = %v", back)
		}
	})

	t.Run("tags and read", func(t *testing.T) {
		got := c.must(http.StatusOK, "POST", s.path(conv, "/tags"), map[string]any{"tags": []string{"Billing", "vip", "billing"}}).json(t)
		if tags := got["tags"].([]any); len(tags) != 2 || tags[0] != "billing" || tags[1] != "vip" {
			t.Fatalf("tags = %v", got["tags"])
		}
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/tags"), map[string]any{"tags": []string{""}})
		c.must(http.StatusBadRequest, "POST", s.path(conv, "/tags"), map[string]any{"tags": []string{strings.Repeat("x", 40)}})
		c.must(http.StatusNotFound, "POST", s.path(99999, "/tags"), map[string]any{"tags": []string{"a"}})
		c.must(http.StatusNoContent, "POST", s.path(conv, "/read"), nil)
		c.must(http.StatusNotFound, "POST", s.path(99999, "/read"), nil)
		if one := c.must(http.StatusOK, "GET", s.path(conv, ""), nil).json(t); one["unread"] != float64(0) {
			t.Fatalf("unread after reading = %v", one["unread"])
		}
	})

	// A customer text on an assigned conversation shows as unread again.
	r.text(peer, "any news?")
	if one := c.must(http.StatusOK, "GET", s.path(conv, ""), nil).json(t); one["unread"] != float64(1) {
		t.Fatalf("unread after a new text = %v", one["unread"])
	}
}

// TestInboxAPI fails if an operation of the inbox, as a supervisor or as an
// agent, does not answer each documented status or does the wrong thing.
func TestInboxAPI(t *testing.T) {
	r := newInboxRig(t)
	t.Run("supervisor", func(t *testing.T) {
		(&scenario{r: r, base: "/api/v1/inbox", c: r.operator, self: 0}).run(t)
	})
	t.Run("agent", func(t *testing.T) {
		(&scenario{r: r, base: "/api/v1/me/inbox", c: r.agent, self: r.agentID, mine: true}).run(t)
	})

	t.Run("bulk and stats", func(t *testing.T) {
		a, b := r.text(peerNumber(), "a"), r.text(peerNumber(), "b")
		ids := []int64{a, b, 99999}
		r.operator.must(http.StatusBadRequest, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": []int64{}, "action": "resolve"})
		r.operator.must(http.StatusBadRequest, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": ids, "action": "delete"})
		r.operator.must(http.StatusBadRequest, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": []int64{-1}, "action": "resolve"})
		r.operator.must(http.StatusBadRequest, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": ids, "action": "resolve", "userId": 3})
		got := r.operator.must(http.StatusOK, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": ids, "action": "resolve"}).json(t)
		if got["done"] != float64(2) || got["failed"] != float64(1) {
			t.Fatalf("bulk = %v", got)
		}
		got = r.operator.must(http.StatusOK, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": []int64{a, b}, "action": "assign", "userId": r.agentID}).json(t)
		if got["failed"] != float64(2) { // resolved conversations cannot be assigned
			t.Fatalf("assigning resolved ones = %v", got)
		}
		r.operator.must(http.StatusOK, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": []int64{a, b}, "action": "reopen"})
		got = r.operator.must(http.StatusOK, "POST", "/api/v1/inbox/conversations/bulk", map[string]any{"ids": []int64{a, b}, "action": "assign", "userId": r.agentID}).json(t)
		if got["done"] != float64(2) {
			t.Fatalf("bulk assign = %v", got)
		}
		stats := r.operator.must(http.StatusOK, "GET", "/api/v1/inbox/stats", nil).json(t)
		if stats["open"].(float64) < 2 {
			t.Fatalf("stats = %v", stats)
		}
	})

	t.Run("status agents and ownership", func(t *testing.T) {
		st := r.agent.must(http.StatusOK, "GET", "/api/v1/me/inbox/status", nil).json(t)
		if st["enabled"] != true {
			t.Fatalf("status = %v", st)
		}
		// Another agent reaches none of the first agent's conversations.
		conv := r.text(peerNumber(), "private")
		r.assign(conv, r.agentID)
		r.agent.must(http.StatusOK, "GET", idPath("/api/v1/me/inbox/conversations", conv, ""), nil)
		for _, req := range [][3]string{
			{"GET", "", ""}, {"GET", "/messages", ""}, {"POST", "/messages", `{"body":"x"}`}, {"POST", "/notes", `{"body":"x"}`},
			{"POST", "/claim", ""}, {"POST", "/assign", `{"userId":0}`}, {"POST", "/status", `{"status":"open"}`},
			{"POST", "/tags", `{"tags":[]}`}, {"POST", "/read", ""},
		} {
			var body any
			if req[2] != "" {
				body = req[2]
			}
			r.other.must(http.StatusNotFound, req[0], idPath("/api/v1/me/inbox/conversations", conv, req[1]), body)
		}
		// The other agent was handed some conversations by the scenarios above
		// but never this one.
		for _, c := range listItems(t, r.other.must(http.StatusOK, "GET", "/api/v1/me/inbox/conversations?status=any", nil)) {
			if c.(map[string]any)["id"] == float64(conv) {
				t.Fatalf("another agent lists the first agent's conversation: %v", c)
			}
		}
		if got := listItems(t, r.agent.must(http.StatusOK, "GET", "/api/v1/me/inbox/agents", nil)); len(got) < 4 {
			t.Fatalf("agents = %v", got)
		}
		for _, a := range listItems(t, r.agent.must(http.StatusOK, "GET", "/api/v1/me/inbox/agents", nil)) {
			if a.(map[string]any)["username"] == "vera" {
				t.Error("a viewer is offered as an agent")
			}
		}
		// A supervisor opening a conversation that is not theirs is audited.
		r.operator.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", conv, ""), nil)
		var n int
		if err := r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'supervisor_view' AND actor = 'user:otto' AND resource_id = $1`, strconv.FormatInt(conv, 10)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%d supervisor_view rows, %v; want 1", n, err)
		}
		// The /me view of an operator is their own, not every conversation.
		own := listItems(t, r.operator.must(http.StatusOK, "GET", "/api/v1/me/inbox/conversations?status=any", nil))
		for _, c := range own {
			if c.(map[string]any)["assigneeUserId"] == nil {
				t.Errorf("an operator's own inbox shows an unassigned conversation: %v", c)
			}
		}
	})
}

// TestMessagingRBAC fails if a viewer reads customer text through any inbox
// route, a user reaches the supervisor or configuration routes, an operator
// changes the configuration, a token without the messages scope reads the
// inbox, or a credential is shown by any GET.
func TestMessagingRBAC(t *testing.T) {
	r := newInboxRig(t)
	conv := r.text(peerNumber(), "private text")
	for _, rt := range Routes() {
		isInbox := strings.HasPrefix(rt.Pattern, "/api/v1/inbox/") || strings.HasPrefix(rt.Pattern, "/api/v1/me/inbox/")
		isConfig := strings.HasPrefix(rt.Pattern, "/api/v1/messaging/")
		if !isInbox && !isConfig {
			continue
		}
		path := strings.ReplaceAll(rt.Pattern, "{id}", strconv.FormatInt(conv, 10))
		send := func(c *client) response {
			var b any
			if rt.Method != "GET" && rt.Method != "DELETE" {
				b = map[string]any{}
			}
			return c.do(rt.Method, path, b)
		}
		forbidden := []*client{r.viewer}
		switch {
		case isConfig || path == "/api/v1/inbox/erase":
			forbidden = append(forbidden, r.operator, r.agent)
		case strings.HasPrefix(rt.Pattern, "/api/v1/inbox/"):
			forbidden = append(forbidden, r.agent)
		}
		for _, c := range forbidden {
			if got := send(c); got.code != http.StatusForbidden {
				t.Errorf("%s %s as a forbidden role = %d %s, want 403", rt.Method, rt.Pattern, got.code, got.body)
			}
		}
		if got := send(r.e()); got.code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %d, want 401", rt.Method, rt.Pattern, got.code)
		}
	}

	// A token without the messages scope reads nothing, with it reads.
	ctx := context.Background()
	var tokens int
	mk := func(scopes auth.Scopes) *client {
		tokens++
		tok, hash := auth.NewToken()
		var uid int64
		if err := r.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = 'otto'`).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.CreateToken(ctx, "test", uid, store.NewToken{Name: "t" + strconv.Itoa(tokens), Scopes: scopes}, hash); err != nil {
			t.Fatal(err)
		}
		c := r.client()
		c.bearer = tok
		return c
	}
	mk(auth.Scopes{auth.ScopeRead, auth.ScopeWrite}).must(http.StatusForbidden, "GET", "/api/v1/inbox/conversations", nil)
	mk(auth.Scopes{auth.ScopeAdmin}).must(http.StatusForbidden, "GET", "/api/v1/inbox/conversations", nil)
	mk(auth.Scopes{auth.ScopeMessages}).must(http.StatusOK, "GET", "/api/v1/inbox/conversations", nil)
	// A token without the admin scope changes no configuration, an operator
	// token with it still does not (the role).
	mk(auth.Scopes{auth.ScopeMessages, auth.ScopeAdmin}).must(http.StatusForbidden, "GET", "/api/v1/messaging/channels", nil)

	// The agent is granted the scope but is not given others' conversations.
	r.agent.must(http.StatusNotFound, "GET", idPath("/api/v1/me/inbox/conversations", conv, ""), nil)
	// No GET shows a credential or the hook id.
	for _, p := range []string{"/api/v1/messaging/channels", "/api/v1/messaging/addresses", "/api/v1/messaging/optouts"} {
		raw := string(r.admin.must(http.StatusOK, "GET", p, nil).body)
		for _, bad := range []string{"credentials", "hookId", "hook_id", "password", "secret"} {
			if strings.Contains(raw, bad) {
				t.Errorf("GET %s shows %q: %s", p, bad, raw)
			}
		}
	}
}

// e returns an unauthenticated client of the same server.
func (r *inboxRig) e() *client { return r.client() }

// TestInboxStream fails if the stream shows another user's events, misses
// an event after a reconnect with Last-Event-ID, repeats one, does not start
// at the newest event, or accepts a bad Last-Event-ID.
func TestInboxStream(t *testing.T) {
	oldPoll, oldKeep := inboxPoll, sseKeepalive
	inboxPoll, sseKeepalive = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { inboxPoll, sseKeepalive = oldPoll, oldKeep })
	r := newInboxRig(t)

	type frame struct {
		id, event, data string
	}
	open := func(c *client, lastID string) (<-chan frame, context.CancelFunc) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/api/v1/me/inbox/stream", nil)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := c.hc.Do(req) //nolint:bodyclose // the reader goroutine below closes it
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			cancel()
			t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		out := make(chan frame, 64)
		go func() {
			defer close(out)
			defer func() { _ = resp.Body.Close() }()
			sc := bufio.NewScanner(resp.Body)
			var f frame
			for sc.Scan() {
				line := sc.Text()
				switch {
				case line == "":
					if f.event != "" {
						out <- f
					}
					f = frame{}
				case strings.HasPrefix(line, "id: "):
					f.id = line[4:]
				case strings.HasPrefix(line, "event: "):
					f.event = line[7:]
				case strings.HasPrefix(line, "data: "):
					f.data = line[6:]
				}
			}
		}()
		return out, cancel
	}
	next := func(ch <-chan frame) frame {
		t.Helper()
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatal("the stream ended")
			}
			return f
		case <-time.After(5 * time.Second):
			t.Fatal("no event within 5 s")
		}
		return frame{}
	}
	none := func(ch <-chan frame, what string) {
		t.Helper()
		select {
		case f := <-ch:
			t.Errorf("%s: got %+v", what, f)
		case <-time.After(300 * time.Millisecond):
		}
	}

	// A bad Last-Event-ID is refused.
	req, _ := http.NewRequest("GET", r.srv.URL+"/api/v1/me/inbox/stream", nil)
	req.Header.Set("Last-Event-ID", "x")
	resp, err := r.agent.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad Last-Event-ID = %d, want 400", resp.StatusCode)
	}

	conv := r.text(peerNumber(), "before the stream")
	r.assign(conv, r.agentID)
	agentCh, stopAgent := open(r.agent, "") // starts at the newest: nothing replays
	defer stopAgent()
	otherCh, stopOther := open(r.other, "")
	defer stopOther()
	none(agentCh, "a new stream replayed an old event")

	r.text(peerFor(t, r, conv), "a new text")
	f := next(agentCh)
	if f.event != "message.in" || !strings.Contains(f.data, fmt.Sprintf(`"conversationId":%d`, conv)) {
		t.Fatalf("event = %+v", f)
	}
	// The content never travels in an event.
	if strings.Contains(f.data, "new text") {
		t.Errorf("an event carries text: %s", f.data)
	}
	none(otherCh, "another user received the agent's event")
	last := f.id

	// More events while the agent is away; resuming gets them once.
	stopAgent()
	r.text(peerFor(t, r, conv), "second")
	r.text(peerFor(t, r, conv), "third")
	resumed, stop2 := open(r.agent, last)
	defer stop2()
	a, b := next(resumed), next(resumed)
	if a.event != "message.in" || b.event != "message.in" {
		t.Fatalf("resumed = %+v, %+v", a, b)
	}
	ai, _ := strconv.Atoi(a.id)
	bi, _ := strconv.Atoi(b.id)
	li, _ := strconv.Atoi(last)
	if li >= ai || ai >= bi {
		t.Fatalf("ids %d, %d after %d: not increasing", ai, bi, li)
	}
	none(resumed, "an event repeated after the resume")
	// The stream shows presence for the email fallback.
	var n int
	if err := r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM inbox_presence WHERE user_id = $1`, r.agentID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("presence rows = %d, %v", n, err)
	}
}

// peerFor returns the customer's number of a conversation.
func peerFor(t *testing.T, r *inboxRig, conv int64) string {
	t.Helper()
	var peer string
	if err := r.db.QueryRowContext(context.Background(), `SELECT peer_key FROM conversations WHERE id = $1`, conv).Scan(&peer); err != nil {
		t.Fatal(err)
	}
	return peer
}
