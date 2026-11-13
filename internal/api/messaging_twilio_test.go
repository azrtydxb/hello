package api

// Messaging item 2, sending through Twilio, against PostgreSQL and a fake of
// Twilio's REST API: the channel and its sealed credentials, first contact,
// the at-most-once send worker, status polling and the opt-out a carrier
// reports. The package's conformance validator checks every request and
// response against openapi.json.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/messaging/provider/twilio"
	"github.com/azrtydxb/hello/internal/secret"
)

const (
	fakeToken = "tok-0123456789-do-not-log"
	fakeMsgID = "SM%032d"
	twilioDID = "+3270999000"
)

// The SIDs are built at run time: a 34-character literal in the source looks
// like a real Twilio identifier to secret scanners.
var (
	fakeSID = "AC" + strings.Repeat("ab", 16)
	fakeSvc = "MG" + strings.Repeat("ef", 16)
)

// fakeTwilio is Twilio's REST API as far as Hello uses it. Each field is a
// switch a test flips.
type fakeTwilio struct {
	srv *httptest.Server
	mu  sync.Mutex
	// accountStatus is the status of the account fetch (0 means 200).
	accountStatus int
	// sendMode is ok, lost (the connection drops), optout (21610), reject
	// (21211) or unauth (401).
	sendMode string
	// msgStatus and msgError are what a status read answers.
	msgStatus string
	msgError  int
	sends     []url.Values
	reads     int
	seq       int
}

func newFakeTwilio(t *testing.T) *fakeTwilio {
	t.Helper()
	f := &fakeTwilio{sendMode: "ok", msgStatus: "delivered"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	provider.Register(twilio.Kind, twilio.Factory(twilio.WithBaseURL(f.srv.URL), twilio.WithHTTPClient(f.srv.Client())))
	t.Cleanup(func() { provider.Register(twilio.Kind, twilio.Factory()) })
	return f
}

func (f *fakeTwilio) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, pass, _ := r.BasicAuth()
	if user != fakeSID || pass != fakeToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/IncomingPhoneNumbers.json"):
		_, _ = io.WriteString(w, `{"incoming_phone_numbers":[{"phone_number":"`+twilioDID+`","friendly_name":"Main","capabilities":{"sms":true,"mms":true,"voice":true}}]}`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, fakeSID+".json"):
		if f.accountStatus != 0 {
			w.WriteHeader(f.accountStatus)
			return
		}
		_, _ = io.WriteString(w, `{"status":"active"}`)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/Messages.json"):
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		f.sends = append(f.sends, form)
		switch f.sendMode {
		case "lost":
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
				}
			}
		case "optout":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":21610,"message":"blocked"}`)
		case "reject":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":21211,"message":"invalid"}`)
		case "unauth":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			f.seq++
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"sid":"`+fmt.Sprintf(fakeMsgID, f.seq)+`","status":"queued"}`)
		}
	case r.Method == http.MethodGet && strings.Contains(p, "/Messages/"):
		f.reads++
		ec := "null"
		if f.msgError != 0 {
			ec = strconv.Itoa(f.msgError)
		}
		_, _ = io.WriteString(w, `{"status":"`+f.msgStatus+`","error_code":`+ec+`}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeTwilio) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *fakeTwilio) firstSend() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return url.Values{}
	}
	return f.sends[0]
}

func (f *fakeTwilio) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *fakeTwilio) set(fn func(*fakeTwilio)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// twilioRig is the inbox rig with a Twilio channel and one number.
type twilioRig struct {
	*inboxRig
	fake            *fakeTwilio
	tChannel, tAddr int64
	box             *secret.Box
}

func newTwilioRig(t *testing.T) *twilioRig {
	t.Helper()
	r := &twilioRig{inboxRig: newInboxRig(t), fake: newFakeTwilio(t)}
	var err error
	if r.box, err = secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32))); err != nil {
		t.Fatal(err)
	}
	ch := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/channels", map[string]any{
		"kind": "sms", "provider": "twilio", "name": "Twilio", "accountSid": fakeSID, "authToken": fakeToken}).json(t)
	r.tChannel = int64(ch["id"].(float64))
	ad := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses", map[string]any{
		"channelId": r.tChannel, "address": twilioDID, "label": "Twilio line"}).json(t)
	r.tAddr = int64(ad["id"].(float64))
	return r
}

