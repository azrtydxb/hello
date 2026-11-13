// Package salesforce is the Salesforce connector of internal/crm (spec
// integrations I-51).
//
// Salesforce has two halves. The interactive one is the Open CTI softphone
// Hello serves (web/public/integrations/salesforce): it runs inside the
// user's Lightning session, so it needs no Salesforce token. This package is
// the other half, the server's: with the customer's connected app (OAuth
// client-credentials flow, an integration user) Hello looks contacts and
// leads up by phone, writes calls as Tasks, patches them with the final
// duration and the recording link, and logs the calls nobody had a browser
// open for, missed calls included.
//
// Request shapes follow Salesforce's REST API reference; what needs a real
// org is listed in docs/crm.md ("Lab checklist").
package salesforce

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit"
)

// Kind is the connector kind.
const Kind = "salesforce"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// DefaultAPIVersion is the REST API version used when none is set.
const DefaultAPIVersion = "62.0"

// SoftphonePath is the page Hello serves for the Open CTI adapter URL.
const SoftphonePath = "/integrations/salesforce/softphone"

// sessionLifetime is how long Hello trusts a client-credentials token. The
// token response carries no expiry; the org's session timeout is at least
// 15 minutes, and a refused token is replaced at once anyway.
const sessionLifetime = 15 * time.Minute

var (
	domainRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.(salesforce\.com|force\.com|cloudforce\.com|salesforce-setup\.com)$`)
	versionRE = regexp.MustCompile(`^[0-9]{2}\.0$`)
	idRE      = regexp.MustCompile(`^[a-zA-Z0-9]{15,18}$`)
)

// Connector is the Salesforce connector.
type Connector struct {
	hc *http.Client
	// BaseURL maps a Salesforce host to the URL requests go to; tests point
	// it at an httptest server.
	BaseURL func(host string) string
}

// New returns the connector on hc (the SSRF-safe client).
func New(hc *http.Client) *Connector {
	return &Connector{hc: hc, BaseURL: func(h string) string { return "https://" + h }}
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. Click-to-call and the screen pop
// are the softphone's, which runs in the browser.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, ClickToCall: true, ScreenPop: true}
}

// ServerSide implements crm.ServerSide: without the server-side logging
// switch the connector is only the softphone's settings.
func (c *Connector) ServerSide(cfg map[string]string) bool { return cfg["serverLogging"] == "true" }

// ValidateConfig implements crm.ConfigValidator.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "domain", "apiVersion", "serverLogging":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	if out["serverLogging"] == "" {
		out["serverLogging"] = "false"
	}
	if out["serverLogging"] != "true" && out["serverLogging"] != "false" {
		return nil, errors.New("serverLogging must be true or false")
	}
	if out["apiVersion"] == "" {
		out["apiVersion"] = DefaultAPIVersion
	}
	if !versionRE.MatchString(out["apiVersion"]) {
		return nil, errors.New("apiVersion must look like 62.0")
	}
	out["domain"] = strings.ToLower(out["domain"])
	if d := out["domain"]; d != "" && !domainRE.MatchString(d) {
		return nil, errors.New("domain must be your My Domain host, for example acme.my.salesforce.com")
	}
	if out["serverLogging"] == "true" {
		if out["domain"] == "" {
			return nil, errors.New("domain is required for server-side logging")
		}
		if out["clientId"] == "" {
			return nil, errors.New("clientId is required for server-side logging")
		}
	}
	return out, nil
}

// Setup implements crm.SetupProvider: the Call Center definition to import,
// the adapter URL and the origin for CSP Trusted Sites.
func (c *Connector) Setup(cfg crm.Config, publicURL string) []crm.SetupItem {
	adapter := publicURL + SoftphonePath
	if cfg.Extra["serverLogging"] != "true" {
		// Nothing else logs a missed call, so the softphone does.
		adapter += "?missed=1"
	}
	return []crm.SetupItem{
		{Name: "callCenterXml", Label: "Call Center definition", Kind: "file", Filename: "HelloSoftphone.xml", Value: CallCenterXML(adapter),
			Hint: "Setup, Call Centers, Import. Then add yourself or a permission set to the call center."},
		{Name: "adapterUrl", Label: "CTI adapter URL", Kind: "url", Value: adapter,
			Hint: "Already inside the definition; shown for a manual call center."},
		{Name: "trustedSite", Label: "CSP Trusted Site", Kind: "url", Value: publicURL,
			Hint: "Setup, CSP Trusted Sites, New. Allow frame-src and connect-src."},
	}
}

// CallCenterXML is the Call Center definition file Salesforce imports for an
// Open CTI softphone at adapterURL.
func CallCenterXML(adapterURL string) string {
	var esc strings.Builder
	_ = xml.EscapeText(&esc, []byte(adapterURL))
	return `<?xml version="1.0" encoding="UTF-8"?>
<callCenter>
  <section sortOrder="0" name="reqGeneralInfo" label="General Information">
    <item sortOrder="0" name="reqInternalName" label="InternalNameLabel">HelloSoftphone</item>
    <item sortOrder="1" name="reqDisplayName" label="DisplayNameLabel">Hello</item>
    <item sortOrder="2" name="reqAdapterUrl" label="AdapterUrlLabel">` + esc.String() + `</item>
    <item sortOrder="3" name="reqUseApi" label="UseApiLabel">true</item>
    <item sortOrder="4" name="reqSoftphoneHeight" label="SoftphoneHeightLabel">420</item>
    <item sortOrder="5" name="reqSoftphoneWidth" label="SoftphoneWidthLabel">320</item>
  </section>
</callCenter>
`
}

func classify(status int, raw []byte) (string, bool) {
	// Errors are [{"message", "errorCode"}] on the REST API and
	// {"error", "error_description"} on the token endpoint.
	var arr []struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		// An org over its API limit answers 403 REQUEST_LIMIT_EXCEEDED: it
		// passes, unlike every other 403.
		return arr[0].ErrorCode + ": " + arr[0].Message, arr[0].ErrorCode == "REQUEST_LIMIT_EXCEEDED"
	}
	var obj struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Error != "" {
		return obj.Error + ": " + obj.Description, false
	}
	return "", false
}

// host returns the Salesforce host of a connection: the instance the token
// response named, else the configured My Domain.
func host(cc crm.Conn) string {
	if h := cc.ExternalAcct; domainRE.MatchString(h) {
		return h
	}
	return cc.Config.Extra["domain"]
}

func (c *Connector) api(cc crm.Conn, path string) string {
	v := cc.Config.Extra["apiVersion"]
	if v == "" {
		v = DefaultAPIVersion
	}
	return c.BaseURL(host(cc)) + "/services/data/v" + v + path
}

// Authenticate implements crm.Direct: the client-credentials grant of the
// customer's connected app.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	if cfg.ClientID == "" {
		return crm.Tokens{}, &crm.APIError{Status: 400, Msg: "the client id is not set"}
	}
	d := cfg.Extra["domain"]
	if !domainRE.MatchString(d) {
		return crm.Tokens{}, &crm.APIError{Status: 400, Msg: "the My Domain host is not set"}
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		InstanceURL string `json:"instance_url"`
		Scope       string `json:"scope"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}}
	if err := c.send(ctx, http.MethodPost, c.BaseURL(d)+"/services/oauth2/token", "", nil, form, &tr); err != nil {
		if errors.Is(err, crm.ErrAuth) {
			return crm.Tokens{}, &crm.APIError{Status: 401, Msg: "the connected app refused the client credentials"}
		}
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	life := sessionLifetime
	if tr.ExpiresIn > 0 {
		life = min(life, time.Duration(tr.ExpiresIn)*time.Second)
	}
	h := d
	if u, err := url.Parse(tr.InstanceURL); err == nil && u.Scheme == "https" && domainRE.MatchString(u.Hostname()) {
		h = u.Hostname()
	}
	return crm.Tokens{Access: tr.AccessToken, ExpiresAt: time.Now().Add(life), Scopes: strings.Fields(tr.Scope), Account: h}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.send(ctx, http.MethodGet, c.api(cc, "/"), cc.AccessToken, nil, nil, nil)
}

