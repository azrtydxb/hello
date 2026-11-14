// Package freshdesk is the Freshdesk connector of internal/crm (spec
// integrations I-47, P16): the caller is a Freshdesk contact found by phone,
// and every call becomes a phone-source ticket with the recording link added
// as a private note.
//
// Authentication is the agent's API key (Profile settings in Freshdesk) sent
// as HTTP Basic "key:X". Facts marked "verify" in docs/crm.md need a real
// Freshdesk account.
package freshdesk

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
const Kind = "freshdesk"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Freshdesk's numeric ticket values.
const (
	sourcePhone    = 3
	priorityLow    = 1
	statusOpen     = 2
	statusPending  = 3
	statusResolved = 4
)

// Connector is the Freshdesk connector.
type Connector struct {
	hc *http.Client
	// Base overrides https://<subdomain>.freshdesk.com for tests.
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
func (c *Connector) SecretBoundKeys() []string { return []string{"subdomain"} }

// ValidateConfig implements crm.ConfigValidator.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "subdomain", "answeredStatus":
			out[k] = v
		case "clientId":
			// Freshdesk has no client id; an empty one is accepted so the
			// console form can share its fields with the other connectors.
			if v != "" {
				return nil, errors.New("freshdesk has no client id")
			}
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	out["subdomain"] = strings.ToLower(out["subdomain"])
	if !kit.Label(out["subdomain"]) {
		return nil, errors.New("subdomain is the first part of your Freshdesk address (acme for acme.freshdesk.com)")
	}
	switch out["answeredStatus"] {
	case "":
		out["answeredStatus"] = "open"
	case "open", "pending", "resolved":
	default:
		return nil, errors.New("answeredStatus must be open, pending or resolved")
	}
	return out, nil
}

func (c *Connector) base(cfg crm.Config) string {
	if c.Base != "" {
		return c.Base
	}
	return "https://" + cfg.Extra["subdomain"] + ".freshdesk.com"
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	return kit.Do(ctx, c.hc, kit.Request{
		Method: method, URL: c.base(cc.Config) + path, Body: body, Authed: true,
		Header: map[string]string{"Authorization": kit.Basic(cc.AccessToken, "X")},
	}, out)
}

// Authenticate implements crm.Direct.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	err := c.Ping(ctx, crm.Conn{Config: cfg, AccessToken: cfg.ClientSecret})
	if errors.Is(err, crm.ErrAuth) {
		return crm.Tokens{}, &crm.APIError{Status: http.StatusUnauthorized, Msg: "Freshdesk refused the API key"}
	}
	if err != nil {
		return crm.Tokens{}, err
	}
	return crm.Tokens{Access: cfg.ClientSecret, Account: cfg.Extra["subdomain"]}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/api/v2/agents/me", nil, nil)
}

type contact struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	Mobile    string `json:"mobile"`
	CompanyID int64  `json:"company_id"`
}

// variants are the spellings of a number a Freshdesk phone filter may have
// to match: the filter compares the stored text exactly.
func variants(n string) []string {
	d := kit.Digits(n)
	return []string{"+" + d, d, "00" + d}
}

// Lookup implements crm.Connector: contacts whose phone or mobile equals the
// number in E.164, bare digits or 00 form (Freshdesk filters on the stored
// text, so the customer's own format must be one of these; verify).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	for _, v := range variants(n) {
		for _, field := range []string{"phone", "mobile"} {
			var list []contact
			q := url.Values{field: {v}}
			if err := c.do(ctx, cc, http.MethodGet, "/api/v2/contacts?"+q.Encode(), nil, &list); err != nil {
				return nil, err
			}
			for _, ct := range list {
				if ct.ID == 0 || !kit.AnySame(n, ct.Phone, ct.Mobile) {
					continue
				}
				out := &crm.Contact{ID: strconv.FormatInt(ct.ID, 10), Name: ct.Name,
					URL: "https://" + cc.Config.Extra["subdomain"] + ".freshdesk.com/a/contacts/" + strconv.FormatInt(ct.ID, 10)}
				if ct.CompanyID != 0 {
					var co struct {
						Name string `json:"name"`
					}
					if c.do(ctx, cc, http.MethodGet, "/api/v2/companies/"+strconv.FormatInt(ct.CompanyID, 10), nil, &co) == nil {
						out.Company = co.Name
					}
				}
				return out, nil
			}
		}
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the agent whose email is the Hello
// user's.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var list []struct {
		ID      int64 `json:"id"`
		Contact struct {
			Email string `json:"email"`
		} `json:"contact"`
	}
	q := url.Values{"email": {email}}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v2/agents?"+q.Encode(), nil, &list); err != nil {
		return "", err
	}
	for _, a := range list {
		if strings.EqualFold(a.Contact.Email, email) {
			return strconv.FormatInt(a.ID, 10), nil
		}
	}
	return "", nil
}

func ticketStatus(cc crm.Conn, l crm.CallLog) int {
	if l.Missed || l.Outcome != crm.OutcomeAnswered {
		return statusOpen
	}
	switch cc.Config.Extra["answeredStatus"] {
	case "pending":
		return statusPending
	case "resolved":
		return statusResolved
	}
	return statusOpen
}

// LogCall implements crm.Connector. Without a contact the ticket carries the
// phone number and Freshdesk makes the contact itself.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	t := map[string]any{
		"subject":     kit.Clip(kit.Title(l), 250),
		"description": kit.HTML(l),
		"status":      ticketStatus(cc, l),
		"priority":    priorityLow,
		"source":      sourcePhone,
		"tags":        []string{"hello", "call_" + l.Direction},
	}
	if l.Contact != nil && l.Contact.ID != "" {
		id, err := strconv.ParseInt(l.Contact.ID, 10, 64)
		if err != nil {
			return "", &crm.APIError{Msg: "the contact id is not a Freshdesk id"}
		}
		t["requester_id"] = id
	} else {
		n, _ := crm.NormalizeNumber(l.Number)
		if n == "" {
			n = l.Number
		}
		t["phone"], t["name"] = n, n
	}
	if l.Missed {
		t["tags"] = []string{"hello", "call_" + l.Direction, "missed_call"}
	}
	if id, err := strconv.ParseInt(l.OwnerID, 10, 64); err == nil && id != 0 {
		t["responder_id"] = id
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, cc, http.MethodPost, "/api/v2/tickets", t, &res); err != nil {
		return "", err
	}
	if res.ID == 0 {
		return "", &crm.APIError{Msg: "the response has no ticket id"}
	}
	return strconv.FormatInt(res.ID, 10), nil
}

// UpdateCallLog implements crm.Connector: a private note with the recording
// link, unless the ticket already has it.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.RecordingURL == "" {
		return nil
	}
	id := url.PathEscape(ref)
	var list []struct {
		Body     string `json:"body"`
		BodyText string `json:"body_text"`
	}
	if err := c.do(ctx, cc, http.MethodGet, "/api/v2/tickets/"+id+"/conversations", nil, &list); err != nil {
		return err
	}
	for _, cv := range list {
		if strings.Contains(cv.Body, l.RecordingURL) || strings.Contains(cv.BodyText, l.RecordingURL) {
			return nil
		}
	}
	e := kit.Escape(l.RecordingURL)
	body := map[string]any{"private": true, "body": `Recording: <a href="` + e + `">` + e + `</a>`}
	return c.do(ctx, cc, http.MethodPost, "/api/v2/tickets/"+id+"/notes", body, nil)
}
