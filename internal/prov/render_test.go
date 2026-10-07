package prov

import (
	"bytes"
	"context"
	"encoding/xml"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/provclient"
)

func renderSample(firmware bool) RenderData {
	tok, _ := NewToken()
	d := RenderData{
		Phone:  Phone{MAC: "805ec0aabbcc", MACUpper: "805EC0AABBCC", Model: "MODEL", AdminPassword: `adm<&>"'n`},
		Line:   Line{Username: "1001-aabbcc", AuthName: "1001-aabbcc-auth", Password: `s3cr&t<x>"'`, DisplayName: "Alice & Bob", Label: "1001", Domain: "hello.test", VoicemailCode: "*97"},
		Server: Server{Host: "192.168.10.101", Port: 30508, Transport: "udp", Expiry: 3600},
		BLF:    []BLFKey{{Number: "1002", Label: "Bob", URI: "sip:1002@hello.test"}, {Number: "1003", Label: "Carol", URI: "sip:1003@hello.test"}},
		Prov:   ProvInfo{URL: "https://prov.hello.test/p/" + tok + "/", CAURL: "http://prov.hello.test/p/ca.crt", ResyncSeconds: 86400},
		Time:   TimeInfo{Zone: "Europe/Brussels", NTP: "pool.ntp.org"},
	}
	if firmware {
		d.Firmware = &FirmwareInfo{URL: d.Prov.URL + "fw/T54W-96.86.0.70.rom", Version: "96.86.0.70"}
	}
	return d
}

func builtinFor(t *testing.T, v Vendor) Template {
	t.Helper()
	for _, b := range Builtins() {
		if b.Vendor == v {
			return b
		}
	}
	t.Fatalf("no built-in for %s", v)
	return Template{}
}

// TestRenderedConfigContents fails if a built-in rendering for any
// first-class vendor lacks the server, port, username, auth name, secret,
// display name, BLF keys, re-check URL with the current token, CA URL,
// admin password or a pinned firmware URL (and carries a firmware URL when
// none is pinned), or if two renders of the same input differ by a byte
// (spec S-5, S-20).
func TestRenderedConfigContents(t *testing.T) {
	models := map[Vendor]string{Yealink: "T54W", Poly: "VVX 450", Grandstream: "GRP2614", Snom: "D785", Fanvil: "X5U"}
	for _, v := range Vendors[:5] {
		for _, pinned := range []bool{true, false} {
			d := renderSample(pinned)
			d.Phone.Vendor, d.Phone.Model = v, models[v]
			tmpl := builtinFor(t, v)
			render := func(name string) []byte {
				t.Helper()
				a, err := Render(context.Background(), tmpl, name, d)
				if err != nil {
					t.Fatalf("%s %s: %v", v, name, err)
				}
				b, _ := Render(context.Background(), tmpl, name, d)
				if !bytes.Equal(a, b) {
					t.Fatalf("%s %s: two renders differ", v, name)
				}
				return a
			}
			var got []provclient.Config
			var err error
			var c provclient.Config
			switch v {
			case Yealink:
				c, err = provclient.ParseYealink(render(d.Phone.MAC + ".cfg"))
				got = append(got, c)
			case Poly:
				master := render(d.Phone.MAC + ".cfg")
				if fallback := render("000000000000.cfg"); !bytes.Equal(master, fallback) {
					t.Errorf("poly: the 000000000000.cfg fallback is not the per-MAC master")
				}
				c, err = provclient.ParsePoly(master, render(d.Phone.MAC+"-hello.cfg"))
				got = append(got, c)
			case Grandstream:
				c, err = provclient.ParseGrandstream(render("cfg"+d.Phone.MAC+".xml"), d.Phone.MAC)
				got = append(got, c)
			case Snom:
				for _, name := range []string{d.Phone.MACUpper, "snomD785-" + d.Phone.MACUpper + ".htm"} {
					c, err = provclient.ParseSnom(render(name))
					if err != nil {
						break
					}
					got = append(got, c)
				}
			case Fanvil:
				c, err = provclient.ParseFanvil(render(d.Phone.MAC + ".cfg"))
				got = append(got, c)
			}
			if err != nil {
				t.Fatalf("%s: parse: %v", v, err)
			}
			want := provclient.Config{
				Server: d.Server.Host, Port: d.Server.Port, User: d.Line.Username, AuthUser: d.Line.AuthName,
				Password: d.Line.Password, DisplayName: d.Line.DisplayName, BLF: []string{"1002", "1003"},
				ProvURL: d.Prov.URL, CAURL: d.Prov.CAURL, AdminPassword: d.Phone.AdminPassword,
			}
			if pinned {
				want.FirmwareURL = d.Firmware.URL
			}
			for _, g := range got {
				if !reflect.DeepEqual(g, want) {
					t.Errorf("%s (firmware pinned %v):\n got %+v\nwant %+v", v, pinned, g, want)
				}
			}
		}
	}
}

