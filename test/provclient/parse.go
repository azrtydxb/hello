// Package provclient requests and reads provisioning files the way each
// first-class vendor's phones do (spec S-18). This file holds the parsers:
// each reads one vendor's format back into a neutral Config, so tests can
// compare what a phone would apply with the device it should register as.
package provclient

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Config is what a phone takes from its files.
type Config struct {
	Server        string
	Port          int
	User          string
	AuthUser      string
	Password      string
	DisplayName   string
	BLF           []string // extension numbers, in key order
	ProvURL       string
	CAURL         string
	FirmwareURL   string
	AdminPassword string
}

// caCommentRE finds the CA URL the XML formats carry in a comment, where
// the vendor has no key that takes a URL.
var caCommentRE = regexp.MustCompile(`Hello CA certificate: (\S+)`)

func caFromComment(b []byte) string {
	if m := caCommentRE.FindSubmatch(b); m != nil {
		return xmlUnescape(string(m[1]))
	}
	return ""
}

func xmlUnescape(s string) string {
	var out string
	if err := xml.Unmarshal([]byte("<x>"+s+"</x>"), &out); err != nil {
		return s
	}
	return out
}

func hostPort(s string) (string, int, error) {
	if s == "" { // a boot file carries no account
		return "", 0, nil
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(p)
	return h, n, err
}

// ParseYealink reads a Yealink per-MAC .cfg ("#!version:1.0.0.1", then
// key = value lines).
func ParseYealink(b []byte) (Config, error) {
	if !bytes.HasPrefix(b, []byte("#!version:1.0.0.1\n")) {
		return Config{}, errors.New("yealink: missing #!version:1.0.0.1 header")
	}
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, " = ")
		if !ok {
			return Config{}, fmt.Errorf("yealink: malformed line %q", line)
		}
		kv[k] = v
	}
	c := Config{
		Server: kv["account.1.sip_server.1.address"], User: kv["account.1.user_name"],
		AuthUser: kv["account.1.auth_name"], Password: kv["account.1.password"],
		DisplayName: kv["account.1.display_name"], ProvURL: kv["static.auto_provision.server.url"],
		CAURL: kv["static.trusted_certificates.url"], FirmwareURL: kv["static.firmware.url"],
	}
	c.AdminPassword, _ = strings.CutPrefix(kv["static.security.user_password"], "admin:")
	c.Port, _ = strconv.Atoi(kv["account.1.sip_server.1.port"])
	c.BLF = numbered(kv, `^linekey\.(\d+)\.value$`, func(n int) bool { return kv["linekey."+strconv.Itoa(n)+".type"] == "16" })
	return c, nil
}

