// Package odoo is the Odoo connector of internal/crm (spec integrations
// P21): partner search by phone, calls written as notes in the chatter of
// the partner (or, for a caller Odoo does not know, of the user who owns the
// call), users by email, and an Odoo API key as the credential.
//
// It speaks Odoo's external JSON-RPC API (/jsonrpc, execute_kw), available in
// Odoo 14 and later. Odoo has no call model every edition shares (crm.phonecall
// was removed in 17 and the VoIP call model is Enterprise only), so a call is a
// log note, as the spec's "chatter/log" says. Facts marked "verify" in
// docs/crm.md need a real Odoo instance.
package odoo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/azrtydxb/hello/internal/crm/kit"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

// Kind is the connector kind.
const Kind = "odoo"

func init() { crm.Register(Kind, func(hc *http.Client) crm.Connector { return New(hc) }) }

// Connector is the Odoo connector.
type Connector struct{ hc *http.Client }

// New returns the connector on hc.
func New(hc *http.Client) *Connector { return &Connector{hc: hc} }

// Kind implements crm.Connector.
func (c *Connector) Kind() string { return Kind }

// Capabilities implements crm.Connector. Odoo's VoIP app dials over SIP by
// itself; Hello has no screen-pop surface there.
func (c *Connector) Capabilities() crm.Capabilities {
	return crm.Capabilities{Lookup: true, LogCalls: true, Recordings: true}
}

