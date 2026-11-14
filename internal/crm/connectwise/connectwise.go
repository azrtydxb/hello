// Package connectwise is the ConnectWise Manage connector of internal/crm
// (spec integrations I-47, P20): the caller is a Manage contact found by
// phone, and every call becomes a Call activity for that contact, assigned to
// the member who took or placed it, with the recording link in its notes.
//
// Authentication is an API member (System, Members, API Members) with its
// public and private key, plus the integrator's clientId header that
// ConnectWise requires on every request. Calls from a number that is no
// contact are skipped: an activity needs a company. Facts marked "verify" in
// docs/crm.md need a real ConnectWise Manage instance.
package connectwise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit"
)

// Kind is the connector kind.
const Kind = "connectwise"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Connector is the ConnectWise Manage connector.
type Connector struct {
	hc *http.Client
	// Base overrides https://<host>/<codebase>/apis/3.0 for tests.
	Base string

	mu    sync.Mutex
	types map[string]cachedType
}

type cachedType struct {
	id int64
	at time.Time
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc, types: map[string]cachedType{}} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true}
}

// SecretBoundKeys implements crm.SecretBound: the host, the company and the
// public key decide where and as whom the private key is used.
func (c *Connector) SecretBoundKeys() []string {
	return []string{"clientId", "host", "companyId", "publicKey", "codebase"}
}

// ValidateConfig implements crm.ConfigValidator. clientId is the
// integrator's clientId header (from developer.connectwise.com), host the
// API host (api-na.myconnectwise.net, api-eu..., api-au... or your own),
// companyId the login company, codebase the release path (v4_6_release) and
// activityType the name of the activity type calls get (Call).
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "host", "companyId", "publicKey", "codebase", "activityType":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	out["host"] = strings.ToLower(out["host"])
	if !kit.Host(out["host"]) {
		return nil, errors.New("host is the API host without https://, for example api-na.myconnectwise.net")
	}
	if out["clientId"] == "" || len(out["clientId"]) > 100 {
		return nil, errors.New("clientId is the integrator client id ConnectWise issued to you")
	}
	for _, k := range []string{"companyId", "publicKey"} {
		if out[k] == "" || len(out[k]) > 100 || strings.ContainsAny(out[k], ":+ ") {
			return nil, fmt.Errorf("%s is required and must not contain a colon, plus or space", k)
		}
	}
	if out["codebase"] == "" {
		out["codebase"] = "v4_6_release"
	}
	if strings.Trim(out["codebase"], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.") != "" {
		return nil, errors.New("codebase is a release path such as v4_6_release")
	}
	if out["activityType"] == "" {
		out["activityType"] = "Call"
	}
	if strings.ContainsAny(out["activityType"], `"\`) || len(out["activityType"]) > 50 {
		return nil, errors.New("activityType must not contain quotes")
	}
	return out, nil
}

func (c *Connector) base(cfg crm.Config) string {
	if c.Base != "" {
		return c.Base
	}
	return "https://" + cfg.Extra["host"] + "/" + cfg.Extra["codebase"] + "/apis/3.0"
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	r := kit.Request{
		Method: method, URL: c.base(cc.Config) + path, Body: body, Authed: true,
		Header: map[string]string{
			"Authorization": kit.Basic(cc.Config.Extra["companyId"]+"+"+cc.Config.Extra["publicKey"], cc.AccessToken),
			"clientId":      cc.Config.ClientID,
		},
	}
	return kit.Do(ctx, c.hc, r, out)
}

// Authenticate implements crm.Direct: the keys are proved with the system
// info call.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	err := c.Ping(ctx, crm.Conn{Config: cfg, AccessToken: cfg.ClientSecret})
	if errors.Is(err, crm.ErrAuth) {
		return crm.Tokens{}, &crm.APIError{Status: http.StatusUnauthorized, Msg: "ConnectWise refused the API member's keys"}
	}
	if err != nil {
		return crm.Tokens{}, err
	}
	return crm.Tokens{Access: cfg.ClientSecret, Account: cfg.Extra["companyId"]}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/system/info", nil, nil)
}

// contactID is "contact:company", the two ids an activity needs.
func contactID(contact, company int64) string {
	return strconv.FormatInt(contact, 10) + ":" + strconv.FormatInt(company, 10)
}

func parseContactID(s string) (contact, company int64, ok bool) {
	a, b, found := strings.Cut(s, ":")
	x, e1 := strconv.ParseInt(a, 10, 64)
	y, e2 := strconv.ParseInt(b, 10, 64)
	return x, y, found && e1 == nil && e2 == nil && x > 0 && y > 0
}

// Lookup implements crm.Connector: contacts with a communication item that
// contains the last four digits, kept when an item's digits equal the number (verify the like wildcard and the child condition).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	var list []struct {
		ID        int64  `json:"id"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Company   struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"company"`
		Items []struct {
			Value string `json:"value"`
		} `json:"communicationItems"`
	}
	q := url.Values{
		"childConditions": {`communicationItems/value like "*` + kit.Last4(n) + `*"`},
		"fields":          {"id,firstName,lastName,company/id,company/name,communicationItems"},
		"pageSize":        {"50"},
	}
	if err := c.do(ctx, cc, http.MethodGet, "/company/contacts?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	for _, ct := range list {
		if ct.ID == 0 || ct.Company.ID == 0 {
			continue
		}
		for _, it := range ct.Items {
			if kit.Same(it.Value, n) {
				return &crm.Contact{ID: contactID(ct.ID, ct.Company.ID), Name: strings.TrimSpace(ct.FirstName + " " + ct.LastName), Company: ct.Company.Name}, nil
			}
		}
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the member whose primary email is
// the Hello user's.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	if strings.ContainsAny(email, `"\`) {
		return "", nil
	}
	var list []struct {
		ID    int64  `json:"id"`
		Email string `json:"primaryEmail"`
		Off   string `json:"officeEmail"`
	}
	q := url.Values{"conditions": {`primaryEmail="` + email + `" or officeEmail="` + email + `"`}, "fields": {"id,primaryEmail,officeEmail"}, "pageSize": {"10"}}
	if err := c.do(ctx, cc, http.MethodGet, "/system/members?"+q.Encode(), nil, &list); err != nil {
		return "", err
	}
	for _, m := range list {
		if strings.EqualFold(m.Email, email) || strings.EqualFold(m.Off, email) {
			return strconv.FormatInt(m.ID, 10), nil
		}
	}
	return "", nil
}

