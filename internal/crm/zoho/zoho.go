// Package zoho is the Zoho CRM connector of internal/crm (spec integrations
// P15): contact and lead search by phone, calls written to the Calls module
// with the recording link in the description, users by email, and the OAuth
// flow of a customer-owned (server-based) API console client.
//
// Zoho's native PhoneBridge click-to-call is only reachable through the Zoho
// Telephony Marketplace, which Hello does not use (decision 1); dialling from
// Zoho goes through the browser extension. Facts marked "verify" in
// docs/crm.md need a real Zoho account.
package zoho

import (
	"context"
	"errors"
	"fmt"
	"github.com/azrtydxb/hello/internal/crm/kit"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

// Kind is the connector kind.
const Kind = "zoho"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Scopes Hello asks the customer's client for.
var Scopes = []string{
	"ZohoCRM.modules.calls.CREATE", "ZohoCRM.modules.calls.UPDATE",
	"ZohoCRM.modules.contacts.READ", "ZohoCRM.modules.leads.READ", "ZohoCRM.users.READ",
}

// tlds are Zoho's data centres: the suffix of accounts.zoho.<tld>,
// www.zohoapis.<tld> and crm.zoho.<tld>.
var tlds = map[string]string{
	"us": "com", "eu": "eu", "in": "in", "au": "com.au", "jp": "jp", "ca": "ca", "sa": "sa", "cn": "com.cn",
}

// Regions lists the data centre codes.
func Regions() []string { return []string{"us", "eu", "in", "au", "jp", "ca", "sa", "cn"} }

// Connector is the Zoho CRM connector.
type Connector struct {
	hc *http.Client
	// Accounts and API give the host of the sign-in/token endpoints and of
	// the REST API per region. Tests point them at an httptest server.
	Accounts, API func(region string) string
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector {
	return &Connector{hc: hc,
		Accounts: func(r string) string { return "https://accounts.zoho." + tlds[r] },
		API:      func(r string) string { return "https://www.zohoapis." + tlds[r] },
	}
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true}
}

// ValidateConfig implements crm.ConfigValidator: the data centre decides
// every host the secret is sent to.
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
	if _, ok := tlds[out["region"]]; !ok {
		return nil, errors.New("region must be one of " + strings.Join(Regions(), ", "))
	}
	return out, nil
}

// SecretBoundKeys implements crm.SecretBound: the region picks the host
// that receives the client secret.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "region"} }

func region(cfg map[string]string) string {
	if _, ok := tlds[cfg["region"]]; ok {
		return cfg["region"]
	}
	return "us"
}

// AuthURL implements crm.OAuth. access_type=offline yields the refresh
// token, prompt=consent makes Zoho issue a new one on every connect.
func (c *Connector) AuthURL(cfg crm.Config, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(Scopes, ","))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	return c.Accounts(region(cfg.Extra)) + "/oauth/v2/auth?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

// token posts to the token endpoint. Zoho answers a refused code or refresh
// token with status 200 and an error member; that is a permanent failure,
// except its rate-limit answer ("Access Denied"), which is retried.
func (c *Connector) token(ctx context.Context, cfg crm.Config, form url.Values) (crm.Tokens, error) {
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	var tr tokenResponse
	if err := kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost, URL: c.Accounts(region(cfg.Extra)) + "/oauth/v2/token", Body: form}, &tr); err != nil {
		return crm.Tokens{}, err
	}
	if tr.Error != "" {
		msg := strings.Join(strings.Fields(tr.Error), " ")
		if len(msg) > 100 {
			msg = msg[:100]
		}
		return crm.Tokens{}, &crm.APIError{Status: http.StatusBadRequest, Retry: strings.EqualFold(tr.Error, "access denied"), Msg: msg}
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	return crm.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second), Scopes: Scopes}, nil
}

// Exchange implements crm.OAuth: the account shown is the email of the user
// who approved the client.
func (c *Connector) Exchange(ctx context.Context, cfg crm.Config, redirectURI, code string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI}, "code": {code}})
	if err != nil {
		return t, err
	}
	if t.Refresh == "" {
		return t, &crm.APIError{Msg: "Zoho returned no refresh token; connect again"}
	}
	var me struct {
		Users []struct {
			Email string `json:"email"`
		} `json:"users"`
	}
	cc := crm.Conn{Config: cfg, AccessToken: t.Access}
	if err := c.api(ctx, cc, http.MethodGet, "/crm/v6/users?type=CurrentUser", nil, &me); err != nil {
		return t, err
	}
	if len(me.Users) > 0 {
		t.Account = me.Users[0].Email
	}
	return t, nil
}

// Refresh implements crm.OAuth. Zoho refresh tokens do not expire.
func (c *Connector) Refresh(ctx context.Context, cfg crm.Config, refresh string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err == nil {
		t.Refresh = "" // unchanged; the framework keeps the stored one
	}
	return t, err
}

// RevokeToken implements crm.Revoker.
func (c *Connector) RevokeToken(ctx context.Context, cfg crm.Config, refresh string) error {
	return kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost,
		URL: c.Accounts(region(cfg.Extra)) + "/oauth/v2/token/revoke?token=" + url.QueryEscape(refresh)}, nil)
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.api(ctx, cc, http.MethodGet, "/crm/v6/users?type=CurrentUser", nil, nil)
}

func (c *Connector) api(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	return kit.Do(ctx, c.hc, kit.Request{Method: method, URL: c.API(region(cc.Config.Extra)) + path, Body: body, Authed: true,
		Header: map[string]string{"Authorization": "Zoho-oauthtoken " + cc.AccessToken}}, out)
}

