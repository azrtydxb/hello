// Package hubspot is the HubSpot connector of internal/crm (spec
// integrations I-50): contact search by phone, calls written to the Calls
// object, owners by email, and the OAuth app flow of a customer-owned app.
//
// Facts marked "verify" in the docs are the ones that need a real HubSpot
// account; the request shapes follow HubSpot's published CRM v3 reference.
package hubspot

import (
	"bytes"
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
	"github.com/azrtydxb/hello/internal/safehttp"
)

// Kind is the connector kind.
const Kind = "hubspot"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Scopes Hello asks the customer's app for. Reading contacts and owners is
// all the lookup needs; creating and updating a call needs the write scope
// on contacts (HubSpot has no narrower one for the Calls object, verify).
var Scopes = []string{"crm.objects.contacts.read", "crm.objects.contacts.write", "crm.objects.owners.read"}

// associationCallToContact is HubSpot's defined association type id from a
// call to a contact.
const associationCallToContact = 194

// Connector is the HubSpot connector.
type Connector struct {
	hc *http.Client
	// APIBase is the REST host; AuthBase maps a region to the host the
	// browser is sent to. Tests point both at an httptest server.
	APIBase  string
	AuthBase map[string]string

	mu      sync.Mutex
	dispo   map[string]string // outcome label -> disposition id
	dispoAt time.Time
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector {
	return &Connector{hc: hc, APIBase: "https://api.hubapi.com", AuthBase: map[string]string{
		"na": "https://app.hubspot.com", "eu": "https://app-eu1.hubspot.com",
	}}
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. Click-to-call and the screen-pop
// are the iframe's, not the server's.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, ClickToCall: true, ScreenPop: true}
}

// ValidateConfig implements crm.ConfigValidator: the region picks the host
// the browser is sent to.
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
		out["region"] = "na"
	}
	if out["region"] != "na" && out["region"] != "eu" {
		return nil, errors.New("region must be na or eu")
	}
	return out, nil
}

func region(cfg crm.Config) string {
	if cfg.Extra["region"] == "eu" {
		return "eu"
	}
	return "na"
}

// AuthURL implements crm.OAuth.
func (c *Connector) AuthURL(cfg crm.Config, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("state", state)
	return c.AuthBase[region(cfg)] + "/oauth/authorize?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Connector) token(ctx context.Context, form url.Values) (crm.Tokens, error) {
	var tr tokenResponse
	if err := c.do(ctx, http.MethodPost, "/oauth/v1/token", "", form, &tr); err != nil {
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	return crm.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}, nil
}

// Exchange implements crm.OAuth: it trades the code for tokens and reads
// the portal id and the granted scopes from the token's metadata.
func (c *Connector) Exchange(ctx context.Context, cfg crm.Config, redirectURI, code string) (crm.Tokens, error) {
	t, err := c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {cfg.ClientID},
		"client_secret": {cfg.ClientSecret}, "redirect_uri": {redirectURI}, "code": {code}})
	if err != nil {
		return t, err
	}
	var meta struct {
		HubID  int64    `json:"hub_id"`
		Scopes []string `json:"scopes"`
	}
	if err := c.do(ctx, http.MethodGet, "/oauth/v1/access-tokens/"+url.PathEscape(t.Access), "", nil, &meta); err != nil {
		return t, err
	}
	t.Account, t.Scopes = strconv.FormatInt(meta.HubID, 10), meta.Scopes
	return t, nil
}

// Refresh implements crm.OAuth. A refresh token HubSpot rejects (400 or
// 401) is a permanent failure.
func (c *Connector) Refresh(ctx context.Context, cfg crm.Config, refresh string) (crm.Tokens, error) {
	t, err := c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {cfg.ClientID},
		"client_secret": {cfg.ClientSecret}, "refresh_token": {refresh}})
	var ae *crm.APIError
	if errors.Is(err, crm.ErrAuth) {
		return t, &crm.APIError{Status: http.StatusUnauthorized, Msg: "the refresh token was rejected"}
	}
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
		ae.Retry = false
	}
	return t, err
}

// RevokeToken implements crm.Revoker.
func (c *Connector) RevokeToken(ctx context.Context, _ crm.Config, refresh string) error {
	return c.do(ctx, http.MethodDelete, "/oauth/v1/refresh-tokens/"+url.PathEscape(refresh), "", nil, nil)
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, http.MethodGet, "/crm/v3/owners?limit=1", cc.AccessToken, nil, nil)
}