// TestCACertInTemplates fails if the Poly or Grandstream built-ins (device
// files and boot bodies) do not carry the CA certificate PEM byte for byte
// once parsed (Poly device.sec.TLS.customCaCert1, Grandstream P8433), its
// line breaks included, or carry the CA keys when no CA is configured.
func TestCACertInTemplates(t *testing.T) {
	const caPEM = "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIU\nZm9vYmFy\n-----END CERTIFICATE-----"
	polyCA := func(t *testing.T, body []byte) string {
		t.Helper()
		var doc struct {
			Device struct {
				Attrs []xml.Attr `xml:",any,attr"`
			} `xml:"device"`
		}
		if err := xml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("poly: %v\n%s", err, body)
		}
		var ca, set string
		for _, a := range doc.Device.Attrs {
			switch a.Name.Local {
			case "device.sec.TLS.customCaCert1":
				ca = a.Value
			case "device.sec.TLS.customCaCert1.set":
				set = a.Value
			}
		}
		if (ca == "") != (set == "") {
			t.Fatalf("poly: customCaCert1 %q with .set %q", ca, set)
		}
		return ca
	}
	gsCA := func(t *testing.T, body []byte) string {
		t.Helper()
		var doc struct {
			P8433 *string `xml:"config>P8433"`
		}
		if err := xml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("grandstream: %v\n%s", err, body)
		}
		if doc.P8433 == nil {
			return ""
		}
		return *doc.P8433
	}
	for _, withCA := range []bool{true, false} {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Poly, "VVX 450"
		want := ""
		if withCA {
			d.Prov.CACertPEM, want = caPEM, caPEM
		}
		render := func(v Vendor, name string) []byte {
			t.Helper()
			b, err := Render(context.Background(), builtinFor(t, v), name, d)
			if err != nil {
				t.Fatalf("%s %s: %v", v, name, err)
			}
			return b
		}
		boot := func(v Vendor) []byte {
			t.Helper()
			b, err := renderBody(context.Background(), bootBodies[v].common, RenderData{Phone: d.Phone, Prov: d.Prov})
			if err != nil {
				t.Fatalf("%s boot: %v", v, err)
			}
			return b
		}
		if got := polyCA(t, render(Poly, d.Phone.MAC+"-hello.cfg")); got != want {
			t.Errorf("poly device (CA %v): customCaCert1 = %q, want %q", withCA, got, want)
		}
		if got := polyCA(t, boot(Poly)); got != want {
			t.Errorf("poly boot (CA %v): customCaCert1 = %q, want %q", withCA, got, want)
		}
		d.Phone.Vendor, d.Phone.Model = Grandstream, "GRP2614"
		if got := gsCA(t, render(Grandstream, "cfg"+d.Phone.MAC+".xml")); got != want {
			t.Errorf("grandstream device (CA %v): P8433 = %q, want %q", withCA, got, want)
		}
		if got := gsCA(t, boot(Grandstream)); got != want {
			t.Errorf("grandstream boot (CA %v): P8433 = %q, want %q", withCA, got, want)
		}
	}
}

// TestBuiltinsValidate fails if a built-in template does not pass the
// validation administrators' templates must pass.
func TestBuiltinsValidate(t *testing.T) {
	for _, b := range Builtins() {
		if errs := Validate(b); len(errs) > 0 {
			t.Errorf("%s: %+v", b.Name, errs)
		}
		if !b.Builtin() || b.BuiltinRef != string(b.Vendor) {
			t.Errorf("%s: not marked built-in", b.Name)
		}
	}
	if got := len(Builtins()); got != 5 {
		t.Fatalf("%d built-ins, want one per first-class vendor", got)
	}
}