type record struct {
	Attributes struct {
		Type string `json:"type"`
	} `json:"attributes"`
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Company string `json:"Company"`
	Account *struct {
		Name string `json:"Name"`
	} `json:"Account"`
}

// Lookup implements crm.Connector: a SOSL search of the phone fields of
// contacts and leads for the last nine digits, contacts first.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	tail := strings.TrimPrefix(n, "+")
	if len(tail) > 9 {
		tail = tail[len(tail)-9:]
	}
	q := "FIND {*" + tail + "} IN PHONE FIELDS RETURNING " +
		"Contact(Id, Name, Account.Name ORDER BY LastModifiedDate DESC LIMIT 1), " +
		"Lead(Id, Name, Company WHERE IsConverted = false ORDER BY LastModifiedDate DESC LIMIT 1)"
	var res struct {
		SearchRecords []record `json:"searchRecords"`
	}
	if err := c.send(ctx, http.MethodGet, c.api(cc, "/search/?q="+url.QueryEscape(q)), cc.AccessToken, nil, nil, &res); err != nil {
		return nil, err
	}
	var pick *record
	for i := range res.SearchRecords {
		r := &res.SearchRecords[i]
		if r.ID == "" || (r.Attributes.Type != "Contact" && r.Attributes.Type != "Lead") {
			continue
		}
		if pick == nil || (r.Attributes.Type == "Contact" && pick.Attributes.Type != "Contact") {
			pick = r
		}
	}
	if pick == nil {
		return nil, nil
	}
	ct := &crm.Contact{ID: pick.ID, Name: pick.Name, Company: pick.Company}
	if pick.Account != nil {
		ct.Company = pick.Account.Name
	}
	ct.URL = "https://" + host(cc) + "/lightning/r/" + pick.Attributes.Type + "/" + url.PathEscape(pick.ID) + "/view"
	return ct, nil
}