// activityTypeID resolves the activity type's name to its id, cached for an
// hour.
func (c *Connector) activityTypeID(ctx context.Context, cc crm.Conn) (int64, error) {
	name := cc.Config.Extra["activityType"]
	if name == "" {
		name = "Call"
	}
	key := cc.Config.Extra["host"] + "|" + cc.Config.Extra["companyId"] + "|" + name
	c.mu.Lock()
	t, ok := c.types[key]
	c.mu.Unlock()
	if ok && time.Since(t.at) < time.Hour {
		return t.id, nil
	}
	var list []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	q := url.Values{"conditions": {`name="` + name + `"`}, "pageSize": {"5"}}
	if err := c.do(ctx, cc, http.MethodGet, "/sales/activities/types?"+q.Encode(), nil, &list); err != nil {
		return 0, err
	}
	for _, a := range list {
		if strings.EqualFold(a.Name, name) {
			c.mu.Lock()
			c.types[key] = cachedType{a.ID, time.Now()}
			c.mu.Unlock()
			return a.ID, nil
		}
	}
	return 0, &crm.APIError{Status: http.StatusUnprocessableEntity, Msg: `ConnectWise has no activity type named "` + name + `"; set activityType`}
}

type ref struct {
	ID int64 `json:"id"`
}

// LogCall implements crm.Connector.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	if l.Contact == nil {
		return "", fmt.Errorf("%w: no ConnectWise contact has this number", crm.ErrNoTarget)
	}
	contact, company, ok := parseContactID(l.Contact.ID)
	if !ok {
		return "", &crm.APIError{Msg: "the contact id is not a ConnectWise contact"}
	}
	member, err := strconv.ParseInt(l.OwnerID, 10, 64)
	if err != nil || member < 1 {
		return "", fmt.Errorf("%w: the call has no ConnectWise member", crm.ErrNoTarget)
	}
	typ, err := c.activityTypeID(ctx, cc)
	if err != nil {
		return "", err
	}
	start := kit.Timestamp(l)
	end := start.Add(l.Duration)
	if l.Duration <= 0 {
		end = start
	}
	body := map[string]any{
		"name": kit.Clip(kit.Title(l), 100), "type": ref{typ}, "company": ref{company}, "contact": ref{contact},
		"assignTo": ref{member}, "notes": kit.Text(l),
		"dateStart": start.Format(time.RFC3339), "dateEnd": end.Format(time.RFC3339),
	}
	var res ref
	if err := c.do(ctx, cc, http.MethodPost, "/sales/activities", body, &res); err != nil {
		return "", err
	}
	if res.ID == 0 {
		return "", &crm.APIError{Msg: "the response has no activity id"}
	}
	return strconv.FormatInt(res.ID, 10), nil
}

// UpdateCallLog implements crm.Connector: the notes are replaced with the
// text that carries the renewed recording link, which is idempotent.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, id string, l crm.CallLog) error {
	ops := []map[string]any{{"op": "replace", "path": "/notes", "value": kit.Text(l)}}
	raw, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	return c.doRaw(ctx, cc, http.MethodPatch, "/sales/activities/"+url.PathEscape(id), raw)
}

func (c *Connector) doRaw(ctx context.Context, cc crm.Conn, method, path string, body []byte) error {
	return kit.Do(ctx, c.hc, kit.Request{
		Method: method, URL: c.base(cc.Config) + path, Body: body, ContentType: "application/json", Authed: true,
		Header: map[string]string{
			"Authorization": kit.Basic(cc.Config.Extra["companyId"]+"+"+cc.Config.Extra["publicKey"], cc.AccessToken),
			"clientId":      cc.Config.ClientID,
		},
	}, nil)
}