// do sends one request. A form body (url.Values) is form-encoded, anything
// else JSON. Errors never carry the request URL or a token.
func (c *Connector) do(ctx context.Context, method, path, token string, body any, out any) error {
	var rd *bytes.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case url.Values:
		rd, ctype = bytes.NewReader([]byte(b.Encode())), "application/x-www-form-urlencoded"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.APIBase+path, rd)
	if err != nil {
		return &crm.APIError{Msg: "building the request"}
	}
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &crm.APIError{Retry: true, Msg: "network error"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := safehttp.ReadBody(resp.Body, 1<<20)
	if resp.StatusCode/100 != 2 {
		return statusError(resp, raw, token != "")
	}
	if rerr != nil {
		return &crm.APIError{Status: resp.StatusCode, Retry: true, Msg: "reading the response"}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &crm.APIError{Status: resp.StatusCode, Msg: "unreadable response"}
		}
	}
	return nil
}

func statusError(resp *http.Response, raw []byte, authed bool) error {
	if resp.StatusCode == http.StatusUnauthorized && authed {
		return crm.ErrAuth
	}
	e := &crm.APIError{Status: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Retry = true
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.After = time.Duration(s) * time.Second
		}
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout:
		e.Retry = true
	}
	var m struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(raw, &m) == nil {
		e.Msg = strings.Join(strings.Fields(m.Message), " ")
		if e.Msg == "" {
			e.Msg = m.Status
		}
	}
	if len(e.Msg) > 200 {
		e.Msg = e.Msg[:200]
	}
	if e.Msg == "" {
		e.Msg = http.StatusText(resp.StatusCode)
	}
	return e
}

type filter struct {
	PropertyName string `json:"propertyName"`
	Operator     string `json:"operator"`
	Value        string `json:"value"`
}

type filterGroup struct {
	Filters []filter `json:"filters"`
}