func (r *twilioRig) allowFirstContact(perUserHour, perAddressDay int) {
	r.t.Helper()
	r.admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{
		"firstContact": "attested", "firstContactPerUserHour": perUserHour, "firstContactPerAddressDay": perAddressDay})
}

var twilioPeerSeq = 0

func twilioPeer() string {
	twilioPeerSeq++
	return fmt.Sprintf("+3247599%04d", twilioPeerSeq)
}

func (r *twilioRig) start(want int, to, body string) response {
	r.t.Helper()
	return r.admin.must(want, "POST", "/api/v1/inbox/conversations", map[string]any{
		"addressId": r.tAddr, "to": to, "body": body, "consentAttestation": true, "consentNote": "asked for a callback by text"})
}

// lastMessage returns the newest customer-facing message of a conversation.
func (r *twilioRig) lastMessage(conv int64) (id int64, status, providerID, errCode string) {
	r.t.Helper()
	err := r.db.QueryRowContext(context.Background(), `
		SELECT id, status, coalesce(provider_message_id, ''), error_code FROM conversation_messages
		WHERE conversation_id = $1 AND kind = 'message' AND direction = 'out' ORDER BY id DESC LIMIT 1`, conv).Scan(&id, &status, &providerID, &errCode)
	if err != nil {
		r.t.Fatal(err)
	}
	return
}

func (r *twilioRig) worker() *messaging.Worker { return &messaging.Worker{Store: r.st} }

func (r *twilioRig) sendOnce() int {
	r.t.Helper()
	n, err := r.worker().SendOnce(context.Background(), 20)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *twilioRig) pollOnce() {
	r.t.Helper()
	// Make every open read due.
	if _, err := r.db.ExecContext(context.Background(), `UPDATE conversation_messages SET next_poll_at = now() - interval '1 second' WHERE next_poll_at IS NOT NULL`); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.worker().PollOnce(context.Background(), 20); err != nil {
		r.t.Fatal(err)
	}
}

