// Package google is the Google connector of internal/crm (spec integrations
// P18): each user signs in with their own Google account, their contacts
// (People API) name callers, and their calendar availability (Calendar
// free/busy, read only) says when they are busy (I-56). Only the free/busy
// scope is asked for: Hello never reads event titles or attendees.
//
// The request shapes follow Google's published REST references; the facts
// that need a real account are listed in docs/contacts-calendar.md.
package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit"
)

// Kind is the connector kind.
const Kind = "google"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Scopes.
const (
	ScopeContacts = "https://www.googleapis.com/auth/contacts.readonly"
	ScopeFreeBusy = "https://www.googleapis.com/auth/calendar.freebusy"
)

// Connector is the Google connector.
type Connector struct {
	j kit.Bearer
	// Hosts are replaced by tests.
	AuthBase, TokenURL, RevokeURL, UserInfoURL, PeopleBase, CalendarBase string
	index                                                                crm.IndexCache
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector {
	//nolint:gosec // G101: public OAuth endpoint addresses, not credentials
	return &Connector{j: kit.Bearer{HC: hc},
		AuthBase: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
		RevokeURL: "https://oauth2.googleapis.com/revoke", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
		PeopleBase: "https://people.googleapis.com/v1", CalendarBase: "https://www.googleapis.com/calendar/v3"}
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, UserScope: true, Calendar: true}
}

// ValidateConfig implements crm.ConfigValidator: features is contacts,
// calendar or both.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "features":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
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

// ScopesFor asks only what the chosen features need.
func ScopesFor(cfg crm.Config) []string {
	s := []string{"openid", "email"}
	for _, f := range crm.Features(cfg.Extra) {
		switch f {
		case crm.FeatureContacts:
			s = append(s, ScopeContacts)
		case crm.FeatureCalendar:
			s = append(s, ScopeFreeBusy)
		}
	}
	return s
}

// AuthURL implements crm.OAuth. access_type=offline with prompt=consent makes
// Google return a refresh token on every sign-in.
func (c *Connector) AuthURL(cfg crm.Config, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(ScopesFor(cfg), " "))
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	return c.AuthBase + "?" + q.Encode()
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
	var tr tokenResponse
	if err := c.j.Do(ctx, http.MethodPost, c.TokenURL, "", nil, form, &tr); err != nil {
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	return crm.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		Scopes: strings.Fields(tr.Scope)}, nil
}

// Exchange implements crm.OAuth.
func (c *Connector) Exchange(ctx context.Context, cfg crm.Config, redirectURI, code string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI}, "code": {code}})
	if err != nil {
		return t, err
	}
	var ui struct {
		Email string `json:"email"`
	}
	if err := c.j.Do(ctx, http.MethodGet, c.UserInfoURL, t.Access, nil, nil, &ui); err != nil {
		return t, err
	}
	t.Account = ui.Email
	return t, nil
}

