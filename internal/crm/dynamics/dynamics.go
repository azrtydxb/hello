// Package dynamics is the Dynamics 365 connector of internal/crm (spec
// integrations I-47, P19).
//
// The interactive half is the Channel Integration Framework v2 widget Hello
// serves (web/public/integrations/dynamics): click-to-act dials through
// Hello, and an incoming call opens the caller's contact record. This
// package is the server's half. With an Entra application user (OAuth
// client-credentials grant) Hello looks contacts and leads up by phone,
// writes every call as a Phone Call activity, and attaches the recording
// link.
//
// Request shapes follow the Dataverse Web API reference; what needs a real
// environment is listed in docs/crm.md ("Lab checklist").
package dynamics

import (
	"context"
	"encoding/json"
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
const Kind = "dynamics"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// CIFPath is the page Hello serves as the channel provider's channel URL.
const CIFPath = "/integrations/dynamics/cif"

const apiPath = "/api/data/v9.2"

// Phone Call states and status reasons.
const (
	stateOpen, stateCompleted = 0, 1
	statusOpen                = 1
	statusMade, statusRecv    = 2, 4
)

var (
	tenantRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`)
	orgRE    = regexp.MustCompile(`^https://[a-z0-9][a-z0-9-]*(\.[a-z0-9-]+)*\.dynamics\.com$`)
)

// Connector is the Dynamics 365 connector.
type Connector struct {
	rest crm.REST
	// LoginURL is the Entra token host; Base maps an environment URL to
	// where requests go. Tests point both at an httptest server.
	LoginURL string
	Base     func(orgURL string) string
}

// New returns the connector on hc (the SSRF-safe client).
func New(hc *http.Client) *Connector {
	return &Connector{rest: crm.REST{HC: hc, Classify: classify}, LoginURL: "https://login.microsoftonline.com", Base: func(u string) string { return u }}
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. Click-to-act and the screen pop are
// the widget's, which runs in the browser.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, ClickToCall: true, ScreenPop: true}
}

// ValidateConfig implements crm.ConfigValidator.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "tenantId", "orgUrl":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	out["orgUrl"] = strings.ToLower(strings.TrimRight(out["orgUrl"], "/"))
	if !orgRE.MatchString(out["orgUrl"]) {
		return nil, errors.New("orgUrl must be your environment URL, for example https://contoso.crm4.dynamics.com")
	}
	if !tenantRE.MatchString(out["tenantId"]) {
		return nil, errors.New("tenantId must be the directory (tenant) id, a GUID")
	}
	return out, nil
}

// Setup implements crm.SetupProvider: the values of the CIF channel
// provider record.
func (c *Connector) Setup(cfg crm.Config, publicURL string) []crm.SetupItem {
	// The page loads the CIF library from the environment, which it learns
	// from the channel URL.
	channel := publicURL + CIFPath + "?org=" + url.QueryEscape(cfg.Extra["orgUrl"])
	return []crm.SetupItem{
		{Name: "name", Label: "Name", Kind: "text", Value: "Hello"},
		{Name: "channelUrl", Label: "Channel URL", Kind: "url", Value: channel,
			Hint: "Customer Service admin center, Channel Providers, New."},
		{Name: "trustedDomain", Label: "Trusted domain", Kind: "url", Value: publicURL},
		{Name: "apiVersion", Label: "Channel provider API version", Kind: "text", Value: "2.0"},
		{Name: "outbound", Label: "Enable outbound communication", Kind: "text", Value: "Yes",
			Hint: "Needed for click-to-act on phone numbers."},
		{Name: "appProfile", Label: "App profile", Kind: "text", Value: "Add the provider to the app profile of the apps whose users dial through Hello."},
	}
}

func classify(_ int, raw []byte) (string, bool) {
	// Dataverse: {"error": {"code", "message"}}; Entra: {"error",
	// "error_description"} whose description carries trace ids on later lines.
	var dv struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &dv) == nil && dv.Error.Message != "" {
		// Dataverse reports its service protection limits as 429; nothing
		// else needs a retry beyond the status.
		return dv.Error.Message, false
	}
	var aad struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(raw, &aad) == nil && aad.Error != "" {
		first, _, _ := strings.Cut(aad.Description, "\n")
		return aad.Error + ": " + first, false
	}
	return "", false
}

// Authenticate implements crm.Direct: the client-credentials grant of the
// application user, scoped to the environment.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	org, tenant := cfg.Extra["orgUrl"], cfg.Extra["tenantId"]
	if !orgRE.MatchString(org) || !tenantRE.MatchString(tenant) {
		return crm.Tokens{}, &crm.APIError{Status: 400, Msg: "the environment URL or tenant id is not set"}
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {cfg.ClientID},
		"client_secret": {cfg.ClientSecret}, "scope": {org + "/.default"}}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.rest.Do(ctx, http.MethodPost, c.LoginURL+"/"+url.PathEscape(tenant)+"/oauth2/v2.0/token", "", nil, form, &tr); err != nil {
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	life := time.Duration(tr.ExpiresIn) * time.Second
	if life <= 0 {
		life = 30 * time.Minute
	}
	return crm.Tokens{Access: tr.AccessToken, ExpiresAt: time.Now().Add(life), Scopes: []string{org + "/.default"}, Account: strings.TrimPrefix(org, "https://")}, nil
}

