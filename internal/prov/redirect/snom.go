package redirect

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// Snom's redirect services (spec S-11, vendor reference table).
var (
	snomRESTBase  = "https://api.sraps.snom.com/api/v1/"
	snomXMLRPCURL = "https://secure-provisioning.snom.com:8083/xmlrpc/" // the trailing slash is required
)

// snomSetting is the SRAPS setting a phone's provisioning URL goes in.
const snomSetting = "setting_server"

type snomSettings struct {
	API string `json:"api"` // "rest" (default) or "xmlrpc"
}

// snom drives SRAPS: the REST API signed with Hawk, or the legacy XML-RPC
// redirect service with basic authentication.
type snom struct {
	conn
	id, key string
	xmlrpc  bool

	mu        sync.Mutex
	settingID string // the setting_server setting's UUID, once found
	endpoints string // the company's endpoints collection URL, once found
}

func newSnom(creds Credentials, settings []byte, hc *http.Client) (*snom, error) {
	v, err := need(prov.Snom, creds, KeySnomKeyID, KeySnomKeySecret)
	if err != nil {
		return nil, err
	}
	var s snomSettings
	if err := decodeSettings(prov.Snom, settings, &s); err != nil {
		return nil, err
	}
	if s.API != "" && s.API != "rest" && s.API != "xmlrpc" {
		return nil, errors.New(`redirect: snom settings: api must be "rest" or "xmlrpc"`)
	}
	return &snom{conn: conn{vendor: prov.Snom, hc: hc, secrets: v}, id: v[0], key: v[1], xmlrpc: s.API == "xmlrpc"}, nil
}

func (*snom) Vendor() prov.Vendor { return prov.Snom }

func (*snom) Capabilities() Caps { return Caps{RegistersURL: true, Supported: true} }

// snomMAC is the 12 hex digits, lowercase for REST and uppercase for
// XML-RPC (the case of each service's examples).
func snomMAC(mac string, upper bool) string {
	m := strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac)
	if upper {
		return strings.ToUpper(m)
	}
	return strings.ToLower(m)
}

func (s *snom) Check(ctx context.Context) error {
	if s.xmlrpc {
		// The service has no account call; a lookup of an unassigned MAC
		// answers only when the credentials are accepted.
		_, err := s.rpc(ctx, "redirect.getPhoneRedirection", "000000000000")
		return s.clean("check", err)
	}
	_, err := s.endpointsURL(ctx)
	return s.clean("check", err)
}

func (s *snom) Register(ctx context.Context, mac, _, u string) error {
	mac, err := normMAC(mac)
	if err != nil {
		return s.clean("register", err)
	}
	if s.xmlrpc {
		return s.clean("register", s.rpcOK(ctx, "redirect.registerPhone", snomMAC(mac, true), u))
	}
	return s.clean("register", s.put(ctx, snomMAC(mac, false), u))
}

func (s *snom) Unregister(ctx context.Context, mac string) error {
	mac, err := normMAC(mac)
	if err != nil {
		return s.clean("unregister", err)
	}
	if s.xmlrpc {
		_, found, err := s.lookupRPC(ctx, mac)
		if err != nil || !found {
			return s.clean("unregister", err)
		}
		return s.clean("unregister", s.rpcOK(ctx, "redirect.deregisterPhone", snomMAC(mac, true)))
	}
	ep, err := s.endpointsURL(ctx)
	if err != nil {
		return s.clean("unregister", err)
	}
	status, body, err := s.hawk(ctx, http.MethodDelete, ep+snomMAC(mac, false), nil)
	if err == nil && status >= 300 && status != http.StatusNotFound {
		err = snomError(status, body)
	}
	return s.clean("unregister", err)
}

func (s *snom) Lookup(ctx context.Context, mac string) (string, bool, error) {
	mac, err := normMAC(mac)
	if err != nil {
		return "", false, s.clean("lookup", err)
	}
	if s.xmlrpc {
		u, found, err := s.lookupRPC(ctx, mac)
		return u, found, s.clean("lookup", err)
	}
	u, found, err := s.lookupREST(ctx, mac)
	return u, found, s.clean("lookup", err)
}

