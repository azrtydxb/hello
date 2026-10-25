package prov

// The phone emulator: a contract test between Hello's built-in templates and
// how each vendor's phone turns a rendered setting into a request.
//
// A template can render a perfectly valid file that the phone then uses in a
// way nobody modelled: Grandstream's P192 is a firmware directory the phone
// appends its own file name to, so a full file URL made it ask for
// fw/wp810fw.bin/wp810fw.bin (404); a trailing slash in P237 made it ask for
// /p/boot//cfg<mac>.xml. Each emulator below holds one vendor's documented
// URL-building rules (base + appended file name, scheme setting, trailing
// slash, enum codes), boots the phone through the real handler (DHCP boot
// path, hand-off, per-device files), and requests every URL the phone would,
// asserting 200 with the right content. A setting that carries a URL, path,
// server or file name and is not modelled here fails the test, so a new one
// cannot ship unchecked. See "Phone emulator" in docs/provisioning.md.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// emu is one phone of one vendor being booted against the real handler.
type emu struct {
	t       tb
	e       *env
	p       *fakePhone
	vendor  Vendor
	host    string
	pinned  bool
	fw      []byte // the pinned firmware's bytes
	ca      []byte // the CA certificate, DER
	flagged map[string]string
	taken   map[string]bool
}

// tb is the part of testing.T the emulators use, so a test can run one
// against a template with a known bug and read the failures it reports.
type tb interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// conf is one fetched config body, as the phone parses it.
type conf struct {
	m  *emu
	kv map[string]string
}

// urlKey is, per vendor, the names of settings that carry a URL, path,
// server, file name or scheme code. Any value holding "://" is flagged too.
var urlKey = map[Vendor]*regexp.Regexp{
	Yealink:     regexp.MustCompile(`(?i)url|server|path|ntp|firmware|voice_mail`),
	Poly:        regexp.MustCompile(`(?i)servername|servertype|bootsrv|url|path|\.address$|sntp|callback|_files$|_directory$|customcacert`),
	Grandstream: regexp.MustCompile(`^P(47|30|33|145|192|193|194|212|232|233|234|235|237|238|330|331|6767|8433)$`),
	Snom:        regexp.MustCompile(`(?i)server|url|firmware|ntp|host|mailbox|custom|status|directory|update_policy|prov_polling`),
	Fanvil:      regexp.MustCompile(`(?i)server|url|file name|protocol|image|sntp|register addr|mwi|upgrade|flash`),
}

var (
	kvLine     = regexp.MustCompile(`^\s*([^#=\s][^=]*?)\s*=\s*(.*?)\s*$`)
	kvAttr     = regexp.MustCompile(`([A-Za-z_][\w.]*)="([^"]*)"`)
	kvP        = regexp.MustCompile(`<(P\d+)>([^<]*)</P\d+>`)
	kvTag      = regexp.MustCompile(`<([a-z_][a-z_0-9]*)(?:\s[^>]*)?>([^<]*)</([a-z_][a-z_0-9]*)>`)
	kvFanvil   = regexp.MustCompile(`^\s*([^:<\n][^:\n]*?)\s*:(.*)$`)
	xmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// parseConf reads a vendor's config format into name/value pairs.
func parseConf(v Vendor, body string) [][2]string {
	var out [][2]string
	add := func(k, val string) {
		out = append(out, [2]string{strings.TrimSpace(k), html.UnescapeString(strings.TrimSpace(val))})
	}
	switch v {
	case Yealink:
		for _, l := range strings.Split(body, "\n") {
			if m := kvLine.FindStringSubmatch(l); m != nil {
				add(m[1], m[2])
			}
		}
	case Poly:
		for _, m := range kvAttr.FindAllStringSubmatch(xmlComment.ReplaceAllString(body, ""), -1) {
			if !strings.HasSuffix(m[1], ".set") { // "apply this value" flags
				add(m[1], m[2])
			}
		}
	case Grandstream:
		for _, m := range kvP.FindAllStringSubmatch(body, -1) {
			add(m[1], m[2])
		}
	case Snom:
		for _, m := range kvTag.FindAllStringSubmatch(body, -1) {
			if m[1] == m[3] {
				add(m[1], m[2])
			}
		}
	case Fanvil:
		for _, l := range strings.Split(body, "\n") {
			if m := kvFanvil.FindStringSubmatch(l); m != nil {
				add(m[1], m[2])
			}
		}
	}
	return out
}

// learn parses a fetched body and flags every setting that carries a URL.
func (m *emu) learn(body string) conf {
	c := conf{m: m, kv: map[string]string{}}
	for _, kv := range parseConf(m.vendor, body) {
		c.kv[kv[0]] = kv[1]
		if urlKey[m.vendor].MatchString(kv[0]) || strings.Contains(kv[1], "://") {
			m.flagged[kv[0]] = kv[1]
		}
	}
	return c
}

// take returns a setting the phone acts on, failing if it is absent.
func (c conf) take(k string) string {
	c.m.t.Helper()
	v, ok := c.kv[k]
	if !ok {
		c.m.t.Errorf("%s: the rendered file has no %q", c.m.vendor, k)
	}
	c.m.taken[k] = true
	return v
}

// opt is take for a setting that may be absent ("" then).
func (c conf) opt(k string) string {
	c.m.taken[k] = true
	return c.kv[k]
}

// takeMatching marks every setting whose name matches re as modelled and
// returns them.
func (c conf) takeMatching(re string) map[string]string {
	r := regexp.MustCompile(re)
	out := map[string]string{}
	for k, v := range c.kv {
		if r.MatchString(k) {
			c.m.taken[k] = true
			out[k] = v
		}
	}
	return out
}

// get requests a URL as the phone would: the scheme sets the proxy's
// X-Forwarded-Proto (so a plain-HTTP device URL is refused, as in
// production), the host must be Hello's.
func (m *emu) get(what, raw string) *httptest.ResponseRecorder {
	m.t.Helper()
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		m.t.Fatalf("%s %s: %q is not an http(s) URL", m.vendor, what, raw)
	}
	if u.Host != m.host {
		m.t.Fatalf("%s %s: %q points outside Hello (%s)", m.vendor, what, raw, m.host)
	}
	return m.e.do(req{path: u.RequestURI(), proto: u.Scheme})
}

