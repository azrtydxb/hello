// Package zendesk is the Zendesk Support connector of internal/crm (spec
// integrations I-47, P16): the caller is a Zendesk user found by phone, and
// every call becomes a ticket with the call's facts in an internal note and
// the recording link added as a second note.
//
// Authentication is Zendesk's API token: the administrator gives an agent
// email and an API token (Admin Center, Apps and integrations, APIs,
// Zendesk API, with token access enabled). Hello sends them as HTTP Basic
// "email/token:token". Facts marked "verify" in docs/crm.md need a real
// Zendesk account; the request shapes follow the published Support API.
package zendesk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit"
)

// Kind is the connector kind.
const Kind = "zendesk"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Connector is the Zendesk connector.
type Connector struct {
	hc *http.Client
	// Base overrides https://<subdomain>.zendesk.com; tests point it at an
	// httptest server.
	Base string
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. Click-to-call is the sidebar app's
// link to Hello's dialer, not a server feature.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, Ticket: true}
}

// SecretBoundKeys implements crm.SecretBound: the email and the subdomain
// decide who receives the API token.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "subdomain"} }

// ValidateConfig implements crm.ConfigValidator. clientId is the agent
// email the API token belongs to.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "subdomain", "createContacts", "answeredStatus":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	out["subdomain"] = strings.ToLower(out["subdomain"])
	if !kit.Label(out["subdomain"]) {
		return nil, errors.New("subdomain is the first part of your Zendesk address (acme for acme.zendesk.com)")
	}
	if !strings.Contains(out["clientId"], "@") || len(out["clientId"]) > 200 {
		return nil, errors.New("clientId is the email address of the agent the API token belongs to")
	}
	switch out["createContacts"] {
	case "":
		out["createContacts"] = "true"
	case "true", "false":
	default:
		return nil, errors.New("createContacts must be true or false")
	}
	switch out["answeredStatus"] {
	case "":
		out["answeredStatus"] = "open"
	case "open", "pending", "hold", "solved":
	default:
		return nil, errors.New("answeredStatus must be open, pending, hold or solved")
	}
	return out, nil
}

func (c *Connector) base(cfg crm.Config) string {
	if c.Base != "" {
		return c.Base
	}
	return "https://" + cfg.Extra["subdomain"] + ".zendesk.com"
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	return kit.Do(ctx, c.hc, kit.Request{
		Method: method, URL: c.base(cc.Config) + path, Body: body, Authed: true,
		Header: map[string]string{"Authorization": kit.Basic(cc.Config.Extra["clientId"]+"/token", cc.AccessToken)},
	}, out)
}

// Authenticate implements crm.Direct: it proves the email and API token
// with the agent's own profile.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	cc := crm.Conn{Config: cfg, AccessToken: cfg.ClientSecret}
	err := c.Ping(ctx, cc)
	if errors.Is(err, crm.ErrAuth) {
		return crm.Tokens{}, &crm.APIError{Status: http.StatusUnauthorized, Msg: "Zendesk refused the email and API token"}
	}
	if err != nil {
		return crm.Tokens{}, err
	}
	return crm.Tokens{Access: cfg.ClientSecret, Account: cfg.Extra["subdomain"]}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/api/v2/users/me.json", nil, nil)
}

type user struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
	Role  string `json:"role"`
	Org   int64  `json:"organization_id"`
}

// Lookup implements crm.Connector: the user whose phone is the number (the
// search takes E.164, which is how Zendesk stores phones).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok || !strings.HasPrefix(n, "+") {
		return nil, nil
	}
	var res struct {
		Results []user `json:"results"`
	}
	q := url.Values{"query": {"type:user phone:" + n}}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v2/search.json?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	for _, u := range res.Results {
		if u.ID == 0 || (u.Phone != "" && !kit.Same(u.Phone, n)) {
			continue
		}
		ct := &crm.Contact{ID: strconv.FormatInt(u.ID, 10), Name: u.Name,
			URL: "https://" + cc.Config.Extra["subdomain"] + ".zendesk.com/agent/users/" + strconv.FormatInt(u.ID, 10)}
		if u.Org != 0 {
			var o struct {
				Organization struct {
					Name string `json:"name"`
				} `json:"organization"`
			}
			if c.do(ctx, cc, http.MethodGet, "/api/v2/organizations/"+strconv.FormatInt(u.Org, 10)+".json", nil, &o) == nil {
				ct.Company = o.Organization.Name
			}
		}
		return ct, nil
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the agent or admin whose email is
// the Hello user's, compared exactly (case-insensitively).
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var res struct {
		Users []user `json:"users"`
	}
	q := url.Values{"query": {email}}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v2/users/search.json?"+q.Encode(), nil, &res); err != nil {
		return "", err
	}
	for _, u := range res.Users {
		if strings.EqualFold(u.Email, email) && (u.Role == "agent" || u.Role == "admin") {
			return strconv.FormatInt(u.ID, 10), nil
		}
	}
	return "", nil
}