// numbered returns the values of keys matching re (one number group), in
// numeric order, keeping those keep accepts.
func numbered(kv map[string]string, re string, keep func(int) bool) []string {
	r := regexp.MustCompile(re)
	type kn struct {
		n int
		v string
	}
	var out []kn
	for k, v := range kv {
		if m := r.FindStringSubmatch(k); m != nil {
			n, _ := strconv.Atoi(m[1])
			if keep(n) {
				out = append(out, kn{n, v})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	vals := make([]string, len(out))
	for i, x := range out {
		vals[i] = x.v
	}
	return vals
}

// xmlScan reads every element of an XML document: attributes of all
// elements into attrs, and each element's text into texts by name (in
// document order; repeated names append).
func xmlScan(b []byte) (attrs map[string]string, texts map[string][]string, err error) {
	attrs, texts = map[string]string{}, map[string][]string{}
	d := xml.NewDecoder(bytes.NewReader(b))
	var stack []string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return attrs, texts, nil
		}
		if err != nil {
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text.Reset()
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			texts[t.Name.Local] = append(texts[t.Name.Local], strings.TrimSpace(text.String()))
			text.Reset()
			stack = stack[:len(stack)-1]
		}
	}
}

func first(texts map[string][]string, name string) string {
	if v := texts[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// numberOfURI is the user part of sip:<number>@<domain>.
func numberOfURI(uri string) string {
	u := strings.TrimPrefix(uri, "sip:")
	n, _, _ := strings.Cut(u, "@")
	return n
}

// ParsePoly reads a Poly per-MAC master and the device file it names.
func ParsePoly(master, device []byte) (Config, error) {
	ma, _, err := xmlScan(master)
	if err != nil {
		return Config{}, fmt.Errorf("poly master: %w", err)
	}
	a, _, err := xmlScan(device)
	if err != nil {
		return Config{}, fmt.Errorf("poly device: %w", err)
	}
	c := Config{
		Server: a["reg.1.server.1.address"], User: a["reg.1.address"], AuthUser: a["reg.1.auth.userId"],
		Password: a["reg.1.auth.password"], DisplayName: a["reg.1.displayName"],
		ProvURL: a["device.prov.serverName"], CAURL: caFromComment(device),
		FirmwareURL: ma["APP_FILE_PATH"], AdminPassword: a["device.auth.localAdminPassword"],
	}
	c.Port, _ = strconv.Atoi(a["reg.1.server.1.port"])
	for _, uri := range numbered(a, `^attendant\.resourceList\.(\d+)\.address$`, func(int) bool { return true }) {
		c.BLF = append(c.BLF, numberOfURI(uri))
	}
	return c, nil
}

// ParseGrandstream reads a Grandstream cfg<mac>.xml (P-values); mac is
// what the phone checks the <mac> element against.
func ParseGrandstream(b []byte, mac string) (Config, error) {
	_, t, err := xmlScan(b)
	if err != nil {
		return Config{}, fmt.Errorf("grandstream: %w", err)
	}
	if got := first(t, "mac"); got != mac {
		return Config{}, fmt.Errorf("grandstream: <mac> is %q, not the phone's %q", got, mac)
	}
	p := map[string]string{}
	for k, v := range t {
		if len(v) > 0 {
			p[k] = v[0]
		}
	}
	c := Config{
		User: p["P35"], AuthUser: p["P36"], Password: p["P34"], DisplayName: p["P3"],
		ProvURL: p["P237"], CAURL: caFromComment(b), FirmwareURL: p["P192"], AdminPassword: p["P2"],
	}
	if c.Server, c.Port, err = hostPort(p["P47"]); err != nil {
		return Config{}, fmt.Errorf("grandstream: P47: %w", err)
	}
	// Multi-purpose keys: P(323+3k) mode (1 = BLF), P(325+3k) value.
	for k := 0; ; k++ {
		mode, ok := p["P"+strconv.Itoa(323+3*k)]
		if !ok {
			break
		}
		if mode == "1" {
			c.BLF = append(c.BLF, p["P"+strconv.Itoa(325+3*k)])
		}
	}
	return c, nil
}

// ParseSnom reads a Snom settings XML.
func ParseSnom(b []byte) (Config, error) {
	_, t, err := xmlScan(b)
	if err != nil {
		return Config{}, fmt.Errorf("snom: %w", err)
	}
	c := Config{
		User: first(t, "user_name"), AuthUser: first(t, "user_pname"), Password: first(t, "user_pass"),
		DisplayName: first(t, "user_realname"), CAURL: caFromComment(b),
		FirmwareURL: first(t, "firmware"), AdminPassword: first(t, "admin_mode_password"),
	}
	c.ProvURL = strings.TrimSuffix(first(t, "setting_server"), "{mac}")
	if c.Server, c.Port, err = hostPort(first(t, "user_host")); err != nil {
		return Config{}, fmt.Errorf("snom: user_host: %w", err)
	}
	for _, k := range t["fkey"] {
		if uri, ok := strings.CutPrefix(k, "blf "); ok {
			c.BLF = append(c.BLF, numberOfURI(uri))
		}
	}
	return c, nil
}

// ParseFanvil reads a Fanvil "<<VOIP CONFIG FILE>>" text config.
func ParseFanvil(b []byte) (Config, error) {
	s := string(b)
	if !strings.HasPrefix(s, "<<VOIP CONFIG FILE>>Version:2.0002\n") || !strings.HasSuffix(strings.TrimSpace(s), "<<END OF FILE>>") {
		return Config{}, errors.New("fanvil: missing <<VOIP CONFIG FILE>> header or <<END OF FILE>>")
	}
	kv := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(line, "<") || !strings.Contains(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		kv[strings.TrimSpace(k)] = v
	}
	c := Config{
		Server: kv["SIP1 RegisterAddr"], User: kv["SIP1 PhoneNumber"], AuthUser: kv["SIP1 RegisterUser"],
		Password: kv["SIP1 RegisterPswd"], DisplayName: kv["SIP1 DisplayName"], ProvURL: kv["FlashServerIP"],
		CAURL: kv["CACertURL"], FirmwareURL: kv["UpgradeServer1"], AdminPassword: kv["admin AdminPswd"],
	}
	c.Port, _ = strconv.Atoi(kv["SIP1 RegisterPort"])
	for _, v := range numbered(kv, `^Fkey(\d+) Value$`, func(n int) bool { return kv["Fkey"+strconv.Itoa(n)+" Type"] == "1" }) {
		num, _, _ := strings.Cut(v, "@")
		c.BLF = append(c.BLF, num)
	}
	return c, nil
}
