// Package bitrix24 is the Bitrix24 connector of internal/crm (spec
// integrations P21): contact, company and lead search by phone, calls written
// with the telephony.externalcall methods (register, finish, attachRecord),
// users by email, and an inbound webhook as the credential.
//
// The webhook address (https://portal/rest/<user>/<code>/) is the secret: it
// is stored sealed as the connector's client secret, never shown again and
// never put in an error message. Facts marked "verify" in docs/crm.md need a
// real Bitrix24 portal.
package bitrix24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

// Kind is the connector kind.
const Kind = "bitrix24"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// WebhookScopes are the permissions the inbound webhook needs.
var WebhookScopes = []string{"crm", "telephony", "user"}

var webhookPath = regexp.MustCompile(`^(.*)/rest/\d+/[A-Za-z0-9]+/?$`)

// Connector is the Bitrix24 connector.
type Connector struct {
	hc *http.Client

	mu   sync.Mutex
	done map[string]*progress // call id -> steps already done
}

// progress remembers what a call's failed attempt already did in Bitrix24,
// so a retry does not register the call a second time (register has no
// idempotency key). It lives in memory for a day.
type progress struct {
	bitrixID string
	finished bool
	at       time.Time
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc, done: map[string]*progress{}} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. The recording is fetched by Bitrix24
// from a short-lived link and kept as its own copy.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, RecordingCopy: true}
}

// ValidateConfig implements crm.ConfigValidator: Bitrix24 has no settings
// of its own; the webhook address is the secret.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		if k != "clientId" {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		out[k] = v
	}
	return out, nil
}

// SecretBoundKeys implements crm.SecretBinder: the webhook address holds
// both the host and the secret, so no other setting steers it.
func (c *Connector) SecretBoundKeys() []string { return nil }

// parseWebhook validates an inbound webhook address and returns it with a
// trailing slash. The error text never repeats the address.
func parseWebhook(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the webhook must be an address like https://your-portal.bitrix24.com/rest/1/xxxxxxxx/")
	}
	if !webhookPath.MatchString(u.Path) {
		return "", errors.New("the webhook address must end in /rest/<user id>/<code>/")
	}
	return strings.TrimRight(u.String(), "/") + "/", nil
}

// portal is the scheme and host of a webhook, for links into the portal.
func portal(hook string) string {
	u, err := url.Parse(hook)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host+strings.TrimSuffix(webhookPath.ReplaceAllString(u.Path, "$1"), "/"), "/")
}

type reply struct {
	Result           json.RawMessage `json:"result"`
	Error            string          `json:"error"`
	ErrorDescription string          `json:"error_description"`
}

// call runs one REST method on the webhook (the connector's "access token")
// and decodes its result.
func (c *Connector) call(ctx context.Context, hook, method string, params map[string]any, out any) error {
	base, err := parseWebhook(hook)
	if err != nil {
		return crm.ErrAuth
	}
	var r reply
	if err := crm.Do(ctx, c.hc, crm.Request{Method: http.MethodPost, URL: base + method + ".json", Body: params, Authed: true}, &r); err != nil {
		return err
	}
	if r.Error != "" {
		switch strings.ToUpper(r.Error) {
		case "INVALID_CREDENTIALS", "EXPIRED_TOKEN", "NO_AUTH_FOUND":
			return crm.ErrAuth
		case "QUERY_LIMIT_EXCEEDED":
			return &crm.APIError{Status: http.StatusServiceUnavailable, Retry: true, After: time.Second, Msg: "query limit exceeded"}
		}
		msg := strings.Join(strings.Fields(r.ErrorDescription), " ")
		if msg == "" {
			msg = strings.Join(strings.Fields(r.Error), " ")
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return &crm.APIError{Status: http.StatusBadRequest, Msg: msg}
	}
	if out != nil && len(r.Result) > 0 {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return &crm.APIError{Msg: "unreadable response"}
		}
	}
	return nil
}