type record struct {
	ID          string `json:"id"`
	FirstName   string `json:"First_Name"`
	LastName    string `json:"Last_Name"`
	FullName    string `json:"Full_Name"`
	Company     string `json:"Company"`
	AccountName *struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"Account_Name"`
	Phone  string `json:"Phone"`
	Mobile string `json:"Mobile"`
}

// matches is false only when the record shows a phone that is another
// number; a record with no phone fields in the answer is accepted.
func (r record) matches(n string) bool {
	if r.Phone == "" && r.Mobile == "" {
		return true
	}
	return (r.Phone != "" && kit.Same(r.Phone, n)) || (r.Mobile != "" && kit.Same(r.Mobile, n))
}

// Lookup implements crm.Connector: contacts, then leads, whose phone
// matches. The search API compares exactly, so the number is tried as
// E.164 and as bare digits; each hit is checked against its own phone
// fields (verify against a real account how Zoho matches formats).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	terms := []string{n, strings.TrimPrefix(n, "+")}
	for _, module := range []string{"Contacts", "Leads"} {
		for _, term := range terms {
			var res struct {
				Data []record `json:"data"`
			}
			q := url.Values{"phone": {term}, "per_page": {"5"}}
			// 204 No Content (no match) has no body to decode.
			if err := c.api(ctx, cc, http.MethodGet, "/crm/v6/"+module+"/search?"+q.Encode(), nil, &res); err != nil {
				return nil, err
			}
			for _, r := range res.Data {
				if r.ID == "" || !r.matches(n) {
					continue
				}
				name := strings.TrimSpace(r.FirstName + " " + r.LastName)
				if name == "" {
					name = r.FullName
				}
				ct := &crm.Contact{ID: module + ":" + r.ID, Name: name, Company: r.Company,
					URL: "https://crm.zoho." + tlds[region(cc.Config.Extra)] + "/crm/tab/" + module + "/" + url.PathEscape(r.ID)}
				if r.AccountName != nil {
					ct.Company, ct.CompanyID = r.AccountName.Name, r.AccountName.ID
				}
				return ct, nil
			}
		}
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the user whose email equals the
// Hello user's (exact, case-insensitive).
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var res struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	if err := c.api(ctx, cc, http.MethodGet, "/crm/v6/users/search?email="+url.QueryEscape(email), nil, &res); err != nil {
		return "", err
	}
	for _, u := range res.Users {
		if strings.EqualFold(u.Email, email) {
			return u.ID, nil
		}
	}
	return "", nil
}

func callType(l crm.CallLog) string {
	switch {
	case l.Missed:
		return "Missed"
	case l.Direction == "outbound":
		return "Outbound"
	}
	return "Inbound"
}

// minSec is Zoho's Call_Duration text, mm:ss (minutes may pass 59).
func minSec(d time.Duration) string {
	s := int64(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func description(l crm.CallLog) string {
	s := "Logged by Hello."
	if l.Missed {
		s = "Nobody answered. " + s
	}
	if l.RecordingURL != "" {
		s += "\nRecording: " + l.RecordingURL
	}
	return s
}

type writeResult struct {
	Data []struct {
		Code    string `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Details struct {
			ID string `json:"id"`
		} `json:"details"`
	} `json:"data"`
}

func (w writeResult) check() (string, error) {
	if len(w.Data) == 0 {
		return "", &crm.APIError{Msg: "the response has no result"}
	}
	d := w.Data[0]
	if !strings.EqualFold(d.Status, "success") && d.Code != "SUCCESS" {
		msg := strings.Join(strings.Fields(d.Message), " ")
		if msg == "" {
			msg = d.Code
		}
		return "", &crm.APIError{Status: http.StatusBadRequest, Msg: msg}
	}
	return d.Details.ID, nil
}

// LogCall implements crm.Connector: a record in the Calls module. The
// contact or lead is the call's Who_Id.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	rec := map[string]any{
		"Subject":         kit.Title(l),
		"Call_Type":       callType(l),
		"Call_Start_Time": kit.Timestamp(l).Format("2006-01-02T15:04:05-07:00"),
		"Call_Duration":   minSec(l.Duration),
		"Description":     description(l),
	}
	if l.OwnerID != "" {
		rec["Owner"] = map[string]string{"id": l.OwnerID}
	}
	if l.Contact != nil {
		if _, id, ok := strings.Cut(l.Contact.ID, ":"); ok && id != "" {
			rec["Who_Id"] = map[string]string{"id": id}
		}
	}
	var res writeResult
	if err := c.api(ctx, cc, http.MethodPost, "/crm/v6/Calls", map[string]any{"data": []any{rec}}, &res); err != nil {
		return "", err
	}
	id, err := res.check()
	if err == nil && id == "" {
		err = &crm.APIError{Msg: "the response has no call id"}
	}
	return id, err
}

// UpdateCallLog implements crm.Connector: it rewrites the description (the
// recording link, renewed before it expires). A call with no link has
// nothing to patch.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.RecordingURL == "" {
		return nil
	}
	var res writeResult
	body := map[string]any{"data": []any{map[string]string{"Description": description(l)}}}
	if err := c.api(ctx, cc, http.MethodPut, "/crm/v6/Calls/"+url.PathEscape(ref), body, &res); err != nil {
		return err
	}
	_, err := res.check()
	return err
}
