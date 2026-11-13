package api

// Messaging item 6, Telnyx and sender registration, against PostgreSQL and a
// fake of Telnyx's v2 REST API (and, for the registration route, of Twilio's
// Messaging API). The package's conformance validator checks every request
// and response against openapi.json.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/messaging/provider/telnyx"
	"github.com/azrtydxb/hello/internal/secret"
)

const (
	telnyxDID     = "+12125550100"
	telnyxTollDID = "+18005550100"
	telnyxMsgID   = "40385f64-5717-4562-b3fc-%012d"
	telnyxProfile = "4001932a-6b2d-4a0c-8f7e-9d5a1c2b3e4f"
)

// The key is built at run time: a literal looks like a real one to secret
// scanners.
var telnyxKey = "KEY" + strings.Repeat("0123456789", 3) + "-do-not-log"

// fakeTelnyx is Telnyx's v2 API as far as Hello uses it. Each field is a
// switch a test flips.
type fakeTelnyx struct {
	srv *httptest.Server
	mu  sync.Mutex
	// numbersStatus is the status of the numbers listing (0 means 200).
	numbersStatus int
	// sendMode is ok, lost, optout (403 / 40300), reject (422 / 40310) or
	// unauth (401).
	sendMode string
	// msgStatus and msgError are what a status read answers.
	msgStatus string
	msgError  string
	// regStatus, when not 0, is the status the 10DLC reads answer.
	regStatus int
	sends     []map[string]any
	reads     int
	seq       int
}