// ValidateConfig implements crm.ConfigValidator. url is the address of the
// Odoo server, db its database; clientId is the login of the user the API
// key belongs to.
func (c *Connector) ValidateConfig(cfg map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		switch k {
		case "clientId", "url", "db":
			out[k] = v
		default:
			return nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	if len(out["db"]) > 100 {
		return nil, errors.New("db must be at most 100 characters")
	}
	if raw := out["url"]; raw != "" {
		u, err := url.Parse(raw)
		switch {
		case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "":
			return nil, errors.New("url must be the http(s) address of the Odoo server")
		case u.User != nil || u.RawQuery != "" || u.Fragment != "":
			return nil, errors.New("url must not carry credentials, a query or a fragment")
		}
		out["url"] = strings.TrimRight(raw, "/")
	}
	return out, nil
}

// SecretBoundKeys implements crm.SecretBound: the API key is sent to the
// server in url, for the database and user named here.
func (c *Connector) SecretBoundKeys() []string { return []string{"clientId", "url", "db"} }

// fault is an Odoo JSON-RPC error.
type fault struct {
	Message string `json:"message"`
	Data    struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"data"`
}

// rpc calls one Odoo service method. Odoo answers every call with HTTP 200;
// a failure is the error member. An AccessDenied means the login or API key
// is wrong (ErrAuth: the framework marks the connection needs_reauth); any
// other fault is permanent, since the same call would fail the same way.
func (c *Connector) rpc(ctx context.Context, cfg crm.Config, service, method string, args []any, out any) error {
	base := cfg.Extra["url"]
	if base == "" {
		return &crm.APIError{Msg: "the Odoo server address is not set"}
	}
	req := map[string]any{"jsonrpc": "2.0", "method": "call", "id": 1,
		"params": map[string]any{"service": service, "method": method, "args": args}}
	var res struct {
		Result json.RawMessage `json:"result"`
		Error  *fault          `json:"error"`
	}
	if err := kit.Do(ctx, c.hc, kit.Request{Method: http.MethodPost, URL: base + "/jsonrpc", Body: req}, &res); err != nil {
		return err
	}
	if res.Error != nil {
		name := res.Error.Data.Name
		msg := strings.Join(strings.Fields(res.Error.Data.Message), " ")
		if msg == "" {
			msg = strings.Join(strings.Fields(res.Error.Message), " ")
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if strings.HasSuffix(name, "AccessDenied") {
			return crm.ErrAuth
		}
		return &crm.APIError{Status: http.StatusBadRequest, Msg: msg}
	}
	if out != nil {
		if err := json.Unmarshal(res.Result, out); err != nil {
			return &crm.APIError{Msg: "unreadable response"}
		}
	}
	return nil
}

// call runs execute_kw on a model with the connection's credentials.
func (c *Connector) call(ctx context.Context, cc crm.Conn, model, method string, args []any, kwargs map[string]any, out any) error {
	uid, err := strconv.ParseInt(cc.ExternalAcct, 10, 64)
	if err != nil {
		return crm.ErrAuth
	}
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	return c.rpc(ctx, cc.Config, "object", "execute_kw",
		[]any{cc.Config.Extra["db"], uid, cc.AccessToken, model, method, args, kwargs}, out)
}

// Authenticate implements crm.Direct: the login and API key must give a
// user id.
func (c *Connector) Authenticate(ctx context.Context, cfg crm.Config) (crm.Tokens, error) {
	switch {
	case cfg.Extra["url"] == "":
		return crm.Tokens{}, errors.New("set the Odoo server address first")
	case cfg.Extra["db"] == "":
		return crm.Tokens{}, errors.New("set the Odoo database first")
	case cfg.ClientID == "":
		return crm.Tokens{}, errors.New("set the login of the API key's user first")
	}
	var uid any
	err := c.rpc(ctx, cfg, "common", "authenticate", []any{cfg.Extra["db"], cfg.ClientID, cfg.ClientSecret, map[string]any{}}, &uid)
	if errors.Is(err, crm.ErrAuth) {
		return crm.Tokens{}, errors.New("Odoo refused the login or API key")
	}
	if err != nil {
		return crm.Tokens{}, err
	}
	f, ok := uid.(float64)
	if !ok || f < 1 {
		return crm.Tokens{}, errors.New("Odoo refused the login or API key")
	}
	return crm.Tokens{Access: cfg.ClientSecret, Account: strconv.FormatInt(int64(f), 10)}, nil
}

// Ping implements crm.Pinger.
func (c *Connector) Ping(ctx context.Context, cc crm.Conn) error {
	return c.call(ctx, cc, "res.users", "search_count", []any{[]any{}}, nil, nil)
}

type partner struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Phone    any    `json:"phone"`
	ParentID any    `json:"parent_id"`
}

// pair reads an Odoo many2one value, [id, name], or false for none.
func pair(v any) (id int64, name string) {
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return 0, ""
	}
	f, _ := l[0].(float64)
	if len(l) > 1 {
		name, _ = l[1].(string)
	}
	return int64(f), name
}

func (p partner) phone() string {
	s, _ := p.Phone.(string)
	return s
}

// Lookup implements crm.Connector: partners whose phone or mobile matches
// the number's last nine digits. Odoo's phone_mobile_search field ignores
// formatting; an Odoo without it falls back to a search on the last three
// digits, filtered here by the full number.
func (c *Connector) Lookup(ctx context.Context, cc crm.Conn, number string) (*crm.Contact, error) {
	n, ok := crm.NormalizeNumber(number)
	if !ok {
		return nil, nil
	}
	tail := tail9(n)
	var ps []partner
	kw := map[string]any{"fields": []string{"name", "phone", "parent_id"}, "limit": 5}
	err := c.call(ctx, cc, "res.partner", "search_read", []any{[]any{[]any{"phone_mobile_search", "ilike", tail}}}, kw, &ps)
	var ae *crm.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
		// No phone_mobile_search: search the raw phone by its ending.
		kw["limit"] = 80
		err = c.call(ctx, cc, "res.partner", "search_read",
			[]any{[]any{[]any{"phone", "ilike", tail[max(0, len(tail)-3):]}}}, kw, &ps)
		kept := ps[:0]
		for _, p := range ps {
			if kit.Same(p.phone(), n) {
				kept = append(kept, p)
			}
		}
		ps = kept
	}
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, nil
	}
	p := ps[0]
	ct := &crm.Contact{ID: strconv.FormatInt(p.ID, 10), Name: strings.TrimSpace(p.Name)}
	if id, name := pair(p.ParentID); id != 0 {
		ct.CompanyID, ct.Company = strconv.FormatInt(id, 10), name
	}
	ct.URL = cc.Config.Extra["url"] + "/web#id=" + ct.ID + "&model=res.partner&view_type=form"
	return ct, nil
}

// ResolveUser implements crm.Connector: the user whose login or email
// equals the Hello user's. The search is a loose ilike; the answer is
// compared exactly here.
func (c *Connector) ResolveUser(ctx context.Context, cc crm.Conn, email string) (string, error) {
	var us []struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Email any    `json:"email"`
	}
	domain := []any{"|", []any{"login", "ilike", email}, []any{"email", "ilike", email}}
	kw := map[string]any{"fields": []string{"login", "email"}, "limit": 20}
	if err := c.call(ctx, cc, "res.users", "search_read", []any{domain}, kw, &us); err != nil {
		return "", err
	}
	for _, u := range us {
		if e, _ := u.Email.(string); strings.EqualFold(u.Login, email) || strings.EqualFold(e, email) {
			return strconv.FormatInt(u.ID, 10), nil
		}
	}
	return "", nil
}

func outcomeText(l crm.CallLog) string {
	switch l.Outcome {
	case crm.OutcomeAnswered:
		return "answered"
	case crm.OutcomeBusy:
		return "busy"
	case crm.OutcomeCancelled:
		return "cancelled"
	case crm.OutcomeFailed:
		return "failed"
	}
	return "not answered"
}

// note is the chatter note, HTML with every value escaped.
func note(l crm.CallLog, owner string) string {
	var b strings.Builder
	b.WriteString("<p><b>" + html.EscapeString(kit.Title(l)) + "</b></p><ul>")
	row := func(k, v string) {
		if v != "" {
			b.WriteString("<li>" + html.EscapeString(k) + ": " + html.EscapeString(v) + "</li>")
		}
	}
	row("Direction", l.Direction)
	row("From", l.From)
	row("To", l.To)
	row("Started", kit.Timestamp(l).Format("2006-01-02 15:04:05 UTC"))
	row("Duration", l.Duration.Round(time.Second).String())
	row("Result", outcomeText(l))
	row("Handled by", owner)
	if l.RecordingURL != "" {
		b.WriteString(`<li>Recording: <a href="` + html.EscapeString(l.RecordingURL) + `">` + html.EscapeString(l.RecordingURL) + `</a></li>`)
	}
	b.WriteString("</ul><p>Logged by Hello.</p>")
	return b.String()
}

// owner reads the call owner's name and partner.
func (c *Connector) owner(ctx context.Context, cc crm.Conn, id string) (name string, partnerID int64, err error) {
	uid, perr := strconv.ParseInt(id, 10, 64)
	if perr != nil {
		return "", 0, nil
	}
	var us []struct {
		Name      string `json:"name"`
		PartnerID any    `json:"partner_id"`
	}
	if err := c.call(ctx, cc, "res.users", "read", []any{[]int64{uid}}, map[string]any{"fields": []string{"name", "partner_id"}}, &us); err != nil {
		return "", 0, err
	}
	if len(us) == 0 {
		return "", 0, nil
	}
	partnerID, _ = pair(us[0].PartnerID)
	return us[0].Name, partnerID, nil
}

// LogCall implements crm.Connector: a note on the partner's chatter, on the
// owner's own partner when the caller is unknown. The reference is the
// message id.
func (c *Connector) LogCall(ctx context.Context, cc crm.Conn, l crm.CallLog) (string, error) {
	ownerName, ownerPartner, err := c.owner(ctx, cc, l.OwnerID)
	if err != nil {
		return "", err
	}
	target := ownerPartner
	if l.Contact != nil {
		if id, err := strconv.ParseInt(l.Contact.ID, 10, 64); err == nil {
			target = id
		}
	}
	if target == 0 {
		return "", fmt.Errorf("%w: no Odoo contact or call owner to attach the note to", crm.ErrNoTarget)
	}
	var msg int64
	kw := map[string]any{"body": note(l, ownerName), "message_type": "comment", "subtype_xmlid": "mail.mt_note"}
	if err := c.call(ctx, cc, "res.partner", "message_post", []any{[]int64{target}}, kw, &msg); err != nil {
		return "", err
	}
	if msg == 0 {
		return "", &crm.APIError{Msg: "the response has no message id"}
	}
	return strconv.FormatInt(msg, 10), nil
}

// UpdateCallLog implements crm.Connector: it rewrites the note's body (the
// recording link). A call with no link has nothing to patch.
func (c *Connector) UpdateCallLog(ctx context.Context, cc crm.Conn, ref string, l crm.CallLog) error {
	if l.RecordingURL == "" {
		return nil
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return &crm.APIError{Msg: "the stored message id is not a number"}
	}
	ownerName, _, err := c.owner(ctx, cc, l.OwnerID)
	if err != nil {
		return err
	}
	return c.call(ctx, cc, "mail.message", "write", []any{[]int64{id}, map[string]any{"body": note(l, ownerName)}}, nil, nil)
}

// tail9 is the last nine digits of a number.
func tail9(n string) string {
	d := kit.Digits(n)
	if len(d) > 9 {
		d = d[len(d)-9:]
	}
	return d
}
