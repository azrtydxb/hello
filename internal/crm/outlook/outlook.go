// Package outlook is the Microsoft 365 / Outlook connector of internal/crm
// (spec integrations P18): each user signs in with their own account
// (delegated Microsoft Graph permissions, no admin consent needed for
// Contacts.Read and Calendars.Read), their contacts name callers, and their
// calendar says when they are busy (I-56).
//
// The request shapes follow the Microsoft Graph v1.0 reference; the facts
// that need a real tenant are listed in docs/contacts-calendar.md.
package outlook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

// Kind is the connector kind.
const Kind = "outlook"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Connector is the Outlook connector.
type Connector struct {
	j crm.JSONClient
	// LoginBase and GraphBase are replaced by tests.
	LoginBase, GraphBase string
	index                crm.IndexCache
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector {
	return &Connector{j: crm.JSONClient{HC: hc}, LoginBase: "https://login.microsoftonline.com", GraphBase: "https://graph.microsoft.com/v1.0"}
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, UserScope: true, Calendar: true}
}

var tenantRe = regexp.MustCompile(`^(common|organizations|consumers|[0-9a-fA-F-]{36}|[A-Za-z0-9][A-Za-z0-9.-]{0,251}\.[A-Za-z]{2,})$`)

// ValidateConfig implements crm.ConfigValidator. tenant is common (any work
// or school account, the default), organizations, a tenant id or a domain;
// features is contacts, calendar or both.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "tenant", "features":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	if out["tenant"] == "" {
		out["tenant"] = "organizations"
	}
	if !tenantRe.MatchString(out["tenant"]) {
		return nil, errors.New("tenant must be organizations, common, a tenant id or a domain name")
	}
	f := crm.Features(out)
	for _, x := range f {
		if x != crm.FeatureContacts && x != crm.FeatureCalendar {
			return nil, errors.New("features must be contacts, calendar or both")
		}
	}
	out["features"] = strings.Join(f, ",")
	return out, nil
}

func tenant(cfg crm.Config) string {
	if t := cfg.Extra["tenant"]; t != "" {
		return t
	}
	return "organizations"
}

// Scopes asks only what the chosen features need.
func Scopes(cfg crm.Config) []string {
	s := []string{"offline_access", "User.Read"}
	for _, f := range crm.Features(cfg.Extra) {
		switch f {
		case crm.FeatureContacts:
			s = append(s, "Contacts.Read")
		case crm.FeatureCalendar:
			s = append(s, "Calendars.Read")
		}
	}
	return s
}

// AuthURL implements crm.OAuth.
func (c *Connector) AuthURL(cfg crm.Config, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("response_mode", "query")
	q.Set("scope", strings.Join(Scopes(cfg), " "))
	q.Set("state", state)
	q.Set("prompt", "select_account")
	return c.LoginBase + "/" + url.PathEscape(tenant(cfg)) + "/oauth2/v2.0/authorize?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (c *Connector) token(ctx context.Context, cfg crm.Config, form url.Values) (crm.Tokens, error) {
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("scope", strings.Join(Scopes(cfg), " "))
	var tr tokenResponse
	if err := c.j.Do(ctx, http.MethodPost, c.LoginBase+"/"+url.PathEscape(tenant(cfg))+"/oauth2/v2.0/token", "", nil, form, &tr); err != nil {
		// A 400 from the token endpoint (invalid_grant: the refresh token
		// was revoked or expired) is final, never retried.
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	return crm.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		Scopes: strings.Fields(tr.Scope)}, nil
}

// Exchange implements crm.OAuth: it trades the code for tokens and reads the
// account's address.
func (c *Connector) Exchange(ctx context.Context, cfg crm.Config, redirectURI, code string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI}, "code": {code}})
	if err != nil {
		return t, err
	}
	var me struct {
		Mail string `json:"mail"`
		UPN  string `json:"userPrincipalName"`
		Name string `json:"displayName"`
		ID   string `json:"id"`
	}
	if err := c.j.Do(ctx, http.MethodGet, c.GraphBase+"/me?$select=id,mail,userPrincipalName,displayName", t.Access, nil, nil, &me); err != nil {
		return t, err
	}
	t.Account = me.Mail
	if t.Account == "" {
		t.Account = me.UPN
	}
	return t, nil
}