func newFakeTelnyx(t *testing.T) *fakeTelnyx {
	t.Helper()
	f := &fakeTelnyx{sendMode: "ok", msgStatus: "delivered"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	provider.Register(telnyx.Kind, telnyx.Factory(telnyx.WithBaseURL(f.srv.URL+"/v2"), telnyx.WithHTTPClient(f.srv.Client())))
	t.Cleanup(func() { provider.Register(telnyx.Kind, telnyx.Factory()) })
	return f
}

func (f *fakeTelnyx) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+telnyxKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":"10009","title":"Authentication failed"}]}`)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v2")
	switch {
	case r.Method == http.MethodGet && p == "/phone_numbers/messaging":
		if f.numbersStatus != 0 {
			w.WriteHeader(f.numbersStatus)
			return
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"n1","phone_number":"`+telnyxDID+`","type":"longcode","features":{"sms":{"domestic_two_way":true},"mms":{"domestic_two_way":true}}},
			{"id":"n2","phone_number":"`+telnyxTollDID+`","type":"toll-free","features":{"sms":{"domestic_two_way":true},"mms":{"domestic_two_way":false}}},
			{"id":"n3","phone_number":"+3270999222","type":"longcode","features":{"sms":{"domestic_two_way":true}}}],
			"meta":{"total_pages":1}}`)
	case r.Method == http.MethodPost && p == "/messages":
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.sends = append(f.sends, body)
		switch f.sendMode {
		case "lost":
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
				}
			}
		case "optout":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"errors":[{"code":"40300","title":"Blocked due to STOP message"}]}`)
		case "reject":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"errors":[{"code":"40310","title":"Invalid To address"}]}`)
		case "unauth":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errors":[{"code":"10009"}]}`)
		default:
			f.seq++
			_, _ = io.WriteString(w, `{"data":{"id":"`+fmt.Sprintf(telnyxMsgID, f.seq)+`","to":[{"phone_number":"x","status":"queued"}]}}`)
		}
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/messages/"):
		f.reads++
		errs := "[]"
		if f.msgError != "" {
			errs = `[{"code":"` + f.msgError + `"}]`
		}
		_, _ = io.WriteString(w, `{"data":{"to":[{"status":"`+f.msgStatus+`"}],"errors":`+errs+`}}`)
	case strings.HasPrefix(p, "/10dlc/") && f.regStatus != 0:
		w.WriteHeader(f.regStatus)
		_, _ = io.WriteString(w, `{"errors":[{"code":"10010"}]}`)
	case p == "/10dlc/brand":
		_, _ = io.WriteString(w, `{"records":[{"brandId":"B1","displayName":"Acme","entityType":"PRIVATE_PROFIT","status":"OK","identityStatus":"VERIFIED"}]}`)
	case p == "/10dlc/campaign":
		_, _ = io.WriteString(w, `{"records":[{"campaignId":"C1","brandId":"B1","usecase":"CUSTOMER_CARE","status":"ACTIVE"}]}`)
	case p == "/10dlc/phone_number_campaigns":
		_, _ = io.WriteString(w, `{"records":[{"phoneNumber":"`+telnyxDID+`","campaignId":"C1","assignmentStatus":"ASSIGNED"}]}`)
	case p == "/messaging_tollfree/verification/requests":
		_, _ = io.WriteString(w, `{"records":[{"phoneNumbers":[{"phoneNumber":"`+telnyxTollDID+`"}],"verificationStatus":"In Progress"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeTelnyx) set(fn func(*fakeTelnyx)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeTelnyx) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *fakeTelnyx) firstSend() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return map[string]any{}
	}
	return f.sends[0]
}

func (f *fakeTelnyx) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// telnyxRig is the inbox rig with a Telnyx channel and one number.
type telnyxRig struct {
	*inboxRig
	fake            *fakeTelnyx
	tChannel, tAddr int64
	box             *secret.Box
}

func newTelnyxRig(t *testing.T) *telnyxRig {
	t.Helper()
	r := &telnyxRig{inboxRig: newInboxRig(t), fake: newFakeTelnyx(t)}
	var err error
	if r.box, err = secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32))); err != nil {
		t.Fatal(err)
	}
	ch := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/channels", map[string]any{
		"kind": "sms", "provider": "telnyx", "name": "Telnyx", "apiKey": telnyxKey, "messagingProfileId": telnyxProfile}).json(t)
	r.tChannel = int64(ch["id"].(float64))
	ad := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses", map[string]any{
		"channelId": r.tChannel, "address": telnyxDID, "label": "Telnyx line"}).json(t)
	r.tAddr = int64(ad["id"].(float64))
	r.admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{
		"firstContact": "attested", "firstContactPerUserHour": 1000, "firstContactPerAddressDay": 1000})
	return r
}

func (r *telnyxRig) start(want int, to, body string) response {
	r.t.Helper()
	return r.admin.must(want, "POST", "/api/v1/inbox/conversations", map[string]any{
		"addressId": r.tAddr, "to": to, "body": body, "consentAttestation": true, "consentNote": "asked for a callback by text"})
}

func (r *telnyxRig) lastMessage(conv int64) (id int64, status, providerID, errCode string) {
	r.t.Helper()
	err := r.db.QueryRowContext(context.Background(), `
		SELECT id, status, coalesce(provider_message_id, ''), error_code FROM conversation_messages
		WHERE conversation_id = $1 AND kind = 'message' AND direction = 'out' ORDER BY id DESC LIMIT 1`, conv).Scan(&id, &status, &providerID, &errCode)
	if err != nil {
		r.t.Fatal(err)
	}
	return
}

func (r *telnyxRig) sendOnce() int {
	r.t.Helper()
	n, err := (&messaging.Worker{Store: r.st}).SendOnce(context.Background(), 20)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *telnyxRig) pollOnce() {
	r.t.Helper()
	if _, err := r.db.ExecContext(context.Background(), `UPDATE conversation_messages SET next_poll_at = now() - interval '1 second' WHERE next_poll_at IS NOT NULL`); err != nil {
		r.t.Fatal(err)
	}
	if _, err := (&messaging.Worker{Store: r.st}).PollOnce(context.Background(), 20); err != nil {
		r.t.Fatal(err)
	}
}

func (r *telnyxRig) channelStatus() string {
	var s string
	if err := r.db.QueryRow(`SELECT status FROM msg_channels WHERE id = $1`, r.tChannel).Scan(&s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

var telnyxPeerSeq = 0

func telnyxPeer() string {
	telnyxPeerSeq++
	return fmt.Sprintf("+3247598%04d", telnyxPeerSeq)
}

// TestTelnyxChannelAPI fails if a Telnyx channel is accepted with a missing
// or refused key, if its key is stored unsealed or shown by any route, if the
// numbers route or the test route misreport, or if a Twilio field is
// accepted on it.
func TestTelnyxChannelAPI(t *testing.T) {
	r := newTelnyxRig(t)
	admin := r.admin
	chans := "/api/v1/messaging/channels"
	body := func(m map[string]any) map[string]any {
		b := map[string]any{"kind": "sms", "provider": "telnyx", "name": "Other", "apiKey": telnyxKey}
		for k, v := range m {
			if v == nil {
				delete(b, k)
			} else {
				b[k] = v
			}
		}
		return b
	}
	t.Run("validation", func(t *testing.T) {
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"apiKey": nil}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"apiKey": "has space"}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"messagingProfileId": "nope"}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"trunkId": r.trunk}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"accountSid": fakeSID}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"authToken": "x"}))
		admin.must(http.StatusBadRequest, "POST", chans, map[string]any{"kind": "sms", "provider": "twilio", "name": "T", "accountSid": fakeSID, "authToken": fakeToken, "apiKey": "k"})
		admin.must(http.StatusBadRequest, "POST", chans, map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "X", "trunkId": r.trunk, "apiKey": "k"})
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"provider": "vonage"}))
		admin.must(http.StatusConflict, "POST", chans, body(map[string]any{"name": "telnyx"}))
	})
	t.Run("carrier refuses or is down", func(t *testing.T) {
		admin.must(http.StatusUnprocessableEntity, "POST", chans, body(map[string]any{"apiKey": "wrong-key"}))
		r.fake.set(func(f *fakeTelnyx) { f.numbersStatus = http.StatusServiceUnavailable })
		admin.must(http.StatusBadGateway, "POST", chans, body(nil))
		r.fake.set(func(f *fakeTelnyx) { f.numbersStatus = 0 })
		var n int
		if err := r.db.QueryRow(`SELECT count(*) FROM msg_channels WHERE name = 'Other'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("a refused channel was saved (%d, %v)", n, err)
		}
	})
	t.Run("sealed and never shown", func(t *testing.T) {
		var sealed []byte
		var fp string
		if err := r.db.QueryRow(`SELECT credentials_enc, credential_fp FROM msg_channels WHERE id = $1`, r.tChannel).Scan(&sealed, &fp); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(sealed), telnyxKey) || strings.Contains(fp, telnyxKey) {
			t.Fatal("the key is stored in the clear")
		}
		plain, err := r.box.Open(sealed, secret.MsgChannelAAD(r.tChannel))
		if err != nil || !strings.Contains(plain, telnyxKey) {
			t.Fatalf("the stored credentials do not open: %v", err)
		}
		for _, p := range []string{chans, chans + "/" + fmt.Sprint(r.tChannel), chans + "/" + fmt.Sprint(r.tChannel) + "/registration"} {
			raw := string(admin.must(http.StatusOK, "GET", p, nil).body)
			for _, bad := range []string{telnyxKey, "apiKey", "credentials_enc"} {
				if strings.Contains(raw, bad) {
					t.Errorf("GET %s shows %q", p, bad)
				}
			}
		}
		c := admin.must(http.StatusOK, "GET", idPath(chans, r.tChannel, ""), nil).json(t)
		if c["hasCredentials"] != true || c["accountHint"] != "3e4f" || c["messagingService"] != true || c["trunkId"] != nil || c["provider"] != "telnyx" {
			t.Errorf("channel = %v", c)
		}
		var logged int
		if err := r.db.QueryRow(`SELECT count(*) FROM audit_events WHERE actor || action || resource || resource_id LIKE '%' || $1 || '%'`, telnyxKey).Scan(&logged); err != nil || logged != 0 {
			t.Errorf("the key is in the audit log (%d, %v)", logged, err)
		}
	})
	t.Run("update", func(t *testing.T) {
		path := idPath(chans, r.tChannel, "")
		admin.must(http.StatusOK, "PUT", path, map[string]any{"name": "Telnyx renamed"})
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"authToken": fakeToken})
		admin.must(http.StatusUnprocessableEntity, "PUT", path, map[string]any{"apiKey": "wrong"})
		admin.must(http.StatusOK, "PUT", path, map[string]any{"apiKey": telnyxKey})
		if c := admin.must(http.StatusOK, "GET", path, nil).json(t); c["messagingService"] != false || c["accountHint"] != "" {
			t.Errorf("channel = %v", c)
		}
		admin.must(http.StatusOK, "PUT", path, map[string]any{"apiKey": telnyxKey, "messagingProfileId": telnyxProfile})
	})
	t.Run("numbers", func(t *testing.T) {
		got := listItems(t, admin.must(http.StatusOK, "GET", idPath(chans, r.tChannel, "/numbers"), nil))
		first, _ := got[0].(map[string]any)
		if len(got) != 3 || first["number"] != telnyxDID || first["sms"] != true || first["mms"] != true {
			t.Fatalf("numbers = %v", got)
		}
	})
	t.Run("test route", func(t *testing.T) {
		res := admin.must(http.StatusOK, "POST", idPath(chans, r.tChannel, "/test"), nil).json(t)
		if res["ok"] != true || r.fake.sendCount() != 0 {
			t.Fatalf("test = %v (%d sends)", res, r.fake.sendCount())
		}
		r.fake.set(func(f *fakeTelnyx) { f.numbersStatus = http.StatusUnauthorized })
		res = admin.must(http.StatusOK, "POST", idPath(chans, r.tChannel, "/test"), nil).json(t)
		if res["ok"] != false || strings.Contains(string(mustJSON(res)), telnyxKey) {
			t.Fatalf("test = %v", res)
		}
		r.fake.set(func(f *fakeTelnyx) { f.numbersStatus = 0 })
	})
}

