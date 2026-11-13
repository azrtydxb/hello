// Package pipedrive is the Pipedrive connector of internal/crm (spec
// integrations P14): person search by phone, calls written as call logs, the
// recording uploaded as an audio file (Pipedrive takes a file, not a link),
// users by email, and the OAuth flow of a customer-owned app.
//
// Facts marked "verify" in docs/crm.md need a real Pipedrive account; the
// request shapes follow Pipedrive's published v1 API reference.
package pipedrive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/azrtydxb/hello/internal/crm/kit"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

// Kind is the connector kind.
const Kind = "pipedrive"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// MaxUploadBytes bounds a recording uploaded to Pipedrive (hello-control sets
// it from HELLO_CRM_UPLOAD_MAX_MB, 50 MB by default). A larger recording is
// not sent and the call stays logged without it.
var MaxUploadBytes int64 = 50 << 20

// Connector is the Pipedrive connector.
type Connector struct {
	hc *http.Client
	// OAuthBase is the host of the authorization and token endpoints.
	OAuthBase string
	// APIBase turns the company's api_domain (kept as the connection's
	// account) into the REST base. The default accepts only an https
	// *.pipedrive.com host, so a token is never sent anywhere else; tests
	// replace it.
	APIBase func(account string) (string, error)
}

// New returns the connector on hc.
func New(hc *http.Client) *Connector {
	return &Connector{hc: hc, OAuthBase: "https://oauth.pipedrive.com", APIBase: apiBase}
}

func apiBase(account string) (string, error) {
	u, err := url.Parse(account)
	host := strings.ToLower(u.Hostname())
	if err != nil || u.Scheme != "https" || u.Port() != "" || (host != "pipedrive.com" && !strings.HasSuffix(host, ".pipedrive.com")) {
		return "", &crm.APIError{Msg: "the Pipedrive account address is not a pipedrive.com address; connect again"}
	}
	return "https://" + host, nil
}

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. Pipedrive has no call-event
// surface for a server, so there is no screen-pop or click-to-call here: the
// browser extension covers tel: links.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true, RecordingUpload: true}
}

// ValidateConfig implements crm.ConfigValidator: only the client id.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		if k != "clientId" {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		out[k] = v
	}
	return out, nil
}

// AuthURL implements crm.OAuth. The scopes are those of the customer's app
// (set in Pipedrive's developer hub), not a request parameter.
func (c *Connector) AuthURL(cfg crm.Config, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return c.OAuthBase + "/oauth/authorize?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	APIDomain    string `json:"api_domain"`
}

func basic(cfg crm.Config) map[string]string {
	r, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	r.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	return map[string]string{"Authorization": r.Header.Get("Authorization")}
}

func (c *Connector) token(ctx context.Context, cfg crm.Config, form url.Values) (crm.Tokens, error) {
	var tr tokenResponse
	if err := kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost, URL: c.OAuthBase + "/oauth/token", Header: basic(cfg), Body: form}, &tr); err != nil {
		return crm.Tokens{}, err
	}
	if tr.AccessToken == "" {
		return crm.Tokens{}, &crm.APIError{Msg: "the token response has no access token"}
	}
	return crm.Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		Scopes: strings.Fields(tr.Scope), Account: tr.APIDomain}, nil
}

// Exchange implements crm.OAuth.
func (c *Connector) Exchange(ctx context.Context, cfg crm.Config, redirectURI, code string) (crm.Tokens, error) {
	return c.token(ctx, cfg, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}})
}

// Refresh implements crm.OAuth. A refresh token Pipedrive rejects (400 or
// 401) is a permanent failure. Pipedrive rotates the refresh token on every
// refresh; the framework stores the new one.
func (c *Connector) Refresh(ctx context.Context, cfg crm.Config, refresh string) (crm.Tokens, error) {
	t, err := c.token(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	var ae *crm.APIError
	if errors.As(err, &ae) && (ae.Status == http.StatusBadRequest || ae.Status == http.StatusUnauthorized) {
		ae.Retry = false
	}
	return t, err
}

// RevokeToken implements crm.Revoker.
func (c *Connector) RevokeToken(ctx context.Context, cfg crm.Config, refresh string) error {
	return kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost, URL: c.OAuthBase + "/oauth/revoke", Header: basic(cfg),
		Body: url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}}}, nil)
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.api(ctx, cc, http.MethodGet, "/api/v1/users/me", nil, nil)
}

func (c *Connector) api(ctx context.Context, cc crm.Conn, method, path string, body, out any) error {
	base, err := c.APIBase(cc.ExternalAcct)
	if err != nil {
		return err
	}
	return kit.Do(ctx, c.hc, kit.Request{Method: method, URL: base + path, Body: body, Authed: true,
		Header: map[string]string{"Authorization": "Bearer " + cc.AccessToken}}, out)
}