// TestTwilioChannelAPI fails if a Twilio channel is accepted with bad or
// refused credentials, if its credentials are stored unsealed or shown by any
// route, if the numbers route or the test route misreport, or if a SIP
// channel's behaviour changed.
func TestTwilioChannelAPI(t *testing.T) {
	r := newTwilioRig(t)
	admin := r.admin
	chans := "/api/v1/messaging/channels"
	body := func(m map[string]any) map[string]any {
		b := map[string]any{"kind": "sms", "provider": "twilio", "name": "Other", "accountSid": fakeSID, "authToken": fakeToken}
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
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"accountSid": nil}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"authToken": nil}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"accountSid": "nope"}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"trunkId": r.trunk}))
		admin.must(http.StatusBadRequest, "POST", chans, body(map[string]any{"messagingServiceSid": "MGnope"}))
		admin.must(http.StatusBadRequest, "POST", chans, map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "X", "trunkId": r.trunk, "authToken": "x"})
		admin.must(http.StatusConflict, "POST", chans, body(map[string]any{"name": "twilio"}))
	})
	t.Run("carrier refuses or is down", func(t *testing.T) {
		admin.must(http.StatusUnprocessableEntity, "POST", chans, body(map[string]any{"authToken": "wrong-token"}))
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = http.StatusServiceUnavailable })
		admin.must(http.StatusBadGateway, "POST", chans, body(nil))
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = 0 })
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
		if strings.Contains(string(sealed), fakeToken) || strings.Contains(fp, fakeToken) {
			t.Fatal("the token is stored in the clear")
		}
		plain, err := r.box.Open(sealed, secret.MsgChannelAAD(r.tChannel))
		if err != nil || !strings.Contains(plain, fakeToken) {
			t.Fatalf("the stored credentials do not open: %v", err)
		}
		if _, err := r.box.Open(sealed, secret.MsgChannelAAD(r.tChannel+1)); err == nil {
			t.Error("the credentials open under another channel's data")
		}
		for _, p := range []string{chans, chans + "/" + strconv.FormatInt(r.tChannel, 10)} {
			raw := string(admin.must(http.StatusOK, "GET", p, nil).body)
			for _, bad := range []string{fakeToken, "authToken", "credentials_enc"} {
				if strings.Contains(raw, bad) {
					t.Errorf("GET %s shows %q", p, bad)
				}
			}
		}
		c := admin.must(http.StatusOK, "GET", idPath(chans, r.tChannel, ""), nil).json(t)
		if c["hasCredentials"] != true || c["accountHint"] != "abab" || c["trunkId"] != nil || c["provider"] != "twilio" {
			t.Errorf("channel = %v", c)
		}
		var logged int
		if err := r.db.QueryRow(`SELECT count(*) FROM audit_events WHERE actor || action || resource || resource_id LIKE '%' || $1 || '%'`, fakeToken).Scan(&logged); err != nil || logged != 0 {
			t.Errorf("the token is in the audit log (%d, %v)", logged, err)
		}
	})
	t.Run("update", func(t *testing.T) {
		path := idPath(chans, r.tChannel, "")
		admin.must(http.StatusOK, "PUT", path, map[string]any{"name": "Twilio renamed"})
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"accountSid": fakeSID})
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"trunkId": r.trunk})
		admin.must(http.StatusUnprocessableEntity, "PUT", path, map[string]any{"accountSid": fakeSID, "authToken": "wrong"})
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = http.StatusBadGateway })
		admin.must(http.StatusBadGateway, "PUT", path, map[string]any{"accountSid": fakeSID, "authToken": fakeToken})
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = 0 })
		admin.must(http.StatusOK, "PUT", path, map[string]any{"accountSid": fakeSID, "authToken": fakeToken, "messagingServiceSid": fakeSvc})
		if c := admin.must(http.StatusOK, "GET", path, nil).json(t); c["messagingService"] != true {
			t.Errorf("channel = %v", c)
		}
		admin.must(http.StatusOK, "PUT", path, map[string]any{"accountSid": fakeSID, "authToken": fakeToken, "messagingServiceSid": ""})
	})
	t.Run("numbers", func(t *testing.T) {
		got := listItems(t, admin.must(http.StatusOK, "GET", idPath(chans, r.tChannel, "/numbers"), nil))
		first, _ := got[0].(map[string]any)
		if len(got) != 1 || first["number"] != twilioDID || first["sms"] != true {
			t.Fatalf("numbers = %v", got)
		}
		admin.must(http.StatusNotFound, "GET", chans+"/99999/numbers", nil)
		admin.must(http.StatusConflict, "GET", idPath(chans, r.channel, "/numbers"), nil)
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = 0 })
		// The carrier refuses the stored credentials (the token was rotated).
		sealed, err := r.box.Seal(`{"accountSid":"`+fakeSID+`","authToken":"rotated"}`, secret.MsgChannelAAD(r.tChannel))
		if err != nil {
			t.Fatal(err)
		}
		saved := mustSealed(t, r, r.tChannel)
		if _, err := r.db.Exec(`UPDATE msg_channels SET credentials_enc = $2 WHERE id = $1`, r.tChannel, sealed); err != nil {
			t.Fatal(err)
		}
		admin.must(http.StatusUnprocessableEntity, "GET", idPath(chans, r.tChannel, "/numbers"), nil)
		// Unreadable credentials.
		if _, err := r.db.Exec(`UPDATE msg_channels SET credentials_enc = '\x00' WHERE id = $1`, r.tChannel); err != nil {
			t.Fatal(err)
		}
		admin.must(http.StatusConflict, "GET", idPath(chans, r.tChannel, "/numbers"), nil)
		if _, err := r.db.Exec(`UPDATE msg_channels SET credentials_enc = $2 WHERE id = $1`, r.tChannel, saved); err != nil {
			t.Fatal(err)
		}
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = 0 })
		// Twilio cannot be reached: a down account read does not apply to the
		// numbers list, so break it by pointing the adapter at a closed port.
		dead := httptest.NewServer(http.NotFoundHandler())
		base := dead.URL
		dead.Close()
		provider.Register(twilio.Kind, twilio.Factory(twilio.WithBaseURL(base), twilio.WithHTTPClient(&http.Client{})))
		admin.must(http.StatusBadGateway, "GET", idPath(chans, r.tChannel, "/numbers"), nil)
		provider.Register(twilio.Kind, twilio.Factory(twilio.WithBaseURL(r.fake.srv.URL), twilio.WithHTTPClient(r.fake.srv.Client())))
	})
	t.Run("test route", func(t *testing.T) {
		res := admin.must(http.StatusOK, "POST", idPath(chans, r.tChannel, "/test"), nil).json(t)
		if res["ok"] != true {
			t.Fatalf("test = %v", res)
		}
		if r.fake.sendCount() != 0 {
			t.Error("the test sent a text")
		}
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = http.StatusUnauthorized })
		res = admin.must(http.StatusOK, "POST", idPath(chans, r.tChannel, "/test"), nil).json(t)
		if res["ok"] != false || strings.Contains(string(mustJSON(res)), fakeToken) {
			t.Fatalf("test = %v", res)
		}
		r.fake.set(func(f *fakeTwilio) { f.accountStatus = 0 })
	})
	t.Run("addresses", func(t *testing.T) {
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.tChannel, "address": "3270999111"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.tChannel, "address": "+3270999111", "sendsPerMinute": 0})
		a := admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses", map[string]any{"channelId": r.tChannel, "address": "+3270999111", "sendsPerMinute": 5}).json(t)
		if a["sendsPerMinute"] != float64(5) {
			t.Errorf("address = %v", a)
		}
		if def := admin.must(http.StatusOK, "GET", idPath("/api/v1/messaging/addresses", r.tAddr, ""), nil).json(t); def["sendsPerMinute"] != float64(60) {
			t.Errorf("default = %v", def)
		}
	})
	t.Run("settings", func(t *testing.T) {
		if s := admin.must(http.StatusOK, "GET", "/api/v1/messaging/settings", nil).json(t); s["firstContact"] != "off" || s["firstContactPerUserHour"] != float64(20) || s["firstContactPerAddressDay"] != float64(200) {
			t.Fatalf("defaults = %v", s)
		}
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/settings", map[string]any{"firstContact": "anyone"})
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/settings", map[string]any{"firstContactPerUserHour": 0})
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messaging/settings", map[string]any{"firstContactPerAddressDay": 10001})
	})
}