// TestTelnyxSendAndStatus fails if a Telnyx text is not sent exactly once
// with the right request, is sent again after a lost answer, reaches the
// carrier after an opt-out, does not record Telnyx's STOP block (40300) as
// Hello's own opt-out, is not moved to its final state by status polling, or
// is not held when the key is refused.
func TestTelnyxSendAndStatus(t *testing.T) {
	r := newTelnyxRig(t)

	// Accepted: one POST, sent, with the carrier's id and the profile.
	c1 := int64(r.start(http.StatusCreated, telnyxPeer(), "first text é").json(t)["id"].(float64))
	if got := r.sendOnce(); got != 1 {
		t.Fatalf("handled %d", got)
	}
	_, st, pid, _ := r.lastMessage(c1)
	if st != "sent" || pid != fmt.Sprintf(telnyxMsgID, 1) {
		t.Fatalf("status = %q, provider id = %q", st, pid)
	}
	if r.sendOnce() != 0 || r.fake.sendCount() != 1 {
		t.Fatalf("the text was sent again (%d sends)", r.fake.sendCount())
	}
	if b := r.fake.firstSend(); b["from"] != telnyxDID || b["text"] != "first text é" || b["messaging_profile_id"] != telnyxProfile || b["type"] != "SMS" {
		t.Errorf("request = %v", b)
	}

	// Status polling: in flight stays sent; delivered settles and stops.
	r.fake.set(func(f *fakeTelnyx) { f.msgStatus = "sent" })
	r.pollOnce()
	if _, st, _, _ := r.lastMessage(c1); st != "sent" {
		t.Fatalf("status = %q while in flight", st)
	}
	r.fake.set(func(f *fakeTelnyx) { f.msgStatus = "delivered" })
	r.pollOnce()
	if _, st, _, _ := r.lastMessage(c1); st != "delivered" {
		t.Fatalf("status = %q, want delivered", st)
	}
	reads := r.fake.readCount()
	r.pollOnce()
	if r.fake.readCount() != reads {
		t.Fatal("a delivered text was polled again")
	}

	// Failed with Telnyx's code.
	c2 := int64(r.start(http.StatusCreated, telnyxPeer(), "will bounce").json(t)["id"].(float64))
	r.sendOnce()
	r.fake.set(func(f *fakeTelnyx) { f.msgStatus = "delivery_failed"; f.msgError = "40008" })
	r.pollOnce()
	if _, st, _, code := r.lastMessage(c2); st != "failed" || code != "40008" {
		t.Fatalf("status = %q, code = %q", st, code)
	}

	// Older than 48 hours: not asked about any more.
	c3 := int64(r.start(http.StatusCreated, telnyxPeer(), "old").json(t)["id"].(float64))
	r.sendOnce()
	old, _, _, _ := r.lastMessage(c3)
	if _, err := r.db.Exec(`UPDATE conversation_messages SET sent_at = now() - interval '49 hours' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	reads = r.fake.readCount()
	r.fake.set(func(f *fakeTelnyx) { f.msgStatus = "delivered"; f.msgError = "" })
	r.pollOnce()
	if r.fake.readCount() != reads {
		t.Fatal("a text older than 48 hours was polled")
	}

	// A lost answer is never sent again.
	c4 := int64(r.start(http.StatusCreated, telnyxPeer(), "may or may not arrive").json(t)["id"].(float64))
	before := r.fake.sendCount()
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "lost" })
	r.sendOnce()
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "ok" })
	r.sendOnce()
	r.sendOnce()
	if _, st, pid, _ := r.lastMessage(c4); st != "sending" || pid != "" || r.fake.sendCount() != before+1 {
		t.Fatalf("status = %q, provider id = %q, %d sends", st, pid, r.fake.sendCount()-before)
	}

	// A refusal fails the text with Telnyx's code and leaves the channel on.
	c5 := int64(r.start(http.StatusCreated, telnyxPeer(), "x").json(t)["id"].(float64))
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "reject" })
	r.sendOnce()
	if _, st, _, code := r.lastMessage(c5); st != "failed" || code != "40310" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	if r.channelStatus() != "ok" {
		t.Error("a bad number degraded the channel")
	}

	// Telnyx's STOP block (40300) is Hello's opt-out too.
	peer := telnyxPeer()
	c6 := int64(r.start(http.StatusCreated, peer, "x").json(t)["id"].(float64))
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "optout" })
	r.sendOnce()
	if _, st, _, code := r.lastMessage(c6); st != "failed" || code != provider.CodeOptedOut {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	var source string
	if err := r.db.QueryRow(`SELECT source FROM sms_opt_outs WHERE address_id = $1 AND state = 'out' AND peer_key = $2`, r.tAddr, peer).Scan(&source); err != nil || source != "provider" {
		t.Fatalf("opt-out source = %q, %v", source, err)
	}
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "ok" })
	r.admin.must(http.StatusUnprocessableEntity, "POST", idPath("/api/v1/inbox/conversations", c6, "/messages"), map[string]any{"body": "again"})

	// ... and so is the same code on a status read.
	peer = telnyxPeer()
	r.start(http.StatusCreated, peer, "blocked later")
	r.sendOnce()
	r.fake.set(func(f *fakeTelnyx) { f.msgStatus = "delivery_failed"; f.msgError = "40300" })
	r.pollOnce()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM sms_opt_outs WHERE address_id = $1 AND peer_key = $2 AND state = 'out' AND source = 'provider'`, r.tAddr, peer).Scan(&n); err != nil || n != 1 {
		t.Fatalf("opt-outs = %d, %v", n, err)
	}
	r.fake.set(func(f *fakeTelnyx) { f.msgStatus = "delivered"; f.msgError = "" })

	// A refused key stops the channel; a new key starts clean.
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "unauth" })
	c7 := int64(r.start(http.StatusCreated, telnyxPeer(), "login refused").json(t)["id"].(float64))
	r.sendOnce()
	if _, st, _, code := r.lastMessage(c7); st != "failed" || code != "credentials_invalid" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	if r.channelStatus() != "credentials_invalid" {
		t.Fatalf("channel status = %q", r.channelStatus())
	}
	r.fake.set(func(f *fakeTelnyx) { f.sendMode = "ok" })
	r.admin.must(http.StatusOK, "PUT", idPath("/api/v1/messaging/channels", r.tChannel, ""), map[string]any{"apiKey": telnyxKey})
	if r.channelStatus() != "ok" {
		t.Fatalf("channel status = %q after a new key", r.channelStatus())
	}
}