func tags(l crm.CallLog) []string {
	t := []string{"hello", "call_" + l.Direction}
	if l.Missed {
		t = append(t, "missed_call")
	}
	return t
}

// requester returns the Zendesk user the ticket is for: the contact found
// by phone, or a new end user with the number when the setting allows it.
func (c *Connector) requester(ctx context.Context, cc crm.Conn, l crm.CallLog) (int64, error) {
	if l.Contact != nil && l.Contact.ID != "" {
		if id, err := strconv.ParseInt(l.Contact.ID, 10, 64); err == nil {
			return id, nil
		}
	}
	if cc.Config.Extra["createContacts"] == "false" {
		return 0, fmt.Errorf("%w: no Zendesk user has this number and creating users is off", crm.ErrNoTarget)
	}
	n, ok := crm.NormalizeNumber(l.Number)
	if !ok || !strings.HasPrefix(n, "+") {
		return 0, fmt.Errorf("%w: the number is not in international format, so Zendesk cannot store it", crm.ErrNoTarget)
	}
	var res struct {
		User user `json:"user"`
	}
	body := map[string]any{"user": map[string]any{"name": n, "phone": n, "role": "end-user"}}
	if err := c.do(ctx, cc, http.MethodPost, "/api/v2/users/create_or_update.json", body, &res); err != nil {
		return 0, err
	}
	if res.User.ID == 0 {
		return 0, &crm.APIError{Msg: "the response has no user id"}
	}
	return res.User.ID, nil
}

func status(cc crm.Conn, l crm.CallLog) string {
	if l.Missed || l.Outcome != crm.OutcomeAnswered {
		return "new"
	}
	if s := cc.Config.Extra["answeredStatus"]; s != "" {
		return s
	}
	return "open"
}

// LogCall implements crm.Connector: a ticket whose first comment is an
// internal note, so the customer is never e-mailed about their own call.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	req, err := c.requester(ctx, cc, l)
	if err != nil {
		return "", err
	}
	t := map[string]any{
		"subject":      kit.Clip(kit.Title(l), 150),
		"comment":      map[string]any{"body": kit.Text(l), "public": false},
		"requester_id": req,
		"status":       status(cc, l),
		"tags":         tags(l),
		"external_id":  "hello:" + l.CallID,
	}
	if id, err := strconv.ParseInt(l.OwnerID, 10, 64); err == nil && id != 0 {
		t["assignee_id"] = id
	}
	var res struct {
		Ticket struct {
			ID int64 `json:"id"`
		} `json:"ticket"`
	}
	if err := c.do(ctx, cc, http.MethodPost, "/api/v2/tickets.json", map[string]any{"ticket": t}, &res); err != nil {
		return "", err
	}
	if res.Ticket.ID == 0 {
		return "", &crm.APIError{Msg: "the response has no ticket id"}
	}
	return strconv.FormatInt(res.Ticket.ID, 10), nil
}

// UpdateCallLog implements crm.Connector: the recording link is added as an
// internal comment unless the ticket already carries it, so a retry or a
// renewal never repeats a note.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.RecordingURL == "" {
		return nil
	}
	id := url.PathEscape(ref)
	var list struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v2/tickets/"+id+"/comments.json", nil, &list); err != nil {
		return err
	}
	for _, cm := range list.Comments {
		if strings.Contains(cm.Body, l.RecordingURL) {
			return nil
		}
	}
	body := map[string]any{"ticket": map[string]any{"comment": map[string]any{"body": kit.RecordingNote(l.RecordingURL), "public": false}}}
	return c.do(ctx, cc, http.MethodPut, "/api/v2/tickets/"+id+".json", body, nil)
}