var odata = map[string]string{"OData-MaxVersion": "4.0", "OData-Version": "4.0"}

func (c *Connector) url(cc crm.Conn, path string) string {
	return c.Base(cc.Config.Extra["orgUrl"]) + apiPath + path
}

func (c *Connector) do(ctx context.Context, cc crm.Conn, method, path string, hdr map[string]string, body, out any) error {
	h := map[string]string{}
	for k, v := range odata {
		h[k] = v
	}
	for k, v := range hdr {
		h[k] = v
	}
	return c.rest.Do(ctx, method, c.url(cc, path), cc.AccessToken, h, body, out)
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.do(ctx, cc, http.MethodGet, "/WhoAmI", nil, nil, nil)
}

// odataString quotes s as an OData string literal.
func odataString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Lookup implements crm.Connector: contacts, then leads, whose phone fields
// contain the last nine digits of the number. Dataverse keeps phone numbers
// as typed, so a number stored with spaces is not found (docs/crm.md).
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	tail := strings.TrimPrefix(n, "+")
	if len(tail) > 9 {
		tail = tail[len(tail)-9:]
	}
	q := odataString(tail)
	for _, e := range []struct{ set, id, name, company, etn string }{
		{"contacts", "contactid", "fullname", "", "contact"},
		{"leads", "leadid", "fullname", "companyname", "lead"},
	} {
		sel := e.id + "," + e.name
		if e.company != "" {
			sel += "," + e.company
		}
		filter := "contains(telephone1," + q + ") or contains(mobilephone," + q + ")"
		if e.set == "contacts" {
			filter += " or contains(telephone2," + q + ")"
		}
		path := "/" + e.set + "?$select=" + sel + "&$filter=" + url.QueryEscape(filter) + "&$orderby=modifiedon%20desc&$top=1"
		var res struct {
			Value []map[string]any `json:"value"`
		}
		if err := c.do(ctx, cc, http.MethodGet, path, nil, nil, &res); err != nil {
			return nil, err
		}
		if len(res.Value) == 0 {
			continue
		}
		r := res.Value[0]
		str := func(k string) string { s, _ := r[k].(string); return s }
		id := str(e.id)
		if id == "" {
			continue
		}
		ct := &crm.Contact{ID: id, Name: str(e.name), Company: str(e.company)}
		ct.URL = cc.Config.Extra["orgUrl"] + "/main.aspx?etn=" + e.etn + "&pagetype=entityrecord&id=" + url.QueryEscape(id)
		return ct, nil
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the one enabled system user whose
// primary email equals the Hello user's.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	filter := "internalemailaddress eq " + odataString(email) + " and isdisabled eq false"
	path := "/systemusers?$select=systemuserid,internalemailaddress&$filter=" + url.QueryEscape(filter) + "&$top=2"
	var res struct {
		Value []struct {
			ID    string `json:"systemuserid"`
			Email string `json:"internalemailaddress"`
		} `json:"value"`
	}
	if err := c.do(ctx, cc, http.MethodGet, path, nil, nil, &res); err != nil {
		return "", err
	}
	id := ""
	for _, r := range res.Value {
		if !strings.EqualFold(r.Email, email) {
			continue
		}
		if id != "" {
			return "", nil
		}
		id = r.ID
	}
	return id, nil
}

// SecretBoundKeys implements crm.SecretBound: the secret goes to the tenant's
// token endpoint for the environment.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "tenantId", "orgUrl"} }

var guidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`)

// ValidRef implements crm.RefValidator: an activity id.
func (c *Connector) ValidRef(ref string) bool { return guidRE.MatchString(ref) }

func outcomeText(o crm.Outcome) string {
	switch o {
	case crm.OutcomeAnswered:
		return "Answered"
	case crm.OutcomeBusy:
		return "Busy"
	case crm.OutcomeCancelled:
		return "Cancelled"
	case crm.OutcomeFailed:
		return "Failed"
	}
	return "No answer"
}

func subject(l crm.CallLog) string {
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

func description(l crm.CallLog) string {
	s := "Logged by Hello.\n"
	if l.From != "" || l.To != "" {
		s += fmt.Sprintf("From: %s\nTo: %s\n", l.From, l.To)
	}
	return s + "Outcome: " + outcomeText(l.Outcome)
}

// minutes is the duration Dataverse stores: whole minutes, at least one for
// a call that was talked on.
func minutes(d time.Duration) int {
	m := int((d + 59*time.Second) / time.Minute)
	if d <= 0 {
		return 0
	}
	return max(m, 1)
}

func isLead(c *crm.Contact) bool {
	return c != nil && strings.Contains(c.URL, "etn=lead")
}

func party(set, field, id string, mask int) map[string]any {
	return map[string]any{"partyid_" + field + "@odata.bind": "/" + set + "(" + id + ")", "participationtypemask": mask}
}

// LogCall implements crm.Connector: a Phone Call activity, closed as Made
// (outbound) or Received (inbound). A missed call is a Received call whose
// subject and description say nobody answered.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	outbound := l.Direction == "outbound"
	start, end := l.StartedAt, l.EndedAt
	if start.IsZero() {
		start = l.AnsweredAt
	}
	p := map[string]any{
		"subject":               subject(l),
		"phonenumber":           l.Number,
		"directioncode":         outbound,
		"description":           crm.RecordingLine(description(l), l.RecordingURL),
		"actualdurationminutes": minutes(l.Duration),
	}
	if !start.IsZero() {
		p["actualstart"] = start.UTC().Format(time.RFC3339)
	}
	if !end.IsZero() {
		p["actualend"] = end.UTC().Format(time.RFC3339)
	}
	var parties []map[string]any
	var user, other map[string]any
	if l.OwnerID != "" {
		p["ownerid@odata.bind"] = "/systemusers(" + l.OwnerID + ")"
	}
	if l.Contact != nil && guidRE.MatchString(l.Contact.ID) {
		set, field := "contacts", "contact"
		if isLead(l.Contact) {
			set, field = "leads", "lead"
		}
		p["regardingobjectid_"+field+"_phonecall@odata.bind"] = "/" + set + "(" + l.Contact.ID + ")"
		// Sender 1, recipient 2.
		if outbound {
			other = party(set, field, l.Contact.ID, 2)
		} else {
			other = party(set, field, l.Contact.ID, 1)
		}
	}
	if l.OwnerID != "" {
		if outbound {
			user = party("systemusers", "systemuser", l.OwnerID, 1)
		} else {
			user = party("systemusers", "systemuser", l.OwnerID, 2)
		}
	}
	// The sender first, then the recipient.
	for _, x := range []map[string]any{user, other} {
		if x != nil {
			parties = append(parties, x)
		}
	}
	if len(parties) == 2 && parties[0]["participationtypemask"] == 2 {
		parties[0], parties[1] = parties[1], parties[0]
	}
	if len(parties) > 0 {
		p["phonecall_activity_parties"] = parties
	}
	var res struct {
		ID string `json:"activityid"`
	}
	hdr := map[string]string{"Prefer": "return=representation"}
	if err := c.do(ctx, cc, http.MethodPost, "/phonecalls?$select=activityid", hdr, p, &res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", &crm.APIError{Msg: "the response has no activity id"}
	}
	// Close it. The record exists now, so a failure here must not make the
	// worker create it again: it is tried twice and the next update closes
	// it if both fail.
	if err := c.setState(ctx, cc, res.ID, true, outbound); err != nil {
		_ = c.setState(ctx, cc, res.ID, true, outbound)
	}
	return res.ID, nil
}

func (c *Connector) setState(ctx context.Context, cc crm.Conn, id string, closed, outbound bool) error {
	b := map[string]any{"statecode": stateOpen, "statuscode": statusOpen}
	if closed {
		code := statusRecv
		if outbound {
			code = statusMade
		}
		b = map[string]any{"statecode": stateCompleted, "statuscode": code}
	}
	return c.do(ctx, cc, http.MethodPatch, "/phonecalls("+id+")", nil, b, nil)
}

// UpdateCallLog implements crm.Connector. A closed activity is read-only in
// Dynamics, so it is reopened, patched and closed again. Only what Hello
// knows best is rewritten.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if !guidRE.MatchString(ref) {
		return &crm.APIError{Status: 400, Msg: "not an activity id"}
	}
	var cur struct {
		State       int    `json:"statecode"`
		Description string `json:"description"`
		Direction   bool   `json:"directioncode"`
		Regarding   string `json:"_regardingobjectid_value"`
	}
	if err := c.do(ctx, cc, http.MethodGet, "/phonecalls("+ref+")?$select=statecode,description,directioncode,_regardingobjectid_value", nil, nil, &cur); err != nil {
		return err
	}
	p := map[string]any{"actualdurationminutes": minutes(l.Duration)}
	if l.RecordingURL != "" {
		p["description"] = crm.RecordingLine(cur.Description, l.RecordingURL)
	}
	if cur.Regarding == "" && l.Contact != nil && guidRE.MatchString(l.Contact.ID) {
		if isLead(l.Contact) {
			p["regardingobjectid_lead_phonecall@odata.bind"] = "/leads(" + l.Contact.ID + ")"
		} else {
			p["regardingobjectid_contact_phonecall@odata.bind"] = "/contacts(" + l.Contact.ID + ")"
		}
	}
	if cur.State != stateOpen {
		if err := c.setState(ctx, cc, ref, false, cur.Direction); err != nil {
			return err
		}
	}
	patchErr := c.do(ctx, cc, http.MethodPatch, "/phonecalls("+ref+")", nil, p, nil)
	// Close it again even when the patch failed, so a retry finds the same
	// state.
	closeErr := c.setState(ctx, cc, ref, true, cur.Direction)
	if patchErr != nil {
		return patchErr
	}
	return closeErr
}