// put creates or replaces the endpoint with the URL in setting_server.
func (s *snom) put(ctx context.Context, mac, u string) error {
	id, err := s.serverSettingID(ctx)
	if err != nil {
		return err
	}
	ep, err := s.endpointsURL(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"mac":                      mac,
		"autoprovisioning_enabled": true,
		"settings_manager":         map[string]any{id: map[string]any{"value": u, "attrs": map[string]string{"perm": "RW"}}},
	})
	if err != nil {
		return err
	}
	status, rb, err := s.hawk(ctx, http.MethodPut, ep+mac, body)
	if err == nil && status >= 300 {
		err = snomError(status, rb)
	}
	return err
}

func (s *snom) lookupREST(ctx context.Context, mac string) (string, bool, error) {
	id, err := s.serverSettingID(ctx)
	if err != nil {
		return "", false, err
	}
	ep, err := s.endpointsURL(ctx)
	if err != nil {
		return "", false, err
	}
	status, body, err := s.hawk(ctx, http.MethodGet, ep+snomMAC(mac, false), nil)
	switch {
	case err != nil:
		return "", false, err
	case status == http.StatusNotFound:
		return "", false, nil
	case status >= 300:
		return "", false, snomError(status, body)
	}
	var e struct {
		Settings map[string]struct {
			Value string `json:"value"`
		} `json:"settings_manager"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return "", false, fmt.Errorf("decoding the endpoint: %w", err)
	}
	set, ok := e.Settings[id]
	if !ok || set.Value == "" {
		return "", false, nil
	}
	return set.Value, true, nil
}

// serverSettingID finds the UUID of the setting_server setting, following
// the settings list's Link pagination.
func (s *snom) serverSettingID(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settingID != "" {
		return s.settingID, nil
	}
	next := snomRESTBase + "settings/?page_size=1000"
	for pages := 0; next != "" && pages < 20; pages++ {
		status, body, h, err := s.hawkFull(ctx, http.MethodGet, next, nil)
		if err != nil {
			return "", err
		}
		if status >= 300 {
			return "", snomError(status, body)
		}
		var page []struct {
			UUID      string `json:"uuid"`
			ParamName string `json:"param_name"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", fmt.Errorf("decoding the settings list: %w", err)
		}
		for _, p := range page {
			if p.ParamName == snomSetting && p.UUID != "" {
				s.settingID = p.UUID
				return p.UUID, nil
			}
		}
		next = linkNext(h.Get("Link"))
		if next != "" && !sameOrigin(snomRESTBase, next) {
			return "", errors.New("the settings list links to another host")
		}
	}
	return "", errors.New("SRAPS has no " + snomSetting + " setting")
}

// endpointsURL resolves the company's endpoints collection through the
// access key's token resource.
func (s *snom) endpointsURL(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoints != "" {
		return s.endpoints, nil
	}
	var tok struct {
		Links struct {
			Company string `json:"company"`
		} `json:"links"`
	}
	if err := s.getJSON(ctx, snomRESTBase+"tokens/"+url.PathEscape(s.id), &tok); err != nil {
		return "", err
	}
	if !sameOrigin(snomRESTBase, tok.Links.Company) {
		return "", errors.New("the token resource has no company link on the SRAPS host")
	}
	var co struct {
		Links struct {
			Endpoints string `json:"endpoints"`
		} `json:"links"`
	}
	if err := s.getJSON(ctx, tok.Links.Company, &co); err != nil {
		return "", err
	}
	if !sameOrigin(snomRESTBase, co.Links.Endpoints) {
		return "", errors.New("the company has no endpoints link on the SRAPS host")
	}
	s.endpoints = co.Links.Endpoints
	if !strings.HasSuffix(s.endpoints, "/") {
		s.endpoints += "/"
	}
	return s.endpoints, nil
}

