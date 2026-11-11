package prov

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/location"
)

func heldPlace(city string) HELDLocation {
	return HELDLocation{Extension: "1001", Place: location.Place{Path: "HQ", Country: "NL", City: city, Street: "Damrak", Floor: "3"}}
}

const heldReq = `<?xml version="1.0"?><locationRequest xmlns="urn:ietf:params:xml:ns:geopriv:held"><locationType exact="false">civic geodetic</locationType></locationRequest>`

func postHeld(e *env, token, body string, mod ...func(*req)) *httpResult {
	r := req{method: http.MethodPost, path: devPath(token, "held"), body: body, ua: "Grandstream WP836 1.0.3.39"}
	for _, f := range mod {
		f(&r)
	}
	w := e.do(r)
	return &httpResult{code: w.Code, body: w.Body.String(), ctype: w.Header().Get("Content-Type")}
}

type httpResult struct {
	code        int
	body, ctype string
}

func heldDoc(t *testing.T, body string) (root xml.Name, code string) {
	t.Helper()
	var v struct {
		XMLName xml.Name
		Code    string `xml:"code,attr"`
	}
	if err := xml.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not XML: %v\n%s", err, body)
	}
	return v.XMLName, v.Code
}

// TestHELDServer covers the HELD server (spec S-28 d). It fails if a phone is
// answered with another phone's location, if a request is answered without a
// valid token over HTTPS for an allowlisted phone (or with anything but an
// empty 404 when it is not), if a phone without a location is not told so in
// a HELD error, if a malformed request is accepted, or if the request body
// can pick the location.
func TestHELDServer(t *testing.T) {
	e := newEnv(t)
	a, ta := e.s.addPhone(Grandstream, "WP836", macA)
	b, tb := e.s.addPhone(Grandstream, "WP826", macB)
	e.s.locs = map[int64]HELDLocation{a.id: heldPlace("Amsterdam"), b.id: heldPlace("Rotterdam")}

	t.Run("each phone gets its own location", func(t *testing.T) {
		for tok, want := range map[string]string{ta: "Amsterdam", tb: "Rotterdam"} {
			r := postHeld(e, tok, heldReq)
			if r.code != http.StatusOK || r.ctype != location.ContentTypeHELD {
				t.Fatalf("status %d type %q", r.code, r.ctype)
			}
			root, _ := heldDoc(t, r.body)
			if root.Local != "locationResponse" || root.Space != location.NSHeld {
				t.Fatalf("root = %v", root)
			}
			other := "Rotterdam"
			if want == "Rotterdam" {
				other = "Amsterdam"
			}
			if !strings.Contains(r.body, "<ca:A3>"+want+"</ca:A3>") || strings.Contains(r.body, other) || !strings.Contains(r.body, "pres:1001@prov.hello.test") {
				t.Fatalf("response for %s:\n%s", want, r.body)
			}
			if row := e.last(); row.Result != ResultServed || row.Status != http.StatusOK {
				t.Fatalf("audit row %+v", row)
			}
		}
	})

	t.Run("a body naming another phone cannot pick the location", func(t *testing.T) {
		body := `<locationRequest xmlns="urn:ietf:params:xml:ns:geopriv:held"><device xmlns="urn:ietf:params:xml:ns:geopriv:held:id"><mac>` + macB + `</mac></device></locationRequest>`
		if r := postHeld(e, ta, body); !strings.Contains(r.body, "Amsterdam") || strings.Contains(r.body, "Rotterdam") {
			t.Fatalf("response:\n%s", r.body)
		}
	})

	t.Run("denials are an empty 404", func(t *testing.T) {
		unknown, _ := NewToken()
		for name, c := range map[string]*httpResult{
			"unknown token": postHeld(e, unknown, heldReq),
			"plain http":    postHeld(e, ta, heldReq, func(r *req) { r.proto = "http" }),
			"GET":           {},
		} {
			if name == "GET" {
				w := e.do(req{path: devPath(ta, "held")})
				c = &httpResult{code: w.Code, body: w.Body.String()}
			}
			if c.code != http.StatusNotFound || c.body != "" {
				t.Errorf("%s: %d %q, want an empty 404", name, c.code, c.body)
			}
		}
		if !e.s.phone(a.id).exposed {
			t.Error("a token sent over plain HTTP was not flagged exposed")
		}
		if w := e.do(req{method: http.MethodPost, path: "/p/boot/held", body: heldReq}); w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Errorf("POST /p/boot/held = %d", w.Code)
		}
		e.s.mu.Lock()
		e.s.phones[b.id].enabled = false
		e.s.mu.Unlock()
		if r := postHeld(e, tb, heldReq); r.code != http.StatusNotFound || r.body != "" {
			t.Errorf("disabled phone: %d %q", r.code, r.body)
		}
	})

	t.Run("errors are HELD documents", func(t *testing.T) {
		c, tc := e.s.addPhone(Grandstream, "WP836", "805ec0001122")
		_ = c
		for name, tc := range map[string]struct {
			tok, body, code string
		}{
			"no location":    {tc, heldReq, location.ErrLocationUnavailable},
			"not a request":  {ta, "hello", location.ErrRequest},
			"wrong root":     {ta, `<foo/>`, location.ErrRequest},
			"doctype":        {ta, `<!DOCTYPE x [<!ENTITY a "b">]><locationRequest xmlns="urn:ietf:params:xml:ns:geopriv:held"/>`, location.ErrRequest},
			"exact geodetic": {ta, `<locationRequest xmlns="urn:ietf:params:xml:ns:geopriv:held"><locationType exact="true">geodetic</locationType></locationRequest>`, location.ErrLocationUnavailable},
		} {
			r := postHeld(e, tc.tok, tc.body)
			if r.code != http.StatusOK || r.ctype != location.ContentTypeHELD {
				t.Errorf("%s: %d %q", name, r.code, r.ctype)
				continue
			}
			if root, code := heldDoc(t, r.body); root.Local != "error" || code != tc.code {
				t.Errorf("%s: %s code %q, want error %q", name, root.Local, code, tc.code)
			}
		}
	})

	t.Run("an outage is a 503", func(t *testing.T) {
		e.s.mu.Lock()
		e.s.outage = errors.New("database down")
		e.s.mu.Unlock()
		defer func() {
			e.s.mu.Lock()
			e.s.outage = nil
			e.s.mu.Unlock()
		}()
		if r := postHeld(e, ta, heldReq); r.code != http.StatusServiceUnavailable {
			t.Errorf("outage: %d", r.code)
		}
	})
}