// Refresh implements crm.OAuth.
func (c *Connector) Refresh(ctx context.Context, cfg crm.Config, refresh string) (crm.Tokens, error) {
	return c.token(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

// RevokeToken implements crm.Revoker.
func (c *Connector) RevokeToken(ctx context.Context, _ crm.Config, refresh string) error {
	return c.j.Do(ctx, http.MethodPost, c.RevokeURL, "", nil, url.Values{"token": {refresh}}, nil)
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.j.Do(ctx, http.MethodGet, c.UserInfoURL, cc.AccessToken, nil, nil, nil)
}

type person struct {
	ResourceName string `json:"resourceName"`
	Names        []struct {
		DisplayName string `json:"displayName"`
	} `json:"names"`
	Orgs []struct {
		Name string `json:"name"`
	} `json:"organizations"`
	Phones []struct {
		Value     string `json:"value"`
		Canonical string `json:"canonicalForm"`
	} `json:"phoneNumbers"`
}

const maxPages = 20

// load reads the user's contacts into an index; like Outlook's it outlives
// the caller's 1.5 s lookup budget.
func (c *Connector) load(ctx context.Context, token string) (*crm.ContactIndex, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	idx := crm.NewContactIndex()
	page := ""
	for range maxPages {
		q := url.Values{}
		q.Set("personFields", "names,phoneNumbers,organizations")
		q.Set("pageSize", "1000")
		if page != "" {
			q.Set("pageToken", page)
		}
		var out struct {
			Connections   []person `json:"connections"`
			NextPageToken string   `json:"nextPageToken"`
		}
		if err := c.j.Do(ctx, http.MethodGet, c.PeopleBase+"/people/me/connections?"+q.Encode(), token, nil, nil, &out); err != nil {
			return nil, err
		}
		for _, p := range out.Connections {
			ct := crm.Contact{ID: p.ResourceName}
			if len(p.Names) > 0 {
				ct.Name = p.Names[0].DisplayName
			}
			if len(p.Orgs) > 0 {
				ct.Company = p.Orgs[0].Name
			}
			var phones []string
			for _, ph := range p.Phones {
				phones = append(phones, ph.Value, ph.Canonical)
			}
			idx.Add(ct, phones...)
		}
		if out.NextPageToken == "" {
			break
		}
		page = out.NextPageToken
	}
	idx.Loaded = time.Now()
	return idx, nil
}

// Lookup implements crm.Connector.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	idx, err := c.index.Get(cc.ExternalAcct, func() (*crm.ContactIndex, error) { return c.load(ctx, cc.AccessToken) })
	if err != nil {
		return nil, err
	}
	return idx.Find(number), nil
}

// LogCall implements crm.Connector; Google does not log calls.
func (c *Connector) LogCall(context.Context, crm.Conn, crm.CallLog) (string, error) {
	return "", &crm.APIError{Msg: "the Google connector does not log calls"}
}

// UpdateCallLog implements crm.Connector.
func (c *Connector) UpdateCallLog(context.Context, crm.Conn, string, crm.CallLog) error {
	return &crm.APIError{Msg: "the Google connector does not log calls"}
}

// ResolveUser implements crm.Connector.
func (c *Connector) ResolveUser(context.Context, crm.Conn, string) (string, error) { return "", nil }

// Busy implements crm.Calendar with the free/busy query on the primary
// calendar: busy while a busy block covers the moment, until the latest end
// of the blocks that follow each other from it.
func (c *Connector) Busy(ctx context.Context, cc crm.Conn, at time.Time) (crm.Busy, error) {
	at = at.UTC()
	req := map[string]any{
		"timeMin": at.Format(time.RFC3339), "timeMax": at.Add(12 * time.Hour).Format(time.RFC3339),
		"items": []map[string]string{{"id": "primary"}},
	}
	var out struct {
		Calendars map[string]struct {
			Busy []struct {
				Start time.Time `json:"start"`
				End   time.Time `json:"end"`
			} `json:"busy"`
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"calendars"`
	}
	if err := c.j.Do(ctx, http.MethodPost, c.CalendarBase+"/freeBusy", cc.AccessToken, nil, req, &out); err != nil {
		return crm.Busy{}, err
	}
	cal := out.Calendars["primary"]
	if len(cal.Errors) > 0 {
		return crm.Busy{}, &crm.APIError{Msg: "free/busy: " + cal.Errors[0].Reason}
	}
	// Blocks come sorted; chain the ones that begin at or before the end of
	// the one covering now (a 10:00-10:30 and a 10:30-11:00 meeting).
	var b crm.Busy
	cur := at
	for _, x := range cal.Busy {
		if x.Start.After(cur) {
			break
		}
		if x.End.After(cur) {
			cur = x.End
			b.Busy, b.Until = true, x.End
		}
	}
	return b, nil
}

var (
	_ crm.Connector       = (*Connector)(nil)
	_ crm.OAuth           = (*Connector)(nil)
	_ crm.Calendar        = (*Connector)(nil)
	_ crm.Pinger          = (*Connector)(nil)
	_ crm.Revoker         = (*Connector)(nil)
	_ crm.ConfigValidator = (*Connector)(nil)
)