func mustSealed(t *testing.T, r *twilioRig, id int64) []byte {
	t.Helper()
	var b []byte
	if err := r.db.QueryRow(`SELECT credentials_enc FROM msg_channels WHERE id = $1`, id).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func mustJSON(m map[string]any) []byte {
	var sb strings.Builder
	fmt.Fprint(&sb, m)
	return []byte(sb.String())
}

// TestFirstContactGuard fails if a new outbound conversation is created while
// first contact is off, without an attestation and its note, to an opted-out
// or non-international number, to a customer who has an open conversation,
// from a SIP number, beyond the per-agent or per-number limits, or by a role
// that has no inbox supervision.
func TestFirstContactGuard(t *testing.T) {
	r := newTwilioRig(t)
	to := twilioPeer()

	r.start(http.StatusConflict, to, "hello") // off by default
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM conversations WHERE origin = 'outbound'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a conversation was created while first contact is off (%d, %v)", n, err)
	}

	if got := r.admin.must(http.StatusOK, "GET", "/api/v1/me/inbox/status", nil).json(t)["senders"].([]any); len(got) != 0 {
		t.Fatalf("senders while first contact is off = %v", got)
	}
	r.allowFirstContact(1000, 1000)
	senders := r.admin.must(http.StatusOK, "GET", "/api/v1/me/inbox/status", nil).json(t)["senders"].([]any)
	if first, _ := senders[0].(map[string]any); len(senders) != 1 || first["address"] != twilioDID || first["addressId"] != float64(r.tAddr) {
		t.Fatalf("senders = %v", senders)
	}
	if got := r.agent.must(http.StatusOK, "GET", "/api/v1/me/inbox/status", nil).json(t)["senders"].([]any); len(got) != 0 {
		t.Fatalf("an agent sees senders: %v", got)
	}
	post := func(want int, m map[string]any) response {
		b := map[string]any{"addressId": r.tAddr, "to": to, "body": "hello", "consentAttestation": true, "consentNote": "asked by text"}
		for k, v := range m {
			if v == nil {
				delete(b, k)
			} else {
				b[k] = v
			}
		}
		return r.admin.must(want, "POST", "/api/v1/inbox/conversations", b)
	}
	post(http.StatusBadRequest, map[string]any{"consentAttestation": false})
	post(http.StatusBadRequest, map[string]any{"consentAttestation": nil})
	post(http.StatusBadRequest, map[string]any{"consentNote": ""})
	post(http.StatusBadRequest, map[string]any{"consentNote": strings.Repeat("x", 501)})
	post(http.StatusBadRequest, map[string]any{"to": "0475123456"})
	post(http.StatusBadRequest, map[string]any{"to": "+32 475 12"})
	post(http.StatusBadRequest, map[string]any{"body": ""})
	post(http.StatusBadRequest, map[string]any{"body": strings.Repeat("a", 1601)})
	post(http.StatusBadRequest, map[string]any{"addressId": 0})
	post(http.StatusBadRequest, map[string]any{"addressId": 999999})
	post(http.StatusConflict, map[string]any{"addressId": r.address}) // a SIP number cannot start one
	r.agent.must(http.StatusForbidden, "POST", "/api/v1/inbox/conversations", map[string]any{})
	r.viewer.must(http.StatusForbidden, "POST", "/api/v1/inbox/conversations", map[string]any{})

	// Opted out: refused, and nothing is created.
	out := twilioPeer()
	r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/optouts", map[string]any{"addressId": r.tAddr, "peer": out, "note": "asked by phone"})
	post(http.StatusUnprocessableEntity, map[string]any{"to": out})

	conv := post(http.StatusCreated, nil).json(t)
	id := int64(conv["id"].(float64))
	if conv["status"] != "open" || conv["assigneeUserId"] == nil || conv["channelName"] != "Twilio" {
		t.Fatalf("conversation = %v", conv)
	}
	var origin, note string
	var creator int64
	if err := r.db.QueryRow(`SELECT origin, consent_note, created_by_user_id FROM conversations WHERE id = $1`, id).Scan(&origin, &note, &creator); err != nil {
		t.Fatal(err)
	}
	if origin != "outbound" || note != "asked by text" || creator != r.adminID {
		t.Errorf("origin = %q, note = %q, creator = %d", origin, note, creator)
	}
	if _, st, _, _ := r.lastMessage(id); st != "queued" {
		t.Errorf("first message status = %q, want queued", st)
	}
	// The same customer again: reply in the open conversation instead.
	post(http.StatusConflict, nil)
	var audited int
	if err := r.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'first_contact attested' AND resource_id = $1`, strconv.FormatInt(id, 10)).Scan(&audited); err != nil || audited != 1 {
		t.Errorf("audit rows = %d, %v", audited, err)
	}
	var leaked int
	if err := r.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action || resource_id LIKE '%' || $1 || '%' OR action || resource_id LIKE '%asked by text%'`, to).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("a number or note is in the audit log (%d, %v)", leaked, err)
	}

	// Limits: one per agent per hour.
	r.allowFirstContact(1, 1000)
	post(http.StatusTooManyRequests, map[string]any{"to": twilioPeer()})
	// One per number per day.
	r.allowFirstContact(1000, 1)
	resp := post(http.StatusTooManyRequests, map[string]any{"to": twilioPeer()})
	if resp.header.Get("Retry-After") == "" {
		t.Error("the 429 has no Retry-After")
	}
	// Back within the limits: allowed.
	r.allowFirstContact(1000, 1000)
	post(http.StatusCreated, map[string]any{"to": twilioPeer()})

	// Turned off again: refused.
	r.admin.must(http.StatusOK, "PUT", "/api/v1/messaging/settings", map[string]any{"firstContact": "off"})
	post(http.StatusConflict, map[string]any{"to": twilioPeer()})
}