// must requests a URL that has to answer 200 and returns the body.
func (m *emu) must(what, raw string) string {
	m.t.Helper()
	w := m.get(what, raw)
	if w.Code != http.StatusOK {
		m.t.Fatalf("%s %s: GET %s answered %d, want 200 (the phone would give up here)", m.vendor, what, raw, w.Code)
	}
	return w.Body.String()
}

// optional requests a probe the phone tolerates losing: 200 or 404, never an
// error.
func (m *emu) optional(what, raw string) {
	m.t.Helper()
	if w := m.get(what, raw); w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		m.t.Fatalf("%s %s: GET %s answered %d, want 200 or 404", m.vendor, what, raw, w.Code)
	}
}

// firmware requests the firmware URL the phone derived and checks the body.
// With nothing pinned, the template must not name one.
func (m *emu) firmware(raw string) {
	m.t.Helper()
	if !m.pinned {
		if raw != "" {
			m.t.Errorf("%s: no firmware is pinned but the file names %q", m.vendor, raw)
		}
		return
	}
	if raw == "" {
		m.t.Errorf("%s: firmware is pinned but the file names none", m.vendor)
		return
	}
	if body := m.must("firmware", raw); body != string(m.fw) {
		m.t.Errorf("%s: firmware GET %s returned %q, want the pinned image", m.vendor, raw, body)
	}
}

// caFetch requests the CA certificate URL a setting names.
func (m *emu) caFetch(raw string) {
	m.t.Helper()
	if raw == "" {
		return
	}
	body := m.must("CA certificate", raw)
	if b, _ := pem.Decode([]byte(body)); b == nil || !bytes.Equal(b.Bytes, m.ca) {
		m.t.Errorf("%s: %s is not the CA certificate", m.vendor, raw)
	}
}

// book requests an address-book URL the phone built and checks it is the
// vendor's directory format.
func (m *emu) book(what, raw, marker string) {
	m.t.Helper()
	if body := m.must(what, raw); !strings.Contains(body, marker) {
		m.t.Errorf("%s: %s (%s) is not a %s directory:\n%s", m.vendor, what, raw, marker, body)
	}
}

// hostOnly fails a host/port setting that holds a scheme or a path.
func (m *emu) hostOnly(what, v string) {
	m.t.Helper()
	if v == "" || strings.Contains(v, "://") || strings.Contains(v, "/") {
		m.t.Errorf("%s: %s = %q must be a bare host[:port]", m.vendor, what, v)
	}
}

// deviceBase checks a URL names this phone's own HTTPS provisioning path.
func (m *emu) deviceBase(what, raw string) {
	m.t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != m.host || !strings.HasPrefix(u.Path, "/p/"+m.p.plain+"/") && u.Path != "/p/"+m.p.plain {
		m.t.Errorf("%s: %s = %q is not this phone's https://%s/p/<token>/ URL", m.vendor, what, raw, m.host)
	}
}

// slashAlways joins as a phone that appends "/" and a name to any base:
// a base ending in "/" doubles it. Hello answers both (spec: some phones
// append their own "/"), and this keeps it that way.
func slashAlways(base, name string) string { return base + "/" + name }

