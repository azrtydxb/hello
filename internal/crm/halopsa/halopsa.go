// Package halopsa is the HaloPSA connector of internal/crm (spec
// integrations I-47, P20): the caller is a Halo user found by phone, and
// every call becomes a ticket for that user's client, with the recording link
// added as a hidden action.
//
// Authentication is an API application with the client-credentials grant
// (Halo, Configuration, Integrations, HaloPSA API): the administrator gives
// the client id and secret, Hello requests an access token and renews it
// before it expires. Facts marked "verify" in docs/crm.md need a real Halo
// tenant.
package halopsa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit"
)

// Kind is the connector kind.
const Kind = "halopsa"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Connector is the HaloPSA connector.
type Connector struct {
	hc *http.Client
	// Base overrides https://<host> for tests.
	Base string
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, Ticket: true}
}

// SecretBoundKeys implements crm.SecretBound.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "host", "tenant"} }

// ValidateConfig implements crm.ConfigValidator. host is the address of the
// Halo instance (acme.halopsa.com), tenant its tenant name for a hosted
// instance, ticketTypeId the Halo ticket type calls get (Halo's default when
// empty) and fallbackClientId the Halo client that owns calls from numbers
// Halo does not know (those calls are skipped without it).
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "host", "tenant", "ticketTypeId", "fallbackClientId":
			if v != "" {
				out[k] = v
			}
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	out["host"] = strings.ToLower(out["host"])
	if !kit.Host(out["host"]) {
		return nil, errors.New("host is the address of your Halo instance without https://, for example acme.halopsa.com")
	}
	if out["tenant"] != "" && !kit.Label(strings.ToLower(out["tenant"])) {
		return nil, errors.New("tenant must be a plain name")
	}
	for _, k := range []string{"ticketTypeId", "fallbackClientId"} {
		if v := out[k]; v != "" {
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				return nil, fmt.Errorf("%s must be a number", k)
			}
		}
	}
	if len(out["clientId"]) > 200 {
		return nil, errors.New("clientId is too long")
	}
	return out, nil
}

func (c *Connector) base(cfg crm.Config) string {
	if c.Base != "" {
		return c.Base
	}
	return "https://" + cfg.Extra["host"]
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	return kit.Do(ctx, c.hc, kit.Request{
		Method: method, URL: c.base(cc.Config) + "/api" + path, Body: body, Authed: true,
		Header: map[string]string{"Authorization": "Bearer " + cc.AccessToken},
	}, out)
}

// Authenticate implements crm.Direct: the client-credentials grant. The
// answer carries its expiry, so the framework asks again when it runs out.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	u := c.base(cfg) + "/auth/token"
	if t := cfg.Extra["tenant"]; t != "" {
		u += "?" + url.Values{"tenant": {t}}.Encode()
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "scope": {"all"}}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err := kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost, URL: u, Body: form}, &tr)
	var ae *crm.APIError
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && !ae.Retry {
		return crm.Tokens{}, &crm.APIError{Status: http.StatusUnauthorized, Msg: "Halo refused the client id and secret"}
	}
	if err != nil {
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	t := crm.Tokens{Access: tr.AccessToken, Account: cfg.Extra["host"]}
	if tr.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/users?count=1", nil, nil)
}

// list decodes Halo's two list shapes: a bare array or an object holding the
// array under key.
func list(raw json.RawMessage, key string, out any) error {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) > 0 && raw[0] == '[' {
		return json.Unmarshal(raw, out)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if v, ok := m[key]; ok {
		return json.Unmarshal(v, out)
	}
	return nil
}

type haloUser struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ClientID    int64  `json:"client_id"`
	ClientName  string `json:"client_name"`
	Phone       string `json:"phonenumber"`
	Mobile      string `json:"mobilenumber"`
	Mobile2     string `json:"mobilenumber2"`
	Home        string `json:"homenumber"`
	SiteID      int64  `json:"site_id"`
	InactiveKey bool   `json:"inactive"`
}

// contactID encodes the user and the client a ticket needs together.
func contactID(u haloUser) string {
	return strconv.FormatInt(u.ID, 10) + ":" + strconv.FormatInt(u.ClientID, 10)
}

func parseContactID(id string) (user, client int64, ok bool) {
	a, b, found := strings.Cut(id, ":")
	if !found {
		return 0, 0, false
	}
	u, err1 := strconv.ParseInt(a, 10, 64)
	cl, err2 := strconv.ParseInt(b, 10, 64)
	return u, cl, err1 == nil && err2 == nil && u > 0
}