// soqlString quotes s as a SOQL string literal.
func soqlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`, `"`, `\"`)
	return "'" + r.Replace(s) + "'"
}

// ResolveUser implements crm.Connector: the one active user whose email
// equals the Hello user's. Two users with the address are ambiguous, so no
// owner is better than the wrong one.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	q := "SELECT Id, Email FROM User WHERE Email = " + soqlString(email) + " AND IsActive = true LIMIT 2"
	var res struct {
		Records []struct {
			ID    string `json:"Id"`
			Email string `json:"Email"`
		} `json:"records"`
	}
	if err := c.send(ctx, http.MethodGet, c.api(cc, "/query/?q="+url.QueryEscape(q)), cc.AccessToken, nil, nil, &res); err != nil {
		return "", err
	}
	id := ""
	for _, r := range res.Records {
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

// SecretBoundKeys implements crm.SecretBound: the secret goes to the domain.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "domain"} }

// ValidRef implements crm.RefValidator: a Task id.
func (c *Connector) ValidRef(ref string) bool { return idRE.MatchString(ref) }

func disposition(o crm.Outcome) string {
	switch o {
	case crm.OutcomeAnswered:
		return "Connected"
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

func when(l crm.CallLog) time.Time {
	for _, t := range []time.Time{l.StartedAt, l.AnsweredAt, l.EndedAt} {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func description(l crm.CallLog) string {
	var b strings.Builder
	b.WriteString("Logged by Hello.\n")
	if l.From != "" || l.To != "" {
		fmt.Fprintf(&b, "From: %s\nTo: %s\n", l.From, l.To)
	}
	fmt.Fprintf(&b, "Outcome: %s\n", disposition(l.Outcome))
	return strings.TrimRight(b.String(), "\n")
}

// whoable reports whether a record id can be a Task's WhoId: a Contact
// (003) or a Lead (00Q).
func whoable(id string) bool { return strings.HasPrefix(id, "003") || strings.HasPrefix(id, "00Q") }

// LogCall implements crm.Connector: a completed Task of subtype Call.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	dir := "Inbound"
	if l.Direction == "outbound" {
		dir = "Outbound"
	}
	t := map[string]any{
		"Subject":               subject(l),
		"Status":                "Completed",
		"Priority":              "Normal",
		"TaskSubtype":           "Call",
		"CallType":              dir,
		"CallDurationInSeconds": int(l.Duration.Seconds()),
		"CallDisposition":       disposition(l.Outcome),
		"CallObject":            l.CallID,
		"ActivityDate":          when(l).Format("2006-01-02"),
		"Description":           crm.RecordingLine(description(l), l.RecordingURL),
	}
	if l.OwnerID != "" {
		t["OwnerId"] = l.OwnerID
	}
	if l.Contact != nil && whoable(l.Contact.ID) {
		t["WhoId"] = l.Contact.ID
	}
	var res struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	if err := c.send(ctx, http.MethodPost, c.api(cc, "/sobjects/Task"), cc.AccessToken, nil, t, &res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", &crm.APIError{Msg: "the response has no task id"}
	}
	return res.ID, nil
}

// UpdateCallLog implements crm.Connector. It rewrites what Hello knows best
// (duration, outcome, the recording line) and nothing a person edited.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if !idRE.MatchString(ref) {
		return &crm.APIError{Status: 400, Msg: "not a task id"}
	}
	path := "/sobjects/Task/" + url.PathEscape(ref)
	var cur struct {
		Description string `json:"Description"`
		WhoID       string `json:"WhoId"`
	}
	if err := c.send(ctx, http.MethodGet, c.api(cc, path+"?fields=Description,WhoId"), cc.AccessToken, nil, nil, &cur); err != nil {
		return err
	}
	p := map[string]any{
		"CallDurationInSeconds": int(l.Duration.Seconds()),
		"CallDisposition":       disposition(l.Outcome),
	}
	if l.RecordingURL != "" {
		p["Description"] = crm.RecordingLine(cur.Description, l.RecordingURL)
	}
	if cur.WhoID == "" && l.Contact != nil && whoable(l.Contact.ID) {
		p["WhoId"] = l.Contact.ID
	}
	return c.send(ctx, http.MethodPatch, c.api(cc, path), cc.AccessToken, nil, p, nil)
}

// send is one request through the shared kit: token, when set, is sent as a
// bearer token (and a 401 is then crm.ErrAuth); extra headers are added as
// given.
func (c *Connector) send(ctx context.Context, method, rawURL, token string, hdr map[string]string, body, out any) error {
	h := map[string]string{}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	for k, v := range hdr {
		h[k] = v
	}
	return kit.Do(ctx, c.hc, kit.Request{Method: method, URL: rawURL, Header: h, Body: body, Authed: token != "", Classify: classify}, out)
}