// TestSendAtMostOnce fails if a queued carrier text is not sent exactly once,
// is sent again after the carrier may have accepted it, reaches the carrier
// after the customer opted out, is not failed when the carrier refuses it,
// is not held when the channel's credentials are refused, or is sent beyond
// its number's throughput.
func TestSendAtMostOnce(t *testing.T) {
	r := newTwilioRig(t)
	r.allowFirstContact(1000, 1000)

	// Accepted: one POST, sent, with the carrier's id and the right form.
	c1 := int64(r.start(http.StatusCreated, twilioPeer(), "first text é").json(t)["id"].(float64))
	if got := r.sendOnce(); got != 1 {
		t.Fatalf("handled %d", got)
	}
	_, st, pid, _ := r.lastMessage(c1)
	if st != "sent" || pid != fmt.Sprintf(fakeMsgID, 1) {
		t.Fatalf("status = %q, provider id = %q", st, pid)
	}
	if r.sendOnce() != 0 || r.fake.sendCount() != 1 {
		t.Fatalf("the text was sent again (%d sends)", r.fake.sendCount())
	}
	if f := r.fake.firstSend(); f.Get("From") != twilioDID || f.Get("Body") != "first text é" || !strings.HasPrefix(f.Get("To"), "+32475") {
		t.Errorf("form = %v", f)
	}

	// A reply in the open conversation through the normal route.
	id := int64(0)
	reply := r.admin.must(http.StatusCreated, "POST", idPath("/api/v1/inbox/conversations", c1, "/messages"), map[string]any{"body": "a reply", "clientMsgId": "c-1"}).json(t)
	id = int64(reply["id"].(float64))
	if reply["status"] != "queued" {
		t.Errorf("reply = %v", reply)
	}
	again := r.admin.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", c1, "/messages"), map[string]any{"body": "a reply", "clientMsgId": "c-1"}).json(t)
	if int64(again["id"].(float64)) != id {
		t.Errorf("a retried POST made a second message")
	}
	// Over the carrier's 1600 characters (and under the API's 2000 bytes).
	r.admin.must(http.StatusBadRequest, "POST", idPath("/api/v1/inbox/conversations", c1, "/messages"), map[string]any{"body": strings.Repeat("a", 1601)})
}