// TestTemplateResolutionAndValidation fails if resolution picks other than
// override, then priority, then glob specificity, then ID; if a template
// that fails to parse, uses an unknown variable, reaches a method, calls
// call or printf, ranges over a number or too deep, defines templates,
// exceeds 100 ms or 256 KiB, or has a bad pattern passes validation (spec
// S-8).
func TestTemplateResolutionAndValidation(t *testing.T) {
	tpl := func(id int64, v Vendor, glob string, prio int) Template {
		return Template{ID: id, Vendor: v, ModelGlob: glob, Priority: prio, Name: "t"}
	}
	phone := Phone{Vendor: Yealink, Model: "T54W"}
	all := []Template{
		tpl(5, Yealink, "*", 0), tpl(7, Yealink, "T5*", 0), tpl(3, Yealink, "T5?W", 0), tpl(2, Yealink, "T54?", 0),
		tpl(1, Poly, "*", 50), tpl(9, Yealink, "T4*", 50),
	}
	pick := func(override *Template, ts []Template) int64 {
		got, ok := Resolve(phone, override, ts)
		if !ok {
			return -1
		}
		return got.ID
	}
	if got := pick(nil, all); got != 2 { // T54? and T5?W tie on specificity; lower ID
		t.Errorf("tie on specificity picked %d, want 2", got)
	}
	if got := pick(nil, append(all, tpl(11, Yealink, "t54w", 0))); got != 11 {
		t.Errorf("most specific glob (any case) picked %d, want 11", got)
	}
	if got := pick(nil, append(all, tpl(12, Yealink, "*", 1))); got != 12 {
		t.Errorf("higher priority picked %d, want 12", got)
	}
	ov := tpl(99, Yealink, "X*", -5)
	if got := pick(&ov, append(all, tpl(12, Yealink, "*", 1))); got != 99 {
		t.Errorf("override picked %d, want 99", got)
	}
	if got := pick(nil, []Template{tpl(1, Poly, "*", 0), tpl(9, Yealink, "T4*", 0), tpl(4, Yealink, "[", 0)}); got != -1 {
		t.Errorf("no match picked %d", got)
	}
	if got := pick(nil, append(Builtins(), tpl(0, Yealink, "*", -1))); got != 0 {
		t.Errorf("built-in not resolved: %d", got)
	}

	file := func(body string) Template {
		return Template{Vendor: Generic, ModelGlob: "*", Name: "x", Files: []TemplateFile{{Pattern: "{mac}.cfg", ContentType: "text/plain", Body: body}}}
	}
	refused := map[string]string{
		"parse":            "line one\n{{.Line.Username",
		"unknown":          "{{.Line.Secret}}",
		"unknown in with":  "{{with .Firmware}}{{.Checksum}}{{end}}",
		"unknown in range": "{{range .BLF}}{{.Extension}}{{end}}",
		"unknown var":      "{{$p := .Phone}}{{$p.Serial}}",
		"method":           "{{.Phone.Vendor.Valid}}",
		"call":             "{{call .Phone.MAC}}",
		"printf":           `{{printf "%0999999999d" 1}}`,
		"slice":            "{{slice .Phone.MAC 1}}",
		"range number":     "{{range 1000000000}}x{{end}}",
		"range call":       "{{range (index .BLF 0)}}x{{end}}",
		"range depth":      "{{range .BLF}}{{range $.BLF}}{{range $.BLF}}x{{end}}{{end}}{{end}}",
		"define":           `{{define "a"}}x{{end}}`,
		"template":         `{{template "body"}}`,
		"too large":        "{{range .BLF}}{{range $.BLF}}" + strings.Repeat("x", 3000) + "{{end}}{{end}}",
		"nil firmware":     "{{.Firmware.URL}}",
	}
	for name, body := range refused {
		if errs := Validate(file(body)); len(errs) == 0 || errs[0].Path != "files[0].body" {
			t.Errorf("%s: validation passed or blamed the wrong field: %+v", name, errs)
		}
		if _, err := Render(context.Background(), file(body), "805ec0aabbcc.cfg", renderSample(true)); err == nil && name != "nil firmware" && name != "too large" { // both depend on the data
			t.Errorf("%s: rendered", name)
		}
	}
	if errs := Validate(file("ok\n{{.Line.Username")); len(errs) != 1 || errs[0].Line != 2 {
		t.Errorf("parse error line: %+v", errs)
	}
	if errs := Validate(file("a\nb\n{{.Line.Nope}}")); len(errs) != 1 || errs[0].Line != 3 {
		t.Errorf("unknown variable line: %+v", errs)
	}
	accepted := `{{$.Line.Password}}{{range $i, $k := .BLF}}{{add $i 1}}={{xml $k.URI}}{{end}}` +
		`{{with .Firmware}}{{.URL}}{{end}}{{default "none" .Phone.Label}}{{upper .Phone.MAC}}{{if eq .Server.Port 5060}}std{{end}}`
	if errs := Validate(file(accepted)); len(errs) > 0 {
		t.Errorf("valid template refused: %+v", errs)
	}

	bad := file("x")
	bad.Files = append(bad.Files,
		TemplateFile{Pattern: "{serial}.cfg", ContentType: "text/plain"},
		TemplateFile{Pattern: "../{mac}", ContentType: "text/plain"},
		TemplateFile{Pattern: "{MAC}.CFG", ContentType: "text/plain"},
		TemplateFile{Pattern: "ok.cfg", ContentType: ""})
	bad.ModelGlob, bad.Vendor, bad.Name = "[", "acme", ""
	var paths []string
	for _, e := range Validate(bad) {
		paths = append(paths, e.Path)
	}
	for _, want := range []string{"vendor", "modelGlob", "name", "files[1].pattern", "files[2].pattern", "files[3].pattern", "files[4].contentType"} {
		if !slices.Contains(paths, want) {
			t.Errorf("no error at %s: %v", want, paths)
		}
	}
	if errs := Validate(Template{Vendor: Generic, ModelGlob: "*", Name: "x"}); len(errs) != 1 || errs[0].Path != "files" {
		t.Errorf("a template without files: %+v", errs)
	}

	// The 100 ms deadline: with the budget shrunk, a valid render fails.
	defer func(d time.Duration) { renderTimeout = d }(renderTimeout)
	renderTimeout = time.Nanosecond
	if errs := Validate(file("{{.Line.Username}}")); len(errs) != 1 || !strings.Contains(errs[0].Message, "100ms") {
		t.Errorf("deadline not enforced: %+v", errs)
	}
}