func e911Data(model string) RenderData {
	d := gsFamilyData(model)
	d.E911 = NewE911("https://prov.hello.test/p/"+strings.Repeat("a", 52)+"/", "112,911")
	d.AlarmNumber = AlarmNumber
	return d
}

// TestWP8x6EmergencyKeys fails if a WP8x6 with E911 on lacks the E911 and HELD
// keys of Grandstream's template, if the HELD address is not host/path
// without a scheme, if the alarm keys are written without an alarm
// destination (P22270 must be omitted then), or if a WP822 or WP856 gets any.
func TestWP8x6EmergencyKeys(t *testing.T) {
	tok := strings.Repeat("a", 52)
	got := renderGS(t, e911Data("WP836"))
	for _, k := range []string{
		"<P8565>1</P8565>", "<P8566>1</P8566>", "<P8567>60</P8567>",
		"<P8568>prov.hello.test/p/" + tok + "/held</P8568>", "<P8597>112;911</P8597>", "<P8725>1</P8725>", "<P26059>1</P26059>",
		"<P22269>1</P22269>", "<P22270>*55</P22270>", "<P22601>1</P22601>", "<P22602>1</P22602>",
	} {
		if !strings.Contains(got, k) {
			t.Errorf("WP836 lacks %s", k)
		}
	}

	d := e911Data("WP836")
	d.AlarmNumber = ""
	got = renderGS(t, d)
	for _, k := range []string{"<P22269>", "<P22270>", "<P22601>", "<P22602>"} {
		if strings.Contains(got, k) {
			t.Errorf("no alarm destination, but %s is written", k)
		}
	}
	if !strings.Contains(got, "<P8565>1</P8565>") {
		t.Error("E911 keys vanished with the alarm")
	}

	d = e911Data("WP836")
	d.E911 = nil
	got = renderGS(t, d)
	if strings.Contains(got, "<P8565>") || strings.Contains(got, "<P8568>") || !strings.Contains(got, "<P22270>*55</P22270>") {
		t.Error("E911 off: the E911 keys are written or the alarm lost")
	}

	for _, model := range []string{"WP822", "WP856", "GRP2614"} {
		got := renderGS(t, e911Data(model))
		for _, k := range []string{"<P8565>", "<P8568>", "<P22269>", "<P22270>", "<P26059>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s carries %s", model, k)
			}
		}
	}
}

func TestNewE911(t *testing.T) {
	if e := NewE911("http://prov.hello.test/p/x/", "112"); e != nil {
		t.Errorf("plain http gave %+v", e)
	}
	if e := NewE911("", "112"); e != nil {
		t.Errorf("no URL gave %+v", e)
	}
	e := NewE911("https://prov.hello.test:8443/p/tok/", "112,911")
	if e == nil || e.Server != "prov.hello.test:8443/p/tok/held" || e.Numbers != "112;911" || e.IntervalMinutes < 30 || e.IntervalMinutes > 1440 {
		t.Errorf("E911 = %+v", e)
	}
}