// TestSendLostAnswer fails if a text whose answer was lost is sent again, is
// not left as sending, is not settled as unknown, or counts as delivered.
func TestSendLostAnswer(t *testing.T) {
	r := newTwilioRig(t)
	r.allowFirstContact(1000, 1000)
	conv := int64(r.start(http.StatusCreated, twilioPeer(), "may or may not arrive").json(t)["id"].(float64))
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "lost" })
	r.sendOnce()
	msg, st, pid, _ := r.lastMessage(conv)
	if st != "sending" || pid != "" {
		t.Fatalf("status = %q, provider id = %q", st, pid)
	}
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "ok" })
	for i := 0; i < 3; i++ {
		r.sendOnce()
	}
	if r.fake.sendCount() != 1 {
		t.Fatalf("a text with a lost answer was sent %d times", r.fake.sendCount())
	}
	// Not yet old enough: left alone. Then settled as unknown.
	if _, _, err := r.st.SettleCarrierTexts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, st, _, _ := r.lastMessage(conv); st != "sending" {
		t.Fatalf("a fresh send was settled early: %q", st)
	}
	if _, err := r.db.Exec(`UPDATE conversation_messages SET claimed_at = now() - interval '10 minutes' WHERE id = $1`, msg); err != nil {
		t.Fatal(err)
	}
	unknown, _, err := r.st.SettleCarrierTexts(context.Background())
	if err != nil || unknown != 1 {
		t.Fatalf("settled %d, %v", unknown, err)
	}
	if _, st, _, _ := r.lastMessage(conv); st != "unknown" {
		t.Fatalf("status = %q, want unknown", st)
	}
	if r.sendCount() != 1 {
		t.Fatal("settling resent the text")
	}
	if chStatus := r.channelStatus(); chStatus != "ok" {
		t.Errorf("one lost answer changed the channel to %q", chStatus)
	}
}

func (r *twilioRig) sendCount() int { return r.fake.sendCount() }