func (s *snom) getJSON(ctx context.Context, u string, dst any) error {
	status, body, err := s.hawk(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return snomError(status, body)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decoding %s: %w", strings.TrimPrefix(u, snomRESTBase), err)
	}
	return nil
}

func (s *snom) hawk(ctx context.Context, method, u string, body []byte) (int, []byte, error) {
	status, b, _, err := s.hawkFull(ctx, method, u, body)
	return status, b, err
}

// hawkFull sends a Hawk-signed request (HMAC-SHA256 over the normalized
// request string, with the payload hash for a body).
func (s *snom) hawkFull(ctx context.Context, method, u string, body []byte) (int, []byte, http.Header, error) {
	// u is the SRAPS base or a link checked by sameOrigin.
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body)) //nolint:gosec // see above
	if err != nil {
		return 0, nil, nil, err
	}
	ctype := ""
	if body != nil {
		ctype = "application/json"
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", hawkHeader(s.id, s.key, method, req.URL, time.Now().Unix(),
		base64.RawURLEncoding.EncodeToString(nonce), ctype, body, ""))
	resp, err := s.hc.Do(req) //nolint:gosec // the SRAPS host only (sameOrigin)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, nil, nil, fmt.Errorf("%s %s: %w", method, req.URL.Path, ue.Err)
		}
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, b, resp.Header, err
}

// hawkHeader builds a Hawk 1 Authorization header. The payload hash is
// included for every request, an empty payload hashed with an empty
// content type.
func hawkHeader(id, key, method string, u *url.URL, ts int64, nonce, ctype string, body []byte, ext string) string {
	hash := hawkPayloadHash(ctype, body)
	mac := hawkMAC(key, ts, nonce, method, u, hash, ext)
	h := fmt.Sprintf(`Hawk id="%s", ts="%d", nonce="%s", hash="%s"`, id, ts, nonce, hash)
	if ext != "" {
		h += fmt.Sprintf(`, ext="%s"`, ext)
	}
	return h + fmt.Sprintf(`, mac="%s"`, mac)
}

// hawkPayloadHash is base64(SHA-256("hawk.1.payload\n" + content type
// without parameters + "\n" + payload + "\n")).
func hawkPayloadHash(ctype string, body []byte) string {
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "hawk.1.payload\n%s\n", strings.ToLower(strings.TrimSpace(ctype)))
	_, _ = h.Write(body)
	_, _ = h.Write([]byte("\n"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// hawkMAC is base64(HMAC-SHA256(key, "hawk.1.header\n" + ts, nonce, method,
// request URI, host, port, payload hash and ext, each followed by "\n")).
func hawkMAC(key string, ts int64, nonce, method string, u *url.URL, hash, ext string) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	norm := fmt.Sprintf("hawk.1.header\n%d\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n",
		ts, nonce, strings.ToUpper(method), u.RequestURI(), strings.ToLower(u.Hostname()), port, hash, ext)
	m := hmac.New(sha256.New, []byte(key))
	_, _ = m.Write([]byte(norm))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// linkNext returns the rel="next" target of an RFC 8288 Link header.
func linkNext(h string) string {
	for part := range strings.SplitSeq(h, ",") {
		segs := strings.Split(part, ";")
		for _, p := range segs[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(segs[0]), "<>")
			}
		}
	}
	return ""
}