// slashEnsure joins as a phone that adds "/" only when the base has none.
func slashEnsure(base, name string) string { return strings.TrimSuffix(base, "/") + "/" + name }

// stripScheme drops a leading scheme, as Grandstream and Poly accept a
// server with or without one.
func stripScheme(s string) (scheme, rest string) {
	if i := strings.Index(s, "://"); i > 0 {
		return strings.ToLower(s[:i]), s[i+3:]
	}
	return "", s
}

const bootBase = "/p/boot/"

func (m *emu) boot() string { return "http://" + m.host + bootBase }

// --- Yealink ---

// emuYealink models T4x/T5x: the DHCP URL is a directory the phone appends
// y0000000000XX.cfg and <mac>.cfg to; static.auto_provision.server.url is a
// full URL (scheme included) the phone appends <mac>.cfg to;
// static.firmware.url and static.trusted_certificates.url are full file URLs.
// Yealink Auto Provisioning Guide V86; Nethesis Tancredi yealink.tmpl.
func emuYealink(m *emu) {
	id, ok := YealinkModelID(m.p.model)
	if !ok {
		m.t.Fatalf("no Yealink hardware ID for %s", m.p.model)
	}
	common := fmt.Sprintf("y%012d.cfg", id)
	m.optional("boot file", m.boot()+"y000000000000.boot")
	c := m.learn(m.must("boot common", m.boot()+common))
	m.caFetch(c.take("static.trusted_certificates.url"))
	c = m.learn(m.must("boot hand-off", m.boot()+m.p.mac+".cfg"))
	srv := c.take("static.auto_provision.server.url")
	m.caFetch(c.take("static.trusted_certificates.url"))
	m.deviceBase("static.auto_provision.server.url", srv)

	m.optional("model common", slashEnsure(srv, common))
	body := m.must("device file", slashEnsure(srv, m.p.mac+".cfg"))
	if !strings.Contains(body, "account.1.password = "+m.p.secret) {
		m.t.Errorf("Yealink: the file at the provisioning URL has no account")
	}
	c = m.learn(body)
	if got := c.take("static.auto_provision.server.url"); got != srv {
		m.t.Errorf("Yealink: the device file moves the server URL from %q to %q", srv, got)
	}
	m.caFetch(c.take("static.trusted_certificates.url"))
	m.firmware(c.opt("static.firmware.url"))
	// remote_phonebook.data.N.url is a full URL of one directory file.
	slots := 0
	for i := 1; i <= 5; i++ {
		name, u := c.opt(fmt.Sprintf("remote_phonebook.data.%d.name", i)), c.opt(fmt.Sprintf("remote_phonebook.data.%d.url", i))
		if u != "" {
			slots++
			m.book("remote phonebook "+name, u, "<YealinkIPPhoneDirectory>")
		}
	}
	if slots != 2 {
		m.t.Errorf("Yealink: %d remote phonebook slots filled, want the phone's 2 books", slots)
	}
	c.takeMatching(`^features\.remote_phonebook\.`)
	m.hostOnly("local_time.ntp_server1", c.take("local_time.ntp_server1"))
	m.hostOnly("account.1.sip_server.1.address", c.take("account.1.sip_server.1.address"))
	c.takeMatching(`^account\.1\.sip_server\.`)
	if v := c.opt("voice_mail.number.1"); strings.Contains(v, "/") || v == "" {
		m.t.Errorf("Yealink: voice_mail.number.1 = %q is not a dial string", v)
	}
	c.takeMatching(`^static\.auto_provision\.`)
}

// --- Poly ---