func (r *twilioRig) channelStatus() string {
	var s string
	if err := r.db.QueryRow(`SELECT status FROM msg_channels WHERE id = $1`, r.tChannel).Scan(&s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

// TestCarrierRefusals fails if a refused text is not failed with the
// carrier's code, if a reported opt-out (21610) does not set Hello's own
// opt-out and block later sends, if refused credentials do not stop the
// channel, if an opt-out between queueing and sending does not stop the text,
// or if repeated failures do not degrade the channel.
func TestCarrierRefusals(t *testing.T) {
	r := newTwilioRig(t)
	r.allowFirstContact(1000, 1000)
	ctx := context.Background()

	// A refusal: failed, with the code, no channel failure.
	conv := int64(r.start(http.StatusCreated, twilioPeer(), "x").json(t)["id"].(float64))
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "reject" })
	r.sendOnce()
	if _, st, _, code := r.lastMessage(conv); st != "failed" || code != "21211" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	if r.channelStatus() != "ok" {
		t.Error("a bad number degraded the channel")
	}

	// A reported opt-out (21610): the customer is opted out, the thread says
	// so, and the next reply is refused before it is queued.
	peer := twilioPeer()
	conv = int64(r.start(http.StatusCreated, peer, "x").json(t)["id"].(float64))
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "optout" })
	r.sendOnce()
	if _, st, _, code := r.lastMessage(conv); st != "failed" || code != "21610" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	var source string
	if err := r.db.QueryRow(`SELECT source FROM sms_opt_outs WHERE address_id = $1 AND state = 'out' AND peer_key = $2`, r.tAddr, peer).Scan(&source); err != nil || source != "provider" {
		t.Fatalf("opt-out source = %q, %v", source, err)
	}
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "ok" })
	r.admin.must(http.StatusUnprocessableEntity, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": "again"})
	before := r.sendCount()

	// Opted out between queueing and sending: the carrier is not called.
	peer = twilioPeer()
	conv = int64(r.start(http.StatusCreated, peer, "queued then opted out").json(t)["id"].(float64))
	r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/optouts", map[string]any{"addressId": r.tAddr, "peer": peer})
	r.sendOnce()
	if _, st, _, code := r.lastMessage(conv); st != "failed" || code != "opted_out" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	if r.sendCount() != before {
		t.Fatal("a text to an opted-out customer reached the carrier")
	}

	// Throughput: a number sends at most sends_per_minute a minute.
	if _, err := r.db.ExecContext(ctx, `UPDATE msg_addresses SET sends_per_minute = 1 WHERE id = $1`, r.tAddr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversation_messages SET claimed_at = now() - interval '1 hour' WHERE claimed_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	a := int64(r.start(http.StatusCreated, twilioPeer(), "one").json(t)["id"].(float64))
	b := int64(r.start(http.StatusCreated, twilioPeer(), "two").json(t)["id"].(float64))
	before = r.sendCount()
	r.sendOnce()
	r.sendOnce()
	if r.sendCount() != before+1 {
		t.Fatalf("a number with 1 send a minute sent %d", r.sendCount()-before)
	}
	_, sa, _, _ := r.lastMessage(a)
	_, sb, _, _ := r.lastMessage(b)
	if sa != "sent" || sb != "queued" {
		t.Fatalf("statuses = %q, %q; want sent, queued", sa, sb)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE msg_addresses SET sends_per_minute = 60 WHERE id = $1`, r.tAddr); err != nil {
		t.Fatal(err)
	}

	// Five failures in a row degrade the channel; an accepted text recovers it.
	if _, err := r.db.ExecContext(ctx, `UPDATE conversation_messages SET status = 'failed' WHERE status = 'queued'`); err != nil {
		t.Fatal(err)
	}
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "lost" })
	for i := 0; i < 5; i++ {
		r.start(http.StatusCreated, twilioPeer(), "will be lost")
		r.sendOnce()
	}
	if r.channelStatus() != "degraded" {
		t.Fatalf("channel status = %q after five lost answers", r.channelStatus())
	}
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "ok" })
	r.start(http.StatusCreated, twilioPeer(), "recovers")
	r.sendOnce()
	if r.channelStatus() != "ok" {
		t.Fatalf("channel status = %q after a good send", r.channelStatus())
	}

	// Refused credentials stop the channel: the text fails, later texts wait
	// and are not sent, and replies are refused.
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "unauth" })
	conv = int64(r.start(http.StatusCreated, twilioPeer(), "login refused").json(t)["id"].(float64))
	r.sendOnce()
	if _, st, _, code := r.lastMessage(conv); st != "failed" || code != "credentials_invalid" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	if r.channelStatus() != "credentials_invalid" {
		t.Fatalf("channel status = %q", r.channelStatus())
	}
	r.admin.must(http.StatusConflict, "POST", idPath("/api/v1/inbox/conversations", conv, "/messages"), map[string]any{"body": "more"})
	post := map[string]any{"addressId": r.tAddr, "to": twilioPeer(), "body": "x", "consentAttestation": true, "consentNote": "n"}
	r.admin.must(http.StatusConflict, "POST", "/api/v1/inbox/conversations", post)
	before = r.sendCount()
	r.sendOnce()
	if r.sendCount() != before {
		t.Fatal("a channel with refused credentials sent a text")
	}
	// New credentials start clean.
	r.fake.set(func(f *fakeTwilio) { f.sendMode = "ok" })
	r.admin.must(http.StatusOK, "PUT", idPath("/api/v1/messaging/channels", r.tChannel, ""), map[string]any{"accountSid": fakeSID, "authToken": fakeToken})
	if r.channelStatus() != "ok" {
		t.Fatalf("channel status = %q after new credentials", r.channelStatus())
	}
}

// TestStatusPolling fails if a sent text is not moved to delivered or failed
// when the carrier reports it, is polled again after it settled, is polled
// after 48 hours, or a failed read with the carrier's opt-out code does not
// opt the customer out.
func TestStatusPolling(t *testing.T) {
	r := newTwilioRig(t)
	r.allowFirstContact(1000, 1000)
	ctx := context.Background()

	conv := int64(r.start(http.StatusCreated, twilioPeer(), "will be delivered").json(t)["id"].(float64))
	r.sendOnce()
	msg, st, _, _ := r.lastMessage(conv)
	if st != "sent" {
		t.Fatalf("status = %q", st)
	}
	// Still in flight: stays sent, and is read again later.
	r.fake.set(func(f *fakeTwilio) { f.msgStatus = "sent" })
	r.pollOnce()
	if _, st, _, _ := r.lastMessage(conv); st != "sent" {
		t.Fatalf("status = %q while in flight", st)
	}
	var next any
	var polls int
	if err := r.db.QueryRow(`SELECT next_poll_at, poll_count FROM conversation_messages WHERE id = $1`, msg).Scan(&next, &polls); err != nil || next == nil || polls != 1 {
		t.Fatalf("next poll = %v, polls = %d, %v", next, polls, err)
	}
	r.fake.set(func(f *fakeTwilio) { f.msgStatus = "delivered" })
	r.pollOnce()
	if _, st, _, _ := r.lastMessage(conv); st != "delivered" {
		t.Fatalf("status = %q, want delivered", st)
	}
	reads := r.fake.readCount()
	r.pollOnce()
	r.pollOnce()
	if r.fake.readCount() != reads {
		t.Fatal("a delivered text was polled again")
	}

	// Undelivered: failed with the carrier's code.
	conv = int64(r.start(http.StatusCreated, twilioPeer(), "will bounce").json(t)["id"].(float64))
	r.sendOnce()
	r.fake.set(func(f *fakeTwilio) { f.msgStatus = "undelivered"; f.msgError = 30003 })
	r.pollOnce()
	if _, st, _, code := r.lastMessage(conv); st != "failed" || code != "30003" {
		t.Fatalf("status = %q, code = %q", st, code)
	}

	// The carrier reports the opt-out when it fails the text.
	peer := twilioPeer()
	r.start(http.StatusCreated, peer, "blocked later")
	r.sendOnce()
	r.fake.set(func(f *fakeTwilio) { f.msgStatus = "failed"; f.msgError = 21610 })
	r.pollOnce()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM sms_opt_outs WHERE address_id = $1 AND peer_key = $2 AND state = 'out' AND source = 'provider'`, r.tAddr, peer).Scan(&n); err != nil || n != 1 {
		t.Fatalf("opt-outs = %d, %v", n, err)
	}

	// Older than 48 hours: not asked about any more.
	conv = int64(r.start(http.StatusCreated, twilioPeer(), "old").json(t)["id"].(float64))
	r.sendOnce()
	old, _, _, _ := r.lastMessage(conv)
	if _, err := r.db.ExecContext(ctx, `UPDATE conversation_messages SET sent_at = now() - interval '49 hours' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	reads = r.fake.readCount()
	r.fake.set(func(f *fakeTwilio) { f.msgStatus = "delivered"; f.msgError = 0 })
	r.pollOnce()
	if r.fake.readCount() != reads {
		t.Fatal("a text older than 48 hours was polled")
	}
	if _, st, _, _ := r.lastMessage(conv); st != "sent" {
		t.Fatalf("status = %q", st)
	}
	var left any
	if err := r.db.QueryRow(`SELECT next_poll_at FROM conversation_messages WHERE id = $1`, old).Scan(&left); err != nil || left != nil {
		t.Fatalf("polling did not stop (%v, %v)", left, err)
	}
}

// TestQueuedTooLong fails if a carrier text stuck queued is not failed after
// an hour without being sent.
func TestQueuedTooLong(t *testing.T) {
	r := newTwilioRig(t)
	r.allowFirstContact(1000, 1000)
	conv := int64(r.start(http.StatusCreated, twilioPeer(), "stuck").json(t)["id"].(float64))
	msg, _, _, _ := r.lastMessage(conv)
	if _, err := r.db.Exec(`UPDATE conversation_messages SET created_at = now() - interval '2 hours' WHERE id = $1`, msg); err != nil {
		t.Fatal(err)
	}
	if _, failed, err := r.st.SettleCarrierTexts(context.Background()); err != nil || failed != 1 {
		t.Fatalf("failed %d, %v", failed, err)
	}
	if _, st, _, code := r.lastMessage(conv); st != "failed" || code != "not_sent" {
		t.Fatalf("status = %q, code = %q", st, code)
	}
	if r.sendCount() != 0 {
		t.Fatal("the stuck text was sent")
	}
}