// TestChannelRegistrationAPI fails if the registration route does not report
// a Telnyx account's brand, campaign, number states and checklist, shows a
// secret, answers for a SIP trunk channel, degrades to an error when the
// carrier will not answer one section, or lets a non-admin read it.
func TestChannelRegistrationAPI(t *testing.T) {
	r := newTelnyxRig(t)
	reg := func(id int64) string { return idPath("/api/v1/messaging/channels", id, "/registration") }

	got := r.admin.must(http.StatusOK, "GET", reg(r.tChannel), nil).json(t)
	if got["provider"] != "telnyx" || got["supported"] != true {
		t.Fatalf("report = %v", got)
	}
	brands, _ := got["brands"].([]any)
	if len(brands) != 1 || brands[0].(map[string]any)["state"] != "approved" {
		t.Errorf("brands = %v", got["brands"])
	}
	states := map[string]string{}
	added := map[string]bool{}
	for _, n := range got["numbers"].([]any) {
		m := n.(map[string]any)
		states[m["number"].(string)] = m["kind"].(string) + "/" + m["state"].(string)
		added[m["number"].(string)] = m["added"].(bool)
	}
	if states[telnyxDID] != "10dlc/approved" || states[telnyxTollDID] != "toll_free/pending" || states["+3270999222"] != "other/unknown" {
		t.Errorf("numbers = %v", states)
	}
	if !added[telnyxDID] || added[telnyxTollDID] {
		t.Errorf("added = %v", added)
	}
	check := map[string]string{}
	for _, c := range got["checklist"].([]any) {
		m := c.(map[string]any)
		check[m["id"].(string)] = m["state"].(string)
		if m["link"] == "" {
			t.Errorf("checklist item %v has no link", m["id"])
		}
	}
	want := map[string]string{"brand": "done", "campaign": "done", "numbers": "done", "tollfree": "pending", "samples": "manual", "stophelp": "todo"}
	for k, v := range want {
		if check[k] != v {
			t.Errorf("checklist[%s] = %q, want %q", k, check[k], v)
		}
	}
	if strings.Contains(string(mustJSON(got)), telnyxKey) {
		t.Error("the report shows the key")
	}
	// Answering HELP completes the last step.
	r.admin.must(http.StatusOK, "PUT", idPath("/api/v1/messaging/addresses", r.tAddr, ""), map[string]any{"helpText": "Reply STOP to opt out. Help: 555-0100"})
	for _, c := range r.admin.must(http.StatusOK, "GET", reg(r.tChannel), nil).json(t)["checklist"].([]any) {
		if m := c.(map[string]any); m["id"] == "stophelp" && m["state"] != "done" {
			t.Errorf("stophelp = %v", m)
		}
	}

	// A key without the 10DLC permission still reads toll-free.
	r.fake.set(func(f *fakeTelnyx) { f.regStatus = http.StatusForbidden })
	got = r.admin.must(http.StatusOK, "GET", reg(r.tChannel), nil).json(t)
	if len(got["unavailable"].([]any)) != 2 {
		t.Errorf("unavailable = %v", got["unavailable"])
	}
	for _, n := range got["numbers"].([]any) {
		if m := n.(map[string]any); m["number"] == telnyxDID && m["state"] != "unknown" {
			t.Errorf("a 10DLC number is %v without the campaign data", m["state"])
		}
	}
	r.fake.set(func(f *fakeTelnyx) { f.regStatus = 0; f.numbersStatus = http.StatusServiceUnavailable })
	r.admin.must(http.StatusBadGateway, "GET", reg(r.tChannel), nil)
	r.fake.set(func(f *fakeTelnyx) { f.numbersStatus = 0 })

	r.admin.must(http.StatusConflict, "GET", reg(r.channel), nil)
	r.admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/channels/99999/registration", nil)
	r.operator.must(http.StatusForbidden, "GET", reg(r.tChannel), nil)
	r.agent.must(http.StatusForbidden, "GET", reg(r.tChannel), nil)
	if _, err := r.db.Exec(`UPDATE msg_channels SET credentials_enc = '\x00' WHERE id = $1`, r.tChannel); err != nil {
		t.Fatal(err)
	}
	r.admin.must(http.StatusConflict, "GET", reg(r.tChannel), nil)
}