// emuPoly models VVX: DHCP 160 gives a directory; the phone fetches
// <mac>.cfg (the master), reads CONFIG_FILES (relative names, [PHONE_MAC_ADDRESS]
// replaced) and APP_FILE_PATH (a URL is fetched as is) from it;
// device.prov.serverName is a URL or host[/path] the phone appends the master
// name to, with device.prov.serverType choosing the protocol.
// UC Software 5.4.1 Administrator Guide (master configuration file).
func emuPoly(m *emu) {
	m.must("zero master", m.boot()+"000000000000.cfg")
	c := m.learn(m.must("boot master", m.boot()+m.p.mac+".cfg"))
	files := m.masterFiles(c)
	if len(files) == 0 {
		m.t.Fatalf("Poly: the boot master names no CONFIG_FILES")
	}
	var hand conf
	for _, f := range files {
		hand = m.learn(m.must("boot hand-off", m.boot()+f))
	}
	base := polyBase(m, hand)
	m.deviceBase("device.prov.serverName", base)

	master := m.learn(m.must("master", slashAlways(base, m.p.mac+".cfg")))
	var fw string
	for _, f := range m.masterFiles(master) {
		body := m.must("device file", slashAlways(base, f))
		if !strings.Contains(body, `reg.1.auth.password="`+m.p.secret+`"`) {
			m.t.Errorf("Poly: %s has no account", f)
		}
		d := m.learn(body)
		if got := polyBase(m, d); got != base {
			m.t.Errorf("Poly: the device file moves the server from %q to %q", base, got)
		}
		m.hostOnly("device.sntp.serverName", d.take("device.sntp.serverName"))
		m.hostOnly("tcpIpApp.sntp.address", d.take("tcpIpApp.sntp.address"))
		d.opt("device.dhcp.bootSrvUseOpt") // verify: enum codes, see the audit
		d.takeMatching(`^msg\.mwi\.`)
		d.takeMatching(`^(reg\.1\.|attendant\.|device\.sec\.)`)
	}
	// The phone's directory: <mac>-directory.xml next to the master.
	m.book("directory", slashAlways(base, m.p.mac+"-directory.xml"), "<directory>")
	if app := master.opt("APP_FILE_PATH"); app != "" {
		if !strings.Contains(app, "://") {
			app = slashAlways(base, app)
		}
		fw = app
	}
	m.firmware(fw)
}

// masterFiles reads CONFIG_FILES and the other master-file path attributes.
func (m *emu) masterFiles(c conf) []string {
	var out []string
	for _, f := range strings.Split(c.take("CONFIG_FILES"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, strings.ReplaceAll(f, "[PHONE_MAC_ADDRESS]", m.p.mac))
		}
	}
	c.takeMatching(`^(MISC_FILES|[A-Z_]+_DIRECTORY)$`)
	c.opt("APP_FILE_PATH")
	return out
}

// polyBase is the base URL device.prov.serverName and serverType give.
func polyBase(m *emu, c conf) string {
	name, typ := c.take("device.prov.serverName"), c.take("device.prov.serverType")
	if !slices.Contains([]string{"HTTP", "HTTPS", "FTP", "FTPS", "TFTP"}, typ) {
		m.t.Errorf("Poly: device.prov.serverType = %q is not a documented protocol", typ)
	}
	if scheme, _ := stripScheme(name); scheme == "" {
		name = strings.ToLower(typ) + "://" + name
	} else if scheme != strings.ToLower(typ) {
		m.t.Errorf("Poly: serverName scheme %q contradicts serverType %q", scheme, typ)
	}
	return name
}

// --- Grandstream ---

// gsVia is the "upgrade via" code of P212 (config) and P6767 (firmware):
// 0 TFTP, 1 HTTP, 2 HTTPS, 3 FTP, 4 FTPS. Only the HTTP ones are modelled.
var gsVia = map[string]string{"1": "http", "2": "https"}

// gsFirmwareFile is the file name a Grandstream model asks its firmware
// server directory for (Grandstream firmware upgrade guides: grp2600fw.bin;
// WP8xx: wp810fw.bin).
var gsFirmwareFile = map[string]string{"WP822": "wp810fw.bin", "GRP2614": "grp2600fw.bin"}

