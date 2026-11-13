// Package autotask is the Datto Autotask PSA connector of internal/crm (spec
// integrations I-47, P20): the caller is an Autotask contact found by phone,
// and every call becomes a company note on the contact's company, assigned to
// the resource who took or placed the call, with the recording link in it.
//
// Authentication is an API user with its secret and the integration code of
// a tracking identifier (Admin, Resources (Users), API user). The REST zone
// (webservices5.autotask.net and so on) is discovered from the API user name
// unless the administrator sets it. Calls from a number that is no contact
// are skipped: a company note needs a company. Facts marked "verify" in
// docs/crm.md need a real Autotask zone.
package autotask

import (
	"context"
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
const Kind = "autotask"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// zoneDiscovery is Autotask's zone lookup host.
const zoneDiscovery = "https://webservices.autotask.net"

// Connector is the Autotask connector.
type Connector struct {
	hc *http.Client
	// Base overrides https://<zone host>/atservicesrest/v1.0 and, with
	// Discovery, the zone lookup, for tests.
	Base      string
	Discovery string

	mu      sync.Mutex
	actions map[string]cachedAction
}

type cachedAction struct {
	value int64
	at    time.Time
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc, actions: map[string]cachedAction{}} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true}
}

// SecretBoundKeys implements crm.SecretBound.
func (c *Connector) SecretBoundKeys() []string {
	return []string{"clientId", "host", "integrationCode"}
}

// ValidateConfig implements crm.ConfigValidator. clientId is the API user
// name, integrationCode the tracking identifier, host an optional zone
// (webservices5.autotask.net; found from the user name when empty) and
// actionType an optional company-note action type id (the one labelled
// "Call", found from Autotask's pick list when empty).
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "integrationCode", "host", "actionType":
			if v != "" {
				out[k] = v
			}
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	if !strings.Contains(out["clientId"], "@") || len(out["clientId"]) > 200 {
		return nil, errors.New("clientId is the API user name, an email-style address")
	}
	if out["integrationCode"] == "" || len(out["integrationCode"]) > 100 {
		return nil, errors.New("integrationCode is the API tracking identifier")
	}
	if h := strings.ToLower(out["host"]); h != "" {
		if !kit.Host(h) || !strings.HasSuffix(h, ".autotask.net") {
			return nil, errors.New("host is an Autotask zone such as webservices5.autotask.net")
		}
		out["host"] = h
	}
	if v := out["actionType"]; v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			return nil, errors.New("actionType must be a number")
		}
	}
	return out, nil
}

func (c *Connector) base(cc crm.Conn) (string, error) {
	if c.Base != "" {
		return c.Base, nil
	}
	h := cc.ExternalAcct
	if h == "" {
		h = cc.Config.Extra["host"]
	}
	if h == "" {
		return "", &crm.APIError{Msg: "the Autotask zone is not known; connect again"}
	}
	return "https://" + h + "/atservicesrest/v1.0", nil
}

func headers(cc crm.Conn) map[string]string {
	return map[string]string{"UserName": cc.Config.ClientID, "Secret": cc.AccessToken, "ApiIntegrationCode": cc.Config.Extra["integrationCode"]}
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	b, err := c.base(cc)
	if err != nil {
		return err
	}
	return kit.Do(ctx, c.hc, kit.Request{Method: method, URL: b + path, Body: body, Authed: true, Header: headers(cc)}, out)
}

// Authenticate implements crm.Direct: it finds the zone, then proves the
// credentials with a harmless read.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	host := cfg.Extra["host"]
	if host == "" {
		d := c.Discovery
		if d == "" {
			d = zoneDiscovery
		}
		var z struct {
			URL string `json:"url"`
		}
		u := d + "/atservicesrest/v1.0/zoneInformation?" + url.Values{"user": {cfg.ClientID}}.Encode()
		if err := kit.Do(ctx, c.hc, kit.Request{Method: http.MethodGet, URL: u}, &z); err != nil {
			return crm.Tokens{}, err
		}
		pu, err := url.Parse(z.URL)
		if err != nil || pu.Hostname() == "" || !strings.HasSuffix(pu.Hostname(), ".autotask.net") {
			return crm.Tokens{}, &crm.APIError{Status: http.StatusUnprocessableEntity, Msg: "Autotask found no zone for this API user"}
		}
		host = pu.Hostname()
	}
	cc := crm.Conn{Config: cfg, AccessToken: cfg.ClientSecret, ExternalAcct: host}
	err := c.Ping(ctx, cc)
	if errors.Is(err, crm.ErrAuth) {
		return crm.Tokens{}, &crm.APIError{Status: http.StatusUnauthorized, Msg: "Autotask refused the API user, secret or integration code"}
	}
	if err != nil {
		return crm.Tokens{}, err
	}
	return crm.Tokens{Access: cfg.ClientSecret, Account: host}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/Contacts/entityInformation", nil, nil)
}

type filterItem struct {
	Op    string       `json:"op"`
	Field string       `json:"field,omitempty"`
	Value any          `json:"value,omitempty"`
	Items []filterItem `json:"items,omitempty"`
}

func query(items ...filterItem) map[string]any {
	return map[string]any{"filter": items, "MaxRecords": 50}
}