// TestTwilioChannelRegistrationAPI is the same route against Twilio's
// Messaging API: the brand, the campaign of the Messaging Service and the
// numbers outside the US are reported, and a refusal of the Messaging API is
// a degraded report, not an error.
func TestTwilioChannelRegistrationAPI(t *testing.T) {
	r := newTwilioRig(t)
	path := idPath("/api/v1/messaging/channels", r.tChannel, "/registration")
	got := r.admin.must(http.StatusOK, "GET", path, nil).json(t)
	if got["provider"] != "twilio" || got["supported"] != true {
		t.Fatalf("report = %v", got)
	}
	brands, _ := got["brands"].([]any)
	campaigns, _ := got["campaigns"].([]any)
	if len(brands) != 1 || len(campaigns) != 1 || campaigns[0].(map[string]any)["state"] != "approved" {
		t.Errorf("brands = %v, campaigns = %v", got["brands"], got["campaigns"])
	}
	nums, _ := got["numbers"].([]any)
	if len(nums) != 1 || nums[0].(map[string]any)["kind"] != "other" || nums[0].(map[string]any)["state"] != "unknown" || nums[0].(map[string]any)["added"] != true {
		t.Errorf("numbers = %v", got["numbers"])
	}
	if strings.Contains(string(mustJSON(got)), fakeToken) {
		t.Error("the report shows the token")
	}
	r.fake.set(func(f *fakeTwilio) { f.regStatus = http.StatusForbidden })
	// Nothing readable at all is the carrier's refusal.
	r.admin.must(http.StatusUnprocessableEntity, "GET", path, nil)
	r.fake.set(func(f *fakeTwilio) { f.regStatus = 0 })
	r.admin.must(http.StatusOK, "GET", path, nil)
}