// emuGrandstream models GXP/GRP/WP: the DHCP URL is a directory the phone
// appends cfg<mac> (404), cfg<mac>.xml to; P237 (Config Server Path) and P192
// (Firmware Server Path) are server paths without a file name, P212 and P6767
// choose the protocol, and the phone appends "/" and its own file name.
// Grandstream firmware upgrade and SIP device provisioning guides
// (documentation.grandstream.com).
func emuGrandstream(m *emu) {
	cfgFile := "cfg" + m.p.mac
	m.optional("cfg<mac>", m.boot()+cfgFile)
	c := m.learn(m.must("boot common", m.boot()+"cfg.xml"))
	c.takeMatching(`^P(47|30|33|145|19[2-4]|212|23[2-8]|6767|8433)$`)
	c = m.learn(m.must("boot hand-off", m.boot()+cfgFile+".xml"))
	base := gsBase(m, c, "P212", "P237")
	c.takeMatching(`^P(47|30|33|145|19[2-4]|23[2-8]|6767|8433)$`)
	m.deviceBase("P237", base)

	m.optional("cfg<mac>", slashAlways(base, cfgFile))
	body := m.must("device file", slashAlways(base, cfgFile+".xml"))
	if !strings.Contains(body, "<P34>"+m.p.secret+"</P34>") {
		m.t.Errorf("Grandstream: the file at the config server path has no account")
	}
	c = m.learn(body)
	if got := gsBase(m, c, "P212", "P237"); got != base {
		m.t.Errorf("Grandstream: the device file moves the config server from %q to %q", base, got)
	}
	m.hostOnly("P30", c.take("P30"))
	if v := c.take("P47"); strings.Contains(v, "://") || strings.Contains(v, "/") {
		m.t.Errorf("Grandstream: P47 = %q must be host[:port]", v)
	}
	c.takeMatching(`^P(33|145|19[34]|8433)$`)
	var fw string
	if _, set := c.kv["P192"]; set {
		dir := gsBase(m, c, "P6767", "P192")
		name := c.opt("P232") + gsFirmwareFile[m.p.model] + c.opt("P233")
		if gsFirmwareFile[m.p.model] == "" {
			m.t.Fatalf("Grandstream: no firmware file name known for %s", m.p.model)
		}
		fw = slashAlways(dir, name)
	}
	// Phonebook XML download: P330 the protocol (1 HTTP, 3 HTTPS; 2 is TFTP),
	// P331 "host[:port]/dir" with the file name when it is not phonebook.xml,
	// P332 minutes (0 or 5 to 720). Grandstream XML Phonebook Guide.
	if _, set := c.kv["P331"]; set {
		scheme, ok := map[string]string{"1": "http", "3": "https"}[c.take("P330")]
		if !ok {
			m.t.Errorf("Grandstream: P330 = %q is not a modelled phonebook protocol code (1 HTTP, 3 HTTPS)", c.kv["P330"])
		}
		path := c.take("P331")
		if s, rest := stripScheme(path); s != "" {
			path = rest
		}
		if !strings.HasSuffix(path, ".xml") {
			path += "/phonebook.xml" // the phone looks for phonebook.xml when given a directory
		}
		m.book("phonebook", scheme+"://"+path, "<AddressBook>")
		if n, err := strconv.Atoi(c.take("P332")); err != nil || (n != 0 && (n < 5 || n > 720)) {
			m.t.Errorf("Grandstream: P332 = %q, documented: 0 or 5 to 720 minutes", c.kv["P332"])
		}
		c.opt("P333")
	}
	c.opt("P234")
	c.opt("P235")
	c.opt("P238")
	m.firmware(fw)
}

// gsBase is scheme (from the via code) + server path, accepting a server
// path with or without a leading scheme as the phone does.
func gsBase(m *emu, c conf, viaKey, pathKey string) string {
	via, path := c.take(viaKey), c.take(pathKey)
	scheme, ok := gsVia[via]
	if !ok {
		m.t.Errorf("Grandstream: %s = %q is not a modelled upgrade-via code (1 HTTP, 2 HTTPS)", viaKey, via)
		scheme = "https"
	}
	if s, rest := stripScheme(path); s != "" {
		if s != scheme {
			m.t.Errorf("Grandstream: %s = %q contradicts %s = %s (%s)", pathKey, path, viaKey, via, scheme)
		}
		path = rest
	}
	return scheme + "://" + path
}

// --- Snom ---