// Lookup implements crm.Connector: active contacts whose phone, mobile or
// alternate phone contains the last four digits, kept when one equals the
// number.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	l4 := kit.Last4(n)
	q := query(filterItem{Op: "and", Items: []filterItem{
		{Op: "eq", Field: "isActive", Value: 1},
		{Op: "or", Items: []filterItem{
			{Op: "contains", Field: "phone", Value: l4},
			{Op: "contains", Field: "mobilePhone", Value: l4},
			{Op: "contains", Field: "alternatePhone", Value: l4},
		}},
	}})
	var res struct {
		Items []struct {
			ID        int64  `json:"id"`
			First     string `json:"firstName"`
			Last      string `json:"lastName"`
			CompanyID int64  `json:"companyID"`
			Phone     string `json:"phone"`
			Mobile    string `json:"mobilePhone"`
			Alt       string `json:"alternatePhone"`
		} `json:"items"`
	}
	if err := c.do(ctx, cc, http.MethodPost, "/Contacts/query", q, &res); err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		if it.ID == 0 || it.CompanyID == 0 || !kit.AnySame(n, it.Phone, it.Mobile, it.Alt) {
			continue
		}
		ct := &crm.Contact{ID: strconv.FormatInt(it.ID, 10) + ":" + strconv.FormatInt(it.CompanyID, 10), Name: strings.TrimSpace(it.First + " " + it.Last)}
		var co struct {
			Item struct {
				Name string `json:"companyName"`
			} `json:"item"`
		}
		if c.do(ctx, cc, http.MethodGet, "/Companies/"+strconv.FormatInt(it.CompanyID, 10), nil, &co) == nil {
			ct.Company = co.Item.Name
		}
		return ct, nil
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the active resource whose email is
// the Hello user's.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	q := query(filterItem{Op: "eq", Field: "email", Value: email}, filterItem{Op: "eq", Field: "isActive", Value: true})
	var res struct {
		Items []struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
		} `json:"items"`
	}
	if err := c.do(ctx, cc, http.MethodPost, "/Resources/query", q, &res); err != nil {
		return "", err
	}
	for _, r := range res.Items {
		if strings.EqualFold(r.Email, email) {
			return strconv.FormatInt(r.ID, 10), nil
		}
	}
	return "", nil
}

// actionType is the company-note action type calls get: the configured id,
// else the pick-list entry labelled "Call" or "Phone" (cached an hour).
func (c *Connector) actionType(ctx context.Context, cc crm.Conn) (int64, error) {
	if v, err := strconv.ParseInt(cc.Config.Extra["actionType"], 10, 64); err == nil && v > 0 {
		return v, nil
	}
	key := cc.ExternalAcct + "|" + cc.Config.ClientID
	c.mu.Lock()
	t, ok := c.actions[key]
	c.mu.Unlock()
	if ok && time.Since(t.at) < time.Hour {
		return t.value, nil
	}
	var res struct {
		Fields []struct {
			Name   string `json:"name"`
			Values []struct {
				Value string `json:"value"`
				Label string `json:"label"`
			} `json:"picklistValues"`
		} `json:"fields"`
	}
	if err := c.do(ctx, cc, http.MethodGet, "/CompanyNotes/entityInformation/fields", nil, &res); err != nil {
		return 0, err
	}
	for _, f := range res.Fields {
		if !strings.EqualFold(f.Name, "actionType") {
			continue
		}
		for _, v := range f.Values {
			l := strings.ToLower(v.Label)
			if l == "call" || l == "phone call" || l == "phone" {
				if id, err := strconv.ParseInt(v.Value, 10, 64); err == nil {
					c.mu.Lock()
					c.actions[key] = cachedAction{id, time.Now()}
					c.mu.Unlock()
					return id, nil
				}
			}
		}
	}
	return 0, &crm.APIError{Status: http.StatusUnprocessableEntity, Msg: `Autotask has no company-note action type labelled "Call"; set actionType`}
}

// LogCall implements crm.Connector.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	if l.Contact == nil {
		return "", fmt.Errorf("%w: no Autotask contact has this number", crm.ErrNoTarget)
	}
	a, b, _ := strings.Cut(l.Contact.ID, ":")
	contact, e1 := strconv.ParseInt(a, 10, 64)
	company, e2 := strconv.ParseInt(b, 10, 64)
	if e1 != nil || e2 != nil || contact < 1 || company < 1 {
		return "", &crm.APIError{Msg: "the contact id is not an Autotask contact"}
	}
	res, err := strconv.ParseInt(l.OwnerID, 10, 64)
	if err != nil || res < 1 {
		return "", fmt.Errorf("%w: the call has no Autotask resource", crm.ErrNoTarget)
	}
	typ, err := c.actionType(ctx, cc)
	if err != nil {
		return "", err
	}
	start := kit.Timestamp(l)
	end := start
	if l.Duration > 0 {
		end = start.Add(l.Duration)
	}
	body := map[string]any{
		"companyID": company, "contactID": contact, "assignedResourceID": res, "actionType": typ,
		"note": kit.Title(l) + "\n" + kit.Text(l), "startDateTime": start.Format(time.RFC3339), "endDateTime": end.Format(time.RFC3339),
	}
	var out struct {
		ItemID int64 `json:"itemId"`
	}
	if err := c.do(ctx, cc, http.MethodPost, "/CompanyNotes", body, &out); err != nil {
		return "", err
	}
	if out.ItemID == 0 {
		return "", &crm.APIError{Msg: "the response has no note id"}
	}
	return strconv.FormatInt(out.ItemID, 10), nil
}

// UpdateCallLog implements crm.Connector: the note text is rewritten whole,
// which carries the renewed recording link and is idempotent.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return &crm.APIError{Msg: "the stored note id is not a number"}
	}
	body := map[string]any{"id": id, "note": kit.Title(l) + "\n" + kit.Text(l)}
	return c.do(ctx, cc, http.MethodPatch, "/CompanyNotes", body, nil)
}