// snomError is a SRAPS refusal, with its message where the body has one.
func snomError(status int, body []byte) error {
	var e struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return &apiError{Status: status, Msg: firstNonEmpty(e.Message, e.Detail, e.Error)}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// lookupRPC reads a registration through redirect.getPhoneRedirection,
// which answers [true, url] for a registered MAC and [false, message]
// otherwise.
func (s *snom) lookupRPC(ctx context.Context, mac string) (string, bool, error) {
	vals, err := s.rpc(ctx, "redirect.getPhoneRedirection", snomMAC(mac, true))
	if err != nil {
		return "", false, err
	}
	ok, msg := rpcPair(vals)
	if !ok || msg == "" {
		return "", false, nil
	}
	return msg, true, nil
}

// rpcOK calls a method answering [success, message] and turns a false
// success into an error carrying the message.
func (s *snom) rpcOK(ctx context.Context, method string, args ...string) error {
	vals, err := s.rpc(ctx, method, args...)
	if err != nil {
		return err
	}
	if ok, msg := rpcPair(vals); !ok {
		return fmt.Errorf("%s refused: %s", method, msg)
	}
	return nil
}

// rpcPair reads a [boolean, string] answer.
func rpcPair(v any) (bool, string) {
	arr, _ := v.([]any)
	if len(arr) == 0 {
		b, _ := v.(bool)
		return b, ""
	}
	ok, _ := arr[0].(bool)
	msg := ""
	if len(arr) > 1 {
		msg = fmt.Sprint(arr[1])
	}
	return ok, msg
}

// rpc calls an XML-RPC method with string arguments and returns its single
// result value.
func (s *snom) rpc(ctx context.Context, method string, args ...string) (any, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	_ = xml.EscapeText(&b, []byte(method))
	b.WriteString(`</methodName><params>`)
	for _, a := range args {
		b.WriteString(`<param><value><string>`)
		_ = xml.EscapeText(&b, []byte(a))
		b.WriteString(`</string></value></param>`)
	}
	b.WriteString(`</params></methodCall>`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, snomXMLRPCURL, &b)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.id, s.key)
	req.Header.Set("Content-Type", "text/xml")
	status, body, err := s.do(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &apiError{Status: status}
	}
	return parseRPCResponse(body)
}

// parseRPCResponse decodes an XML-RPC methodResponse: its one parameter, or
// the fault as an error.
func parseRPCResponse(body []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	fault := false
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("decoding the XML-RPC response: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "fault":
			fault = true
		case "value":
			v, err := rpcValue(d, 0)
			if err != nil {
				return nil, err
			}
			if fault {
				m, _ := v.(map[string]any)
				return nil, fmt.Errorf("XML-RPC fault %v: %v", m["faultCode"], m["faultString"])
			}
			return v, nil
		}
	}
}

// rpcValue decodes the content of a <value> element whose start was read.
func rpcValue(d *xml.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("XML-RPC value nested too deep")
	}
	var text strings.Builder
	var out any
	typed := false
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("decoding an XML-RPC value: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if t.Name.Local == "value" {
				if !typed {
					return text.String(), nil
				}
				return out, nil
			}
		case xml.StartElement:
			typed = true
			switch t.Name.Local {
			case "array":
				arr := []any{}
				for {
					tok, err := d.Token()
					if err != nil {
						return nil, fmt.Errorf("decoding an XML-RPC array: %w", err)
					}
					if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "value" {
						v, err := rpcValue(d, depth+1)
						if err != nil {
							return nil, err
						}
						arr = append(arr, v)
					}
					if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "array" {
						break
					}
				}
				out = arr
			case "struct":
				m := map[string]any{}
				name := ""
				for {
					tok, err := d.Token()
					if err != nil {
						return nil, fmt.Errorf("decoding an XML-RPC struct: %w", err)
					}
					if se, ok := tok.(xml.StartElement); ok {
						switch se.Name.Local {
						case "name":
							var n string
							if err := d.DecodeElement(&n, &se); err != nil {
								return nil, err
							}
							name = n
						case "value":
							v, err := rpcValue(d, depth+1)
							if err != nil {
								return nil, err
							}
							m[name] = v
						}
					}
					if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "struct" {
						break
					}
				}
				out = m
			default:
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return nil, err
				}
				switch t.Name.Local {
				case "boolean":
					out = strings.TrimSpace(s) == "1"
				case "int", "i4", "i8":
					n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
					if err != nil {
						return nil, fmt.Errorf("XML-RPC integer: %w", err)
					}
					out = n
				default:
					out = s
				}
			}
		}
	}
}