// emuSnom models D series: given a DHCP URL without a file name the phone
// asks for snom<model>.htm and snom<model>-<MAC>.htm; setting_server is a
// URL in which {mac} is replaced by the MAC (uppercase); firmware (10.1.38
// and later) is the firmware file's full URL; with none set the phone asks
// <dir>/snom<model>/snom<model>-firmware.htm and <dir>/snom<model>-firmware.htm.
// Snom wiki: Auto Provisioning, setting_server, firmware, update_policy.
func emuSnom(m *emu) {
	model := "snom" + m.p.model
	mac := strings.ToUpper(m.p.mac)
	c := m.learn(m.must("boot common", m.boot()+model+".htm"))
	first := c.take("setting_server")
	c.takeMatching(`^(prov_polling_|update_policy)`)
	c = m.learn(m.must("boot hand-off", m.boot()+model+"-"+mac+".htm"))
	ss := c.take("setting_server")
	c.takeMatching(`^(prov_polling_|update_policy)`)
	m.deviceBase("setting_server", strings.ReplaceAll(ss, "{mac}", mac))
	m.must("boot setting_server", strings.ReplaceAll(first, "{mac}", mac))

	body := m.must("settings", strings.ReplaceAll(ss, "{mac}", mac))
	if !strings.Contains(body, ">"+m.p.secret+"</user_pass>") {
		m.t.Errorf("Snom: the file at setting_server has no account")
	}
	c = m.learn(body)
	if got := c.take("setting_server"); got != ss {
		m.t.Errorf("Snom: the settings move setting_server from %q to %q", ss, got)
	}
	m.hostOnly("ntp_server", c.take("ntp_server"))
	if v := c.take("user_host"); strings.Contains(v, "://") || strings.Contains(v, "/") {
		m.t.Errorf("Snom: user_host = %q must be host[:port]", v)
	}
	if v := c.opt("user_mailbox"); strings.Contains(v, "/") {
		m.t.Errorf("Snom: user_mailbox = %q is not a dial string", v)
	}
	if v := c.take("prov_polling_mode"); !slices.Contains([]string{"rel", "abs", "random"}, v) {
		m.t.Errorf("Snom: prov_polling_mode = %q, documented: rel, abs, random", v)
	}
	if v := c.take("prov_polling_enabled"); !slices.Contains([]string{"on", "off"}, v) {
		m.t.Errorf("Snom: prov_polling_enabled = %q, documented: on, off", v)
	}
	// dkey_directory "url <URL>" opens the directory at a full URL.
	if v := c.opt("dkey_directory"); v != "" {
		u, ok := strings.CutPrefix(v, "url ")
		if !ok {
			m.t.Errorf("Snom: dkey_directory = %q is not a modelled action (url <URL>)", v)
		}
		m.book("directory", u, "<SnomIPPhoneDirectory>")
	}
	fw := c.opt("firmware")
	if pol := c.opt("update_policy"); pol != "" && !slices.Contains([]string{"auto_update", "ask_for_update", "settings_only", "never_update"}, pol) {
		m.t.Errorf("Snom: update_policy = %q, documented: auto_update, ask_for_update, settings_only, never_update", pol)
	}
	if (fw != "") != (c.kv["update_policy"] == "auto_update") {
		m.t.Errorf("Snom: update_policy auto_update and a firmware URL must come together (%q, %q)", c.kv["update_policy"], fw)
	}
	m.firmware(fw)
	dir := strings.TrimSuffix(ss, "{mac}")
	m.optional("firmware status", dir+model+"/"+model+"-firmware.htm")
	m.optional("firmware status", dir+model+"-firmware.htm")
}

// --- Fanvil ---

// fanvilProto is Flash Protocol: 1 FTP, 2 TFTP, 4 HTTP, 5 HTTPS (the codes
// Nethesis Tancredi's fanvil.macros scheme_map uses on X-series phones).
var fanvilProto = map[string]string{"4": "http", "5": "https"}

// emuFanvil models X series: DHCP gives a directory the phone appends the
// model's common file (F0V00X600000.cfg for the X6) and $mac.cfg to; Flash
// Server IP is a bare host[:port], Flash File Name the path and file name
// ($mac replaced), Flash Protocol the scheme; Auto Image Url is a full
// firmware URL. Fanvil Auto Provision Description (URL format, general
// configuration file names); Tancredi fanvil-X5.tmpl for the key names.
func emuFanvil(m *emu) {
	common, _ := FanvilCommon(m.p.model)
	c := m.learn(m.must("boot common", m.boot()+common))
	m.caFetch(c.opt("CACertURL"))
	c.takeMatching(`^Flash `)
	c = m.learn(m.must("boot hand-off", m.boot()+m.p.mac+".cfg"))
	devURL := fanvilURL(m, c)
	m.caFetch(c.opt("CACertURL"))
	c.takeMatching(`^Flash `)
	m.deviceBase("Flash Server IP + Flash File Name", devURL)

	body := m.must("device file", devURL)
	if !strings.Contains(body, ":"+m.p.secret) {
		m.t.Errorf("Fanvil: the file at the auto provision URL has no account")
	}
	c = m.learn(body)
	if got := fanvilURL(m, c); got != devURL {
		m.t.Errorf("Fanvil: the device file moves the server from %q to %q", devURL, got)
	}
	if mode := c.take("Flash Mode"); mode != "2" {
		m.t.Errorf("Fanvil: Flash Mode = %q; the phone re-checks only in interval mode (2)", mode)
	}
	if h, err := strconv.Atoi(c.take("Flash Interval")); err != nil || h < 1 {
		m.t.Errorf("Fanvil: Flash Interval must be whole hours >= 1")
	}
	m.caFetch(c.opt("CACertURL"))
	c.opt("Enable SNTP")
	m.hostOnly("SNTP Server", c.take("SNTP Server"))
	m.hostOnly("SIP1 Register Addr", c.take("SIP1 Register Addr"))
	c.takeMatching(`^SIP1 (Register|MWI)`)
	m.firmware(c.opt("Auto Image Url"))
	// Remote Phonebook1 URL is a full URL of the directory file.
	if u := c.opt("Remote Phonebook1 URL"); u != "" {
		m.book("remote phonebook", u, "<IPPhoneDirectory>")
		c.takeMatching(`^Remote Phonebook`)
	} else {
		m.t.Errorf("Fanvil: no remote phonebook URL although the phone has books")
	}
}

