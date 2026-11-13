// Package clio is the Clio Manage connector of internal/crm (spec
// integrations I-47, P20): the caller is a Clio contact found by phone, and
// every call becomes a phone communication between that contact and the
// Clio user who took or placed it, with the recording link in its body.
//
// Authentication is OAuth 2 with the firm's own Clio developer application
// (the administrator creates it in Clio's developer portal and gives Hello
// its key and secret). Calls from a number that is no contact are skipped:
// Clio's communications need a contact on one end. Facts marked "verify" in
// docs/crm.md need a real Clio account.
package clio

import (
	"context"
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
const Kind = "clio"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// regionHost maps a Clio region to its host.
var regionHost = map[string]string{
	"us": "app.clio.com", "eu": "eu.app.clio.com", "ca": "ca.app.clio.com", "au": "au.app.clio.com",
}

// Connector is the Clio connector.
type Connector struct {
	hc *http.Client
	// Base overrides https://<region host> for tests.
	Base string
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true}
}

// SecretBoundKeys implements crm.SecretBound: the region picks the host the
// secret goes to.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "region"} }

// ValidateConfig implements crm.ConfigValidator.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "region":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	if out["region"] == "" {
		out["region"] = "us"
	}
	if _, ok := regionHost[out["region"]]; !ok {
		return nil, errors.New("region must be us, eu, ca or au")
	}
	return out, nil
}

func (c *Connector) base(cfg crm.Config) string {
	if c.Base != "" {
		return c.Base
	}
	h, ok := regionHost[cfg.Extra["region"]]
	if !ok {
		h = regionHost["us"]
	}
	return "https://" + h
}

// AuthURL implements crm.OAuth.
func (c *Connector) AuthURL(cfg crm.Config, redirectURI, state string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "redirect_uri": {redirectURI}, "state": {state}}
	return c.base(cfg) + "/oauth/authorize?" + q.Encode()
}

func (c *Connector) token(ctx context.Context, cfg crm.Config, form url.Values) (crm.Tokens, error) {
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	req := kit.Request{Method: http.MethodPost, URL: c.base(cfg) + "/oauth/token", Body: form}
	if err := kit.Do(ctx, c.hc, req, &tr); err != nil {
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	t := crm.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// Exchange implements crm.OAuth and names the account after the Clio user
// who approved it.
func (c *Connector) Exchange(ctx context.Context, cfg crm.Config, redirectURI, code string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"authorization_code"}, "client_id": {cfg.ClientID},
		"client_secret": {cfg.ClientSecret}, "redirect_uri": {redirectURI}, "code": {code}})
	if err != nil {
		return t, err
	}
	var who struct {
		Data struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"data"`
	}
	cc := crm.Conn{Config: cfg, AccessToken: t.Access}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v4/users/who_am_i.json?fields=name,email", nil, &who); err != nil {
		return t, err
	}
	t.Account = who.Data.Name
	if t.Account == "" {
		t.Account = who.Data.Email
	}
	return t, nil
}

// Refresh implements crm.OAuth. A refresh token Clio rejects (400 or 401) is
// a permanent failure; Clio keeps the refresh token, so none comes back.
func (c *Connector) Refresh(ctx context.Context, cfg crm.Config, refresh string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "client_id": {cfg.ClientID},
		"client_secret": {cfg.ClientSecret}, "refresh_token": {refresh}})
	var ae *crm.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
		ae.Retry = false
	}
	return t, err
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/api/v4/users/who_am_i.json?fields=id", nil, nil)
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	return kit.Do(ctx, c.hc, kit.Request{
		Method: method, URL: c.base(cc.Config) + path, Body: body, Authed: true,
		Header: map[string]string{"Authorization": "Bearer " + cc.AccessToken},
	}, out)
}

// contactID is "Person:123" or "Company:45": a communication names the kind
// of contact on each end.
func contactID(typ string, id int64) string { return typ + ":" + strconv.FormatInt(id, 10) }

func parseContactID(s string) (typ string, id int64, ok bool) {
	t, n, found := strings.Cut(s, ":")
	v, err := strconv.ParseInt(n, 10, 64)
	return t, v, found && err == nil && (t == "Person" || t == "Company")
}

// Lookup implements crm.Connector: contacts matching the last four digits
// of the number, kept when one of their phone numbers is the number (verify
// that Clio's query matches phone numbers this way).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	var res struct {
		Data []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Company struct {
				Name string `json:"name"`
			} `json:"company"`
			Phones []struct {
				Number string `json:"number"`
			} `json:"phone_numbers"`
		} `json:"data"`
	}
	q := url.Values{"query": {kit.Last4(n)}, "limit": {"50"}, "fields": {"id,name,type,company{name},phone_numbers{number}"}}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v4/contacts.json?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	for _, d := range res.Data {
		if d.ID == 0 || (d.Type != "Person" && d.Type != "Company") {
			continue
		}
		for _, p := range d.Phones {
			if kit.Same(p.Number, n) {
				return &crm.Contact{ID: contactID(d.Type, d.ID), Name: d.Name, Company: d.Company.Name,
					URL: c.base(cc.Config) + "/nc/#/contacts/" + strconv.FormatInt(d.ID, 10)}, nil
			}
		}
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the Clio user whose email is the
// Hello user's.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var res struct {
		Data []struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
		} `json:"data"`
	}
	q := url.Values{"query": {email}, "limit": {"20"}, "fields": {"id,email"}}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v4/users.json?"+q.Encode(), nil, &res); err != nil {
		return "", err
	}
	for _, u := range res.Data {
		if strings.EqualFold(u.Email, email) {
			return strconv.FormatInt(u.ID, 10), nil
		}
	}
	return "", nil
}

type party struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// LogCall implements crm.Connector: a PhoneCommunication from the caller to
// the user (inbound) or the reverse (outbound).
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	if l.Contact == nil {
		return "", fmt.Errorf("%w: no Clio contact has this number", crm.ErrNoTarget)
	}
	typ, cid, ok := parseContactID(l.Contact.ID)
	if !ok {
		return "", &crm.APIError{Msg: "the contact id is not a Clio contact"}
	}
	uid, err := strconv.ParseInt(l.OwnerID, 10, 64)
	if err != nil || uid < 1 {
		return "", fmt.Errorf("%w: the call has no Clio user", crm.ErrNoTarget)
	}
	contact, user := party{cid, typ}, party{uid, "User"}
	from, to := contact, user
	if l.Direction == "outbound" {
		from, to = user, contact
	}
	body := map[string]any{"data": map[string]any{
		"type": "PhoneCommunication", "subject": kit.Clip(kit.Title(l), 200), "body": kit.Text(l),
		"date": kit.Timestamp(l).Format("2006-01-02"), "senders": []party{from}, "receivers": []party{to},
	}}
	var res struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, cc, http.MethodPost, "/api/v4/communications.json?fields=id", body, &res); err != nil {
		return "", err
	}
	if res.Data.ID == 0 {
		return "", &crm.APIError{Msg: "the response has no communication id"}
	}
	return strconv.FormatInt(res.Data.ID, 10), nil
}

// UpdateCallLog implements crm.Connector: the body is rewritten whole, which
// carries the renewed recording link and is idempotent.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	body := map[string]any{"data": map[string]any{"body": kit.Text(l)}}
	return c.do(ctx, cc, http.MethodPatch, "/api/v4/communications/"+url.PathEscape(ref)+".json", body, nil)
}