// Lookup implements crm.Connector: contacts whose phone or mobile phone
// equals the number, or whose normalised phone ends with its last nine
// digits (verify against a real portal, see docs/integrations.md).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	digits := strings.TrimPrefix(n, "+")
	tail := digits
	if len(tail) > 9 {
		tail = tail[len(tail)-9:]
	}
	req := map[string]any{
		"filterGroups": []filterGroup{
			{Filters: []filter{{"phone", "EQ", n}}},
			{Filters: []filter{{"mobilephone", "EQ", n}}},
			{Filters: []filter{{"hs_searchable_calculated_phone_number", "CONTAINS_TOKEN", "*" + tail}}},
		},
		"properties": []string{"firstname", "lastname", "company"},
		"limit":      1,
	}
	var res struct {
		Results []struct {
			ID         string            `json:"id"`
			Properties map[string]string `json:"properties"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/crm/v3/objects/contacts/search", cc.AccessToken, req, &res); err != nil {
		return nil, err
	}
	if len(res.Results) == 0 {
		return nil, nil
	}
	r := res.Results[0]
	ct := &crm.Contact{ID: r.ID, Name: strings.TrimSpace(r.Properties["firstname"] + " " + r.Properties["lastname"]), Company: r.Properties["company"]}
	if cc.ExternalAcct != "" {
		host := "app.hubspot.com"
		if cc.Config.Extra["region"] == "eu" {
			host = "app-eu1.hubspot.com"
		}
		ct.URL = "https://" + host + "/contacts/" + url.PathEscape(cc.ExternalAcct) + "/record/0-1/" + url.PathEscape(r.ID)
	}
	return ct, nil
}

// ResolveUser implements crm.Connector: the owner whose email equals the
// Hello user's, compared exactly (case-insensitively).
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var res struct {
		Results []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/crm/v3/owners?limit=10&email="+url.QueryEscape(email), cc.AccessToken, nil, &res); err != nil {
		return "", err
	}
	for _, r := range res.Results {
		if strings.EqualFold(r.Email, email) {
			return r.ID, nil
		}
	}
	return "", nil
}

// status is the hs_call_status of an outcome.
func status(o crm.Outcome) string {
	switch o {
	case crm.OutcomeAnswered:
		return "COMPLETED"
	case crm.OutcomeBusy:
		return "BUSY"
	case crm.OutcomeCancelled:
		return "CANCELED"
	case crm.OutcomeFailed:
		return "FAILED"
	}
	return "NO_ANSWER"
}

// dispositionLabel is the label of HubSpot's built-in call outcomes.
func dispositionLabel(o crm.Outcome) string {
	switch o {
	case crm.OutcomeAnswered:
		return "Connected"
	case crm.OutcomeBusy:
		return "Busy"
	case crm.OutcomeNoAnswer:
		return "No answer"
	}
	return ""
}

// dispositionID maps an outcome to the portal's disposition id (HubSpot
// stores the outcome as a GUID). It is best effort: without a match the
// property is left out.
func (c *Connector) dispositionID(ctx context.Context, cc crm.Conn, o crm.Outcome) string {
	label := dispositionLabel(o)
	if label == "" {
		return ""
	}
	c.mu.Lock()
	fresh := c.dispo != nil && time.Since(c.dispoAt) < time.Hour
	c.mu.Unlock()
	if !fresh {
		var list []struct {
			ID      string `json:"id"`
			Label   string `json:"label"`
			Deleted bool   `json:"deleted"`
		}
		m := map[string]string{}
		if err := c.do(ctx, http.MethodGet, "/calling/v1/dispositions", cc.AccessToken, nil, &list); err == nil {
			for _, d := range list {
				if !d.Deleted {
					m[strings.ToLower(d.Label)] = d.ID
				}
			}
		}
		c.mu.Lock()
		c.dispo, c.dispoAt = m, time.Now()
		c.mu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dispo[strings.ToLower(label)]
}

// validRecordingURL is what hs_call_recording_url accepts: https and an
// .mp3 or .wav path.
func validRecordingURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".mp3") || strings.HasSuffix(p, ".wav")
}

func timestamp(l crm.CallLog) time.Time {
	for _, t := range []time.Time{l.StartedAt, l.AnsweredAt, l.EndedAt} {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// properties are the call's HubSpot properties. title and body are only
// written when the call is created, so a call a person edited in HubSpot
// keeps their notes.
func (c *Connector) properties(ctx context.Context, cc crm.Conn, l crm.CallLog, create bool) map[string]string {
	dir := "INBOUND"
	if l.Direction == "outbound" {
		dir = "OUTBOUND"
	}
	p := map[string]string{
		"hs_timestamp":        timestamp(l).Format("2006-01-02T15:04:05.000Z"),
		"hs_call_duration":    strconv.FormatInt(l.Duration.Milliseconds(), 10),
		"hs_call_from_number": l.From,
		"hs_call_to_number":   l.To,
		"hs_call_direction":   dir,
		"hs_call_status":      status(l.Outcome),
	}
	if create {
		p["hs_call_source"] = "INTEGRATIONS_PLATFORM"
		p["hs_call_title"] = title(l)
		p["hs_call_body"] = body(l)
	}
	if l.OwnerID != "" {
		p["hubspot_owner_id"] = l.OwnerID
	}
	if d := c.dispositionID(ctx, cc, l.Outcome); d != "" {
		p["hs_call_disposition"] = d
	}
	if l.RecordingURL != "" && validRecordingURL(l.RecordingURL) {
		p["hs_call_recording_url"] = l.RecordingURL
	}
	return p
}

func title(l crm.CallLog) string {
	who := l.Number
	if l.Contact != nil && l.Contact.Name != "" {
		who = l.Contact.Name
	}
	switch {
	case l.Missed:
		return "Missed call from " + who
	case l.Direction == "outbound":
		return "Call to " + who
	}
	return "Call from " + who
}

func body(l crm.CallLog) string {
	s := "Logged by Hello."
	if l.Missed {
		s = "Nobody answered. " + s
	}
	return s
}

// LogCall implements crm.Connector.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	req := map[string]any{"properties": c.properties(ctx, cc, l, true)}
	if l.Contact != nil && l.Contact.ID != "" {
		req["associations"] = []map[string]any{{
			"to":    map[string]string{"id": l.Contact.ID},
			"types": []map[string]any{{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": associationCallToContact}},
		}}
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/crm/v3/objects/calls", cc.AccessToken, req, &res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", &crm.APIError{Msg: "the response has no call id"}
	}
	return res.ID, nil
}

// UpdateCallLog implements crm.Connector.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	req := map[string]any{"properties": c.properties(ctx, cc, l, false)}
	return c.do(ctx, http.MethodPatch, "/crm/v3/objects/calls/"+url.PathEscape(ref), cc.AccessToken, req, nil)
}