// fanvilURL is the URL the Flash settings give.
func fanvilURL(m *emu, c conf) string {
	host, file, proto := c.take("Flash Server IP"), c.take("Flash File Name"), c.take("Flash Protocol")
	scheme, ok := fanvilProto[proto]
	if !ok {
		m.t.Errorf("Fanvil: Flash Protocol = %q is not a modelled code (4 HTTP, 5 HTTPS)", proto)
		scheme = "https"
	}
	m.hostOnly("Flash Server IP", host)
	return scheme + "://" + host + "/" + strings.TrimPrefix(strings.ReplaceAll(file, "$mac", m.p.mac), "/")
}

// --- the tests ---

// testCA writes a CA certificate file the handler serves at /p/ca.crt.
func testCA(t *testing.T) (file string, der []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Hello emulator CA"}, NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return file, der
}

// emuCase is one phone model, the firmware file name Hello pins for it (the
// one the phone asks for) and the emulator of its vendor.
type emuCase struct {
	vendor Vendor
	model  string
	fw     string
	run    func(*emu)
}

var emuCases = []emuCase{
	{Yealink, "T54W", "T54W-96.86.0.70.rom", emuYealink},
	{Poly, "VVX 450", "sip.ld", emuPoly},
	{Grandstream, "WP822", "wp810fw.bin", emuGrandstream},
	{Grandstream, "GRP2614", "grp2600fw.bin", emuGrandstream},
	{Snom, "D785", "snomD785-10.1.200-SIP-r.bin", emuSnom},
	{Fanvil, "X6", "X6-2.0.2.z", emuFanvil},
}

// newEmu boots a handler with the phone, a CA file and (when pinned) the
// firmware, and returns its emulator.
func newEmu(t *testing.T, c emuCase, pinned bool) *emu {
	t.Helper()
	caFile, caDER := testCA(t)
	e := newEnv(t, func(o *Options) { o.CACertFile = caFile })
	p, _ := e.s.addPhone(c.vendor, c.model, macA)
	p.books = twoBooks()
	m := &emu{t: t, e: e, p: p, vendor: c.vendor, host: e.s.public.Host, pinned: pinned, ca: caDER,
		fw: []byte("image-" + c.fw), flagged: map[string]string{}, taken: map[string]bool{}}
	if pinned {
		key := string(c.vendor) + "/" + c.fw
		fw := Firmware{ID: 1, Vendor: c.vendor, Filename: c.fw, Version: "1.2.3", ObjectKey: key, SHA256: strings.Repeat("c", 64), UploadedAt: time.Now()}
		e.s.firmware[key], p.pinned = fw, &fw
		e.o[key] = m.fw
	}
	return m
}

// finish fails for every setting that carries a URL, path, server or file
// name that no emulator step used.
func (m *emu) finish() {
	var names []string
	for k, v := range m.flagged {
		if !m.taken[k] {
			names = append(names, fmt.Sprintf("%s = %q", k, v))
		}
	}
	slices.Sort(names)
	for _, n := range names {
		m.t.Errorf("%s: %s carries a URL, path, server or file name that phone_emulator_test.go does not model; add how the phone uses it (docs/provisioning.md, Phone emulator)", m.vendor, n)
	}
}

// TestPhoneEmulator fails if a phone of any first-class vendor, booted from
// the DHCP URL the way the vendor's documentation says it builds its
// requests, cannot reach its hand-off, its account file, its pinned firmware
// or the CA through the URLs Hello's built-in templates render; if a
// firmware setting is rendered with nothing pinned; if a scheme or "upgrade
// via" code contradicts what Hello serves (device files only over HTTPS); or
// if a setting that carries a URL, path, server or file name is rendered
// that this emulator does not model (so a new one must be added here).
func TestPhoneEmulator(t *testing.T) {
	for _, c := range emuCases {
		for _, pinned := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%s/pinned=%v", c.vendor, c.model, pinned), func(t *testing.T) {
				m := newEmu(t, c, pinned)
				c.run(m)
				m.finish()
			})
		}
	}
}

// failures collects what an emulator reports, so a test can read it.
type failures struct{ msgs []string }

func (f *failures) Helper() {}
func (f *failures) Errorf(format string, args ...any) {
	f.msgs = append(f.msgs, fmt.Sprintf(format, args...))
}

func (f *failures) Fatalf(format string, args ...any) {
	f.Errorf(format, args...)
	runtime.Goexit()
}