// Refresh implements crm.OAuth.
func (c *Connector) Refresh(ctx context.Context, cfg crm.Config, refresh string) (crm.Tokens, error) {
	return c.token(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.j.Do(ctx, http.MethodGet, c.GraphBase+"/me?$select=id", cc.AccessToken, nil, nil, nil)
}

type contact struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"displayName"`
	CompanyName   string   `json:"companyName"`
	MobilePhone   string   `json:"mobilePhone"`
	BusinessPhone []string `json:"businessPhones"`
	HomePhones    []string `json:"homePhones"`
}

type contactPage struct {
	Value    []contact `json:"value"`
	NextLink string    `json:"@odata.nextLink"`
}

// maxPages bounds one read of an address book (999 contacts a page).
const maxPages = 20

// load reads the user's contacts into an index. It outlives the caller's
// context (a lookup is bounded to 1.5 s, an address book takes longer), so
// the first call after a quiet spell misses and the next ones hit.
func (c *Connector) load(ctx context.Context, token string) (*crm.ContactIndex, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	idx := crm.NewContactIndex()
	next := c.GraphBase + "/me/contacts?$top=999&$select=id,displayName,companyName,mobilePhone,businessPhones,homePhones"
	for range maxPages {
		var p contactPage
		if err := c.j.Do(ctx, http.MethodGet, next, token, nil, nil, &p); err != nil {
			return nil, err
		}
		for _, x := range p.Value {
			phones := append(append([]string{x.MobilePhone}, x.BusinessPhone...), x.HomePhones...)
			idx.Add(crm.Contact{ID: x.ID, Name: x.DisplayName, Company: x.CompanyName}, phones...)
		}
		if p.NextLink == "" || !strings.HasPrefix(p.NextLink, c.GraphBase+"/") {
			break
		}
		next = p.NextLink
	}
	idx.Loaded = time.Now()
	return idx, nil
}

// Lookup implements crm.Connector: the user's contacts, matched on the
// number's digits.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	idx, err := c.index.Get(cc.ExternalAcct, func() (*crm.ContactIndex, error) { return c.load(ctx, cc.AccessToken) })
	if err != nil {
		return nil, err
	}
	return idx.Find(number), nil
}

// LogCall implements crm.Connector; Outlook does not log calls.
func (c *Connector) LogCall(context.Context, crm.Conn, crm.CallLog) (string, error) {
	return "", &crm.APIError{Msg: "the Outlook connector does not log calls"}
}

// UpdateCallLog implements crm.Connector.
func (c *Connector) UpdateCallLog(context.Context, crm.Conn, string, crm.CallLog) error {
	return &crm.APIError{Msg: "the Outlook connector does not log calls"}
}

// ResolveUser implements crm.Connector; every user is their own account.
func (c *Connector) ResolveUser(context.Context, crm.Conn, string) (string, error) { return "", nil }

type calEvent struct {
	ShowAs      string `json:"showAs"`
	IsCancelled bool   `json:"isCancelled"`
	IsAllDay    bool   `json:"isAllDay"`
	Start       struct {
		DateTime string `json:"dateTime"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
	} `json:"end"`
	ResponseStatus struct {
		Response string `json:"response"`
	} `json:"responseStatus"`
}

func graphTime(s string) (time.Time, bool) {
	// With Prefer: outlook.timezone="UTC" Graph returns "2026-11-12T10:00:00.0000000".
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Busy implements crm.Calendar: the user is busy while an event that shows
// them as busy or out of office covers the moment. A cancelled event, a
// declined one, a tentative one and a free all-day marker (a birthday) do not
// count; an all-day event counts only as out of office.
func (c *Connector) Busy(ctx context.Context, cc crm.Conn, at time.Time) (crm.Busy, error) {
	q := url.Values{}
	q.Set("startDateTime", at.UTC().Format("2006-01-02T15:04:05"))
	q.Set("endDateTime", at.UTC().Add(time.Minute).Format("2006-01-02T15:04:05"))
	q.Set("$select", "showAs,isCancelled,isAllDay,start,end,responseStatus")
	q.Set("$top", "50")
	var out struct {
		Value []calEvent `json:"value"`
	}
	if err := c.j.Do(ctx, http.MethodGet, c.GraphBase+"/me/calendarView?"+q.Encode(), cc.AccessToken,
		map[string]string{"Prefer": `outlook.timezone="UTC"`}, nil, &out); err != nil {
		return crm.Busy{}, err
	}
	var b crm.Busy
	for _, e := range out.Value {
		if e.IsCancelled || e.ResponseStatus.Response == "declined" {
			continue
		}
		busy := e.ShowAs == "busy" || e.ShowAs == "oof"
		if e.IsAllDay {
			busy = e.ShowAs == "oof"
		}
		if !busy {
			continue
		}
		end, ok := graphTime(e.End.DateTime)
		if !ok {
			continue
		}
		b.Busy = true
		if end.After(b.Until) {
			b.Until = end
		}
	}
	return b, nil
}

var (
	_ crm.Connector       = (*Connector)(nil)
	_ crm.OAuth           = (*Connector)(nil)
	_ crm.Calendar        = (*Connector)(nil)
	_ crm.Pinger          = (*Connector)(nil)
	_ crm.ConfigValidator = (*Connector)(nil)
)