// Lookup implements crm.Connector: persons whose phone ends in the same
// digits. The search term is the full number's digits, then its last nine
// (verify against a real account how Pipedrive matches phone formats); every
// hit is checked against the numbers it returns, so a loose search never
// names the wrong person.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	digits := strings.TrimPrefix(n, "+")
	terms := []string{digits}
	if t := tail9(n); t != digits {
		terms = append(terms, t)
	}
	for _, term := range terms {
		var res struct {
			Data struct {
				Items []struct {
					Item struct {
						ID           int64 `json:"id"`
						Name         string
						Phones       []string `json:"phones"`
						Organization *struct {
							ID   int64  `json:"id"`
							Name string `json:"name"`
						} `json:"organization"`
					} `json:"item"`
				} `json:"items"`
			} `json:"data"`
		}
		q := url.Values{"term": {term}, "fields": {"phone"}, "limit": {"10"}}
		if err := c.api(ctx, cc, http.MethodGet, "/api/v1/persons/search?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Data.Items {
			for _, ph := range it.Item.Phones {
				if !kit.Same(ph, n) {
					continue
				}
				ct := &crm.Contact{ID: strconv.FormatInt(it.Item.ID, 10), Name: strings.TrimSpace(it.Item.Name)}
				if o := it.Item.Organization; o != nil {
					ct.Company, ct.CompanyID = o.Name, strconv.FormatInt(o.ID, 10)
				}
				if base, err := c.APIBase(cc.ExternalAcct); err == nil {
					ct.URL = base + "/person/" + ct.ID
				}
				return ct, nil
			}
		}
	}
	return nil, nil
}

// ResolveUser implements crm.Connector: the user whose email equals the
// Hello user's, compared exactly (case-insensitively).
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var res struct {
		Data []struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
		} `json:"data"`
	}
	q := url.Values{"term": {email}, "search_by_email": {"1"}}
	if err := c.api(ctx, cc, http.MethodGet, "/api/v1/users/find?"+q.Encode(), nil, &res); err != nil {
		return "", err
	}
	for _, u := range res.Data {
		if strings.EqualFold(u.Email, email) {
			return strconv.FormatInt(u.ID, 10), nil
		}
	}
	return "", nil
}

// outcome is the call log outcome Pipedrive knows.
func outcome(o crm.Outcome) string {
	switch o {
	case crm.OutcomeAnswered:
		return "connected"
	case crm.OutcomeBusy:
		return "busy"
	}
	return "no_answer"
}

const timeFormat = "2006-01-02 15:04:05"

// LogCall implements crm.Connector: POST /api/v1/callLogs. The recording
// is uploaded afterwards by UpdateCallLog.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	start := kit.Timestamp(l)
	end := l.EndedAt.UTC()
	if end.Before(start) {
		end = start.Add(l.Duration)
	}
	body := map[string]any{
		"outcome":           outcome(l.Outcome),
		"to_phone_number":   l.To,
		"from_phone_number": l.From,
		"start_time":        start.Format(timeFormat),
		"end_time":          end.Format(timeFormat),
		"duration":          strconv.FormatInt(int64(l.Duration.Seconds()), 10),
		"subject":           kit.Title(l),
		"note":              note(l),
	}
	if id, err := strconv.ParseInt(l.OwnerID, 10, 64); err == nil {
		body["user_id"] = id
	}
	if l.Contact != nil {
		if id, err := strconv.ParseInt(l.Contact.ID, 10, 64); err == nil {
			body["person_id"] = id
		}
		if id, err := strconv.ParseInt(l.Contact.CompanyID, 10, 64); err == nil {
			body["org_id"] = id
		}
	}
	var res struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.api(ctx, cc, http.MethodPost, "/api/v1/callLogs", body, &res); err != nil {
		return "", err
	}
	if res.Data.ID == "" {
		return "", &crm.APIError{Msg: "the response has no call log id"}
	}
	return res.Data.ID, nil
}

func note(l crm.CallLog) string {
	if l.Missed {
		return "Nobody answered. Logged by Hello."
	}
	return "Logged by Hello."
}

// UpdateCallLog implements crm.Connector. Pipedrive cannot edit a call log,
// so the only update is the recording upload (once: the worker calls it for
// a recording that arrived, never for a renewal).
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.Recording == nil {
		return nil
	}
	f := l.Recording
	if f.Size > MaxUploadBytes {
		return &crm.SkipError{Reason: fmt.Sprintf("the recording is larger than the %d MB upload limit", MaxUploadBytes>>20)}
	}
	var data bytes.Buffer
	n, err := data.ReadFrom(io.LimitReader(f.Body, MaxUploadBytes+1))
	if err != nil {
		return &crm.APIError{Retry: true, Msg: "reading the recording"}
	}
	if n > MaxUploadBytes {
		return &crm.SkipError{Reason: fmt.Sprintf("the recording is larger than the %d MB upload limit", MaxUploadBytes>>20)}
	}
	var mp bytes.Buffer
	w := multipart.NewWriter(&mp)
	name := f.Name
	if name == "" {
		name = "recording.wav"
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	ct := f.ContentType
	if ct == "" {
		ct = "audio/wav"
	}
	h.Set("Content-Type", ct)
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, _ = part.Write(data.Bytes())
	_ = w.Close()
	base, err := c.APIBase(cc.ExternalAcct)
	if err != nil {
		return err
	}
	return kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost, URL: base + "/api/v1/callLogs/" + url.PathEscape(ref) + "/recordings",
		Body: mp.Bytes(), ContentType: w.FormDataContentType(), Authed: true,
		Header: map[string]string{"Authorization": "Bearer " + cc.AccessToken}}, nil)
}

// tail9 is the last nine digits of a number.
func tail9(n string) string {
	d := kit.Digits(n)
	if len(d) > 9 {
		d = d[len(d)-9:]
	}
	return d
}