// Lookup implements crm.Connector: a user search on the last four digits,
// kept only when one of the user's numbers is the number (the search is
// loose; verify the search term against a tenant).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	var raw json.RawMessage
	q := url.Values{"search": {kit.Last4(n)}, "count": {"50"}, "includeinactive": {"false"}}
	if err := c.do(ctx, cc, http.MethodGet, "/users?"+q.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	var users []haloUser
	if err := list(raw, "users", &users); err != nil {
		return nil, &crm.APIError{Msg: "unreadable response"}
	}
	for _, u := range users {
		if u.ID == 0 || u.InactiveKey || !kit.AnySame(n, u.Phone, u.Mobile, u.Mobile2, u.Home) {
			continue
		}
		return &crm.Contact{ID: contactID(u), Name: u.Name, Company: u.ClientName,
			URL: c.base(cc.Config) + "/tickets?view=users&id=" + strconv.FormatInt(u.ID, 10)}, nil
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the agent whose email is the Hello
// user's.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var raw json.RawMessage
	q := url.Values{"search": {email}, "count": {"20"}}
	if err := c.do(ctx, cc, http.MethodGet, "/agent?"+q.Encode(), nil, &raw); err != nil {
		return "", err
	}
	var agents []struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	if err := list(raw, "agents", &agents); err != nil {
		return "", &crm.APIError{Msg: "unreadable response"}
	}
	for _, a := range agents {
		if strings.EqualFold(a.Email, email) {
			return strconv.FormatInt(a.ID, 10), nil
		}
	}
	return "", nil
}

// LogCall implements crm.Connector. Halo needs a client for a ticket: the
// user's, or the fallback client for a caller Halo does not know.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	t := map[string]any{
		"summary":       kit.Clip(kit.Title(l), 250),
		"details":       kit.HTML(l),
		"dont_do_rules": true,
	}
	if l.Contact != nil {
		u, cl, ok := parseContactID(l.Contact.ID)
		if !ok {
			return "", &crm.APIError{Msg: "the contact id is not a Halo user"}
		}
		t["user_id"] = u
		if cl > 0 {
			t["client_id"] = cl
		}
	}
	if _, ok := t["client_id"]; !ok {
		fb, err := strconv.ParseInt(cc.Config.Extra["fallbackClientId"], 10, 64)
		if err != nil || fb < 1 {
			return "", fmt.Errorf("%w: Halo has no user with this number and no fallback client is set", crm.ErrNoTarget)
		}
		t["client_id"] = fb
	}
	if v, err := strconv.Atoi(cc.Config.Extra["ticketTypeId"]); err == nil && v > 0 {
		t["tickettype_id"] = v
	}
	if id, err := strconv.ParseInt(l.OwnerID, 10, 64); err == nil && id != 0 {
		t["agent_id"] = id
	}
	var raw json.RawMessage
	if err := c.do(ctx, cc, http.MethodPost, "/tickets", []any{t}, &raw); err != nil {
		return "", err
	}
	var one struct {
		ID int64 `json:"id"`
	}
	if s := strings.TrimSpace(string(raw)); strings.HasPrefix(s, "[") {
		var many []struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
			one.ID = many[0].ID
		}
	} else {
		_ = json.Unmarshal(raw, &one)
	}
	if one.ID == 0 {
		return "", &crm.APIError{Msg: "the response has no ticket id"}
	}
	return strconv.FormatInt(one.ID, 10), nil
}

// UpdateCallLog implements crm.Connector: a hidden action with the recording
// link, unless the ticket already has it.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.RecordingURL == "" {
		return nil
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return &crm.APIError{Msg: "the stored ticket id is not a number"}
	}
	var raw json.RawMessage
	q := url.Values{"ticket_id": {ref}, "count": {"200"}}
	if err := c.do(ctx, cc, http.MethodGet, "/actions?"+q.Encode(), nil, &raw); err != nil {
		return err
	}
	var actions []struct {
		Note     string `json:"note"`
		NoteHTML string `json:"note_html"`
	}
	if err := list(raw, "actions", &actions); err != nil {
		return &crm.APIError{Msg: "unreadable response"}
	}
	for _, a := range actions {
		if strings.Contains(a.Note, l.RecordingURL) || strings.Contains(a.NoteHTML, l.RecordingURL) {
			return nil
		}
	}
	e := kit.Escape(l.RecordingURL)
	act := map[string]any{"ticket_id": id, "note_html": `Recording: <a href="` + e + `">` + e + `</a>`, "hiddenfromuser": true}
	return c.do(ctx, cc, http.MethodPost, "/actions", []any{act}, nil)
}