// TestBootBodiesHandOff fails if a common boot body sets the server URL or
// the DHCP-option override (fetched in the same boot cycle, it would undo
// the hand-off), or if a hand-off body does not set the phone's own HTTPS
// URL with HTTPS and the DHCP option off (Grandstream P237/P212/P145,
// Yealink, Fanvil, Poly), so the next boot leaves the boot URL.
func TestBootBodiesHandOff(t *testing.T) {
	const own = "https://prov.hello.test/p/TOKEN/"
	bootInfo := ProvInfo{URL: "http://prov.hello.test/p/boot/", CAURL: "http://prov.hello.test/p/ca.crt", ResyncSeconds: 86400}
	ownInfo := ProvInfo{URL: own, CAURL: bootInfo.CAURL, ResyncSeconds: 86400}
	render := func(t *testing.T, body string, info ProvInfo) string {
		t.Helper()
		b, err := renderBody(context.Background(), body, RenderData{Phone: Phone{MAC: "ec74d769e044", Model: "WP836"}, Prov: info})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		v             Vendor
		commonMustNot []string
		handoffMust   []string
	}{
		{Grandstream, []string{"<P237>", "<P145>", "<P212>"}, []string{"<P237>" + own + "</P237>", "<P212>2</P212>", "<P145>0</P145>"}},
		{Yealink, []string{"auto_provision.server.url", "dhcp_option"}, []string{"static.auto_provision.server.url = " + own, "static.auto_provision.dhcp_option.enable = 0"}},
		{Fanvil, []string{"FlashServerIP"}, []string{"FlashServerIP      :" + own}},
		{Poly, nil, []string{`device.prov.serverName="` + own + `"`, `device.dhcp.bootSrvUseOpt="2"`}},
	}
	for _, c := range cases {
		bb := bootBodies[c.v]
		common := render(t, bb.common, bootInfo)
		for _, s := range c.commonMustNot {
			if strings.Contains(common, s) {
				t.Errorf("%s common boot body sets %q:\n%s", c.v, s, common)
			}
		}
		handoff := render(t, bb.handoffBody(), ownInfo)
		for _, s := range c.handoffMust {
			if !strings.Contains(handoff, s) {
				t.Errorf("%s hand-off lacks %q:\n%s", c.v, s, handoff)
			}
		}
	}
}