// runRecorded runs the emulator of c against a template that replaces old
// by repl in the vendor's built-in device file, and returns what it reports.
func runRecorded(t *testing.T, c emuCase, old, repl string) []string {
	t.Helper()
	m := newEmu(t, c, true)
	tmpl := builtinFor(t, c.vendor)
	changed := false
	for i, f := range tmpl.Files {
		if strings.Contains(f.Body, old) {
			tmpl.Files[i].Body, changed = strings.ReplaceAll(f.Body, old, repl), true
		}
	}
	if !changed {
		t.Fatalf("%s: no built-in file holds %q; update the test", c.vendor, old)
	}
	m.p.override = &tmpl
	f := &failures{}
	m.t = f
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.run(m)
		m.finish()
	}()
	<-done
	return f.msgs
}

// TestPhoneEmulatorCatchesKnownBugs fails if the emulator passes a template
// with a bug of the class it guards against: Grandstream P192 holding the
// full firmware file URL (the phone appends its own file name and gets 404),
// a scheme code that contradicts what Hello serves, Fanvil's server setting
// holding a full URL, and a URL-carrying setting nobody modelled.
func TestPhoneEmulatorCatchesKnownBugs(t *testing.T) {
	byModel := func(model string) emuCase {
		for _, c := range emuCases {
			if c.model == model {
				return c
			}
		}
		t.Fatalf("no emulator case for %s", model)
		return emuCase{}
	}
	for _, tc := range []struct {
		name, model, old, repl, want string
	}{
		{"grandstream P192 as a file URL", "WP822", "<P192>{{xml .Dir}}</P192>", "<P192>{{xml .URL}}</P192>", "wp810fw.bin/wp810fw.bin"},
		{"grandstream HTTP code for an HTTPS server", "WP822", "<P212>2</P212>", "<P212>1</P212>", "contradicts"},
		{"grandstream unknown via code", "WP822", "<P212>2</P212>", "<P212>7</P212>", "not a modelled upgrade-via code"},
		{"yealink unmodelled ringtone URL", "T54W", "static.firmware.url", "ringtone.url = https://x.example/ring.wav\nstatic.firmware.url", "does not model"},
		{"snom firmware without update policy", "D785", `<update_policy perm="R">auto_update</update_policy>`, "", "must come together"},
		{"fanvil server as a full URL", "X6", "Flash Server IP    :", "Flash Server IP    :https://x/", "must be a bare host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := runRecorded(t, byModel(tc.model), tc.old, tc.repl)
			if !strings.Contains(strings.Join(msgs, "\n"), tc.want) {
				t.Fatalf("emulator reported %q, want a failure containing %q", msgs, tc.want)
			}
		})
	}
}

// TestPhoneEmulatorFileNames fails if a file name the vendor's documentation
// says the phone requests from a directory is not answered by the boot path
// (the DHCP URL), or if a name outside the phone's set is answered with
// anything but 404.
func TestPhoneEmulatorFileNames(t *testing.T) {
	e := newEnv(t)
	e.s.addPhone(Generic, "x", "000000000099")
	// Fanvil's general configuration file names (Fanvil Auto Provision
	// Description 4.1): "F0V0" + the model, zero-filled to 12 characters,
	// with the model right-aligned in its first 4 characters.
	for model, name := range map[string]string{
		"X6": "F0V00X600000.cfg", "X7": "F0V00X700000.cfg", "X5S": "F0V0X5S00000.cfg",
		"X7C": "F0V0X7C00000.cfg", "X3U": "F0V0X3U00000.cfg", "X4U": "F0V0X4U00000.cfg",
		"X5U": "F0V0X5U00000.cfg", "X6U": "F0V0X6U00000.cfg", "X210": "F0VX21000000.cfg",
		"X3S": "f0X3Shw1.100.cfg", "H3": "f0H3hw1.100.cfg", "i20S": "f0i20SVhw1.100.cfg",
	} {
		if got, ok := FanvilCommon(model); !ok || got != name {
			t.Errorf("FanvilCommon(%s) = %q, %v; the phone asks for %s", model, got, ok, name)
		}
		if w := e.do(req{path: bootBase + name, proto: "http"}); w.Code != http.StatusOK {
			t.Errorf("boot path: %s (Fanvil %s common file) answered %d", name, model, w.Code)
		}
	}
	// Grandstream: cfg<mac>.xml, the model file and the generic file.
	for _, name := range []string{"cfg.xml", "cfggrp2614.xml", "cfgwp822.xml"} {
		if w := e.do(req{path: bootBase + name, proto: "http"}); w.Code != http.StatusOK {
			t.Errorf("boot path: %s (Grandstream common file) answered %d", name, w.Code)
		}
	}
	// A directory-style name the phone appended twice is not served.
	if w := e.do(req{path: bootBase + "cfg.xml/cfg.xml", proto: "http"}); w.Code != http.StatusNotFound {
		t.Errorf("cfg.xml/cfg.xml answered %d, want 404", w.Code)
	}
}