// Authenticate implements crm.StaticAuth: the webhook must answer profile.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	hook, err := parseWebhook(cfg.ClientSecret)
	if err != nil {
		return crm.Tokens{}, err
	}
	var me struct {
		ID any `json:"ID"`
	}
	if err := c.call(ctx, hook, "profile", nil, &me); err != nil {
		if errors.Is(err, crm.ErrAuth) {
			return crm.Tokens{}, errors.New("Bitrix24 refused the webhook")
		}
		return crm.Tokens{}, err
	}
	u, _ := url.Parse(hook)
	return crm.Tokens{Access: "credential", Account: u.Hostname(), Scopes: WebhookScopes}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.call(ctx, cc.AccessToken, "profile", nil, nil)
}

type entity struct {
	Title, Name, LastName, CompanyTitle string
	CompanyID                           any
}

func (e *entity) UnmarshalJSON(b []byte) error {
	var m struct {
		Title        string `json:"TITLE"`
		Name         string `json:"NAME"`
		LastName     string `json:"LAST_NAME"`
		CompanyTitle string `json:"COMPANY_TITLE"`
		CompanyID    any    `json:"COMPANY_ID"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*e = entity{m.Title, m.Name, m.LastName, m.CompanyTitle, m.CompanyID}
	return nil
}

func idString(v any) string {
	switch x := v.(type) {
	case string:
		if x != "0" {
			return x
		}
	case float64:
		if x != 0 {
			return strconv.FormatInt(int64(x), 10)
		}
	}
	return ""
}

// Lookup implements crm.Connector: crm.duplicate.findbycomm finds the
// contact, company or lead that carries the number (contacts first), then the
// entity is read for its name.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	for _, v := range []string{n, strings.TrimPrefix(n, "+")} {
		// No match is an empty list, a match an object by entity type.
		var raw json.RawMessage
		if err := c.call(ctx, cc.AccessToken, "crm.duplicate.findbycomm", map[string]any{"type": "PHONE", "values": []string{v}}, &raw); err != nil {
			return nil, err
		}
		var found map[string][]any
		_ = json.Unmarshal(raw, &found)
		for _, kind := range []string{"CONTACT", "COMPANY", "LEAD"} {
			ids := found[kind]
			if len(ids) == 0 {
				continue
			}
			id := idString(ids[0])
			if id == "" {
				continue
			}
			return c.entity(ctx, cc, kind, id)
		}
	}
	return nil, nil
}

func (c *Connector) entity(ctx context.Context, cc crm.Conn, kind, id string) (*crm.Contact, error) {
	var e entity
	if err := c.call(ctx, cc.AccessToken, "crm."+strings.ToLower(kind)+".get", map[string]any{"id": id}, &e); err != nil {
		return nil, err
	}
	ct := &crm.Contact{ID: kind + ":" + id, Company: e.CompanyTitle}
	switch kind {
	case "COMPANY":
		ct.Name, ct.Company = e.Title, e.Title
	default:
		ct.Name = strings.TrimSpace(e.Name + " " + e.LastName)
		if ct.Name == "" {
			ct.Name = e.Title
		}
	}
	if cid := idString(e.CompanyID); kind == "CONTACT" && cid != "" {
		ct.CompanyID = cid
		var co entity
		if err := c.call(ctx, cc.AccessToken, "crm.company.get", map[string]any{"id": cid}, &co); err == nil {
			ct.Company = co.Title
		}
	}
	if p := portal(cc.AccessToken); p != "" {
		ct.URL = p + "/crm/" + strings.ToLower(kind) + "/details/" + id + "/"
	}
	return ct, nil
}

// ResolveUser implements crm.Connector: user.get filtered by email, compared
// exactly (case-insensitively).
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var us []struct {
		ID    any    `json:"ID"`
		Email string `json:"EMAIL"`
	}
	if err := c.call(ctx, cc.AccessToken, "user.get", map[string]any{"FILTER": map[string]any{"EMAIL": email}}, &us); err != nil {
		return "", err
	}
	for _, u := range us {
		if strings.EqualFold(u.Email, email) {
			return idString(u.ID), nil
		}
	}
	return "", nil
}

// statusCode is the SIP-style code finish takes: 200 answered, 304 missed,
// 486 busy, 503 failed.
func statusCode(o crm.Outcome) string {
	switch o {
	case crm.OutcomeAnswered:
		return "200"
	case crm.OutcomeBusy:
		return "486"
	case crm.OutcomeFailed:
		return "503"
	}
	return "304"
}

func (c *Connector) progress(key string) *progress {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, p := range c.done {
		if time.Since(p.at) > 24*time.Hour {
			delete(c.done, k)
		}
	}
	p, ok := c.done[key]
	if !ok {
		p = &progress{at: time.Now()}
		c.done[key] = p
	}
	return p
}

func (c *Connector) forget(key string) {
	c.mu.Lock()
	delete(c.done, key)
	c.mu.Unlock()
}

// LogCall implements crm.Connector: register the call (bound to the CRM
// entity found by phone), finish it with its duration and result, and attach
// the recording when there is one. A retry resumes where a failed attempt
// stopped.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	key := cc.AccessToken + "|" + l.CallID
	p := c.progress(key)
	typ := 2
	if l.Direction == "outbound" {
		typ = 1
	}
	if p.bitrixID == "" {
		params := map[string]any{
			"USER_ID": l.OwnerID, "PHONE_NUMBER": l.Number, "CALL_START_DATE": crm.CallTime(l).Format(time.RFC3339),
			"CRM_CREATE": 0, "CRM_SOURCE": "CALL", "TYPE": typ, "SHOW": 0,
		}
		if l.Extension != "" {
			params["USER_PHONE_INNER"] = l.Extension
		}
		if l.Contact != nil {
			if kind, id, ok := strings.Cut(l.Contact.ID, ":"); ok {
				params["CRM_ENTITY_TYPE"], params["CRM_ENTITY_ID"] = kind, id
			}
		}
		var res struct {
			CallID string `json:"CALL_ID"`
		}
		if err := c.call(ctx, cc.AccessToken, "telephony.externalcall.register", params, &res); err != nil {
			return "", err
		}
		if res.CallID == "" {
			return "", &crm.APIError{Msg: "the response has no call id"}
		}
		p.bitrixID = res.CallID
	}
	if !p.finished {
		err := c.call(ctx, cc.AccessToken, "telephony.externalcall.finish", map[string]any{
			"CALL_ID": p.bitrixID, "USER_ID": l.OwnerID, "DURATION": int64(l.Duration.Seconds()),
			"COST": 0, "STATUS_CODE": statusCode(l.Outcome), "ADD_TO_CHAT": 0,
		}, nil)
		if err != nil {
			return "", err
		}
		p.finished = true
	}
	if l.RecordingURL != "" {
		if err := c.attach(ctx, cc, p.bitrixID, l.RecordingURL); err != nil {
			return "", err
		}
	}
	id := p.bitrixID
	c.forget(key)
	return id, nil
}

func (c *Connector) attach(ctx context.Context, cc crm.Conn, callID, link string) error {
	name := "recording.wav"
	if u, err := url.Parse(link); err == nil && path.Ext(u.Path) != "" {
		name = path.Base(u.Path)
	}
	return c.call(ctx, cc.AccessToken, "telephony.externalCall.attachRecord", map[string]any{
		"CALL_ID": callID, "FILENAME": name, "RECORD_URL": link,
	}, nil)
}

// UpdateCallLog implements crm.Connector: it attaches the recording that
// arrived after the call was written. Bitrix24 copies the file, so this runs
// once per call.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.RecordingURL == "" {
		return nil
	}
	return c.attach(ctx, cc, ref, l.RecordingURL)
}
