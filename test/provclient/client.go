package provclient

// This file holds the request side: each first-class vendor's file
// sequence for its representative model, with the MAC case, User-Agent and
// fallbacks its phones use (spec S-18 and the spec's vendor reference).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Model is one vendor's representative phone.
type Model struct {
	Vendor   string // Hello's vendor name
	Model    string // the model as entered in Hello
	Firmware string // the version its User-Agent reports
	OUI      string // a real OUI of the vendor, for test MACs
	Common   string // the model's common file name
}

// Models are the representative phones of spec S-18.
var Models = []Model{
	{Vendor: "yealink", Model: "T54W", Firmware: "96.86.0.70", OUI: "805ec0", Common: "y000000000096.cfg"},
	{Vendor: "poly", Model: "VVX 450", Firmware: "6.4.6.2195", OUI: "0004f2", Common: "000000000000.cfg"},
	{Vendor: "grandstream", Model: "GRP2614", Firmware: "1.0.9.69", OUI: "c074ad", Common: "cfggrp2614.xml"},
	{Vendor: "snom", Model: "D785", Firmware: "10.1.159.12", OUI: "000413", Common: "snomD785.htm"},
	{Vendor: "fanvil", Model: "X5U", Firmware: "2.12.1.4", OUI: "0c383e", Common: "F0V00X5U0000.cfg"},
}

// UserAgent is what the phone with MAC mac (12 lowercase hex digits)
// sends: Yealink, Grandstream and Fanvil include the MAC, Poly and Snom do
// not (Poly only with device.prov.tagSerialNo; Snom's is unconfirmed).
func (m Model) UserAgent(mac string) string {
	switch m.Vendor {
	case "yealink":
		pairs := make([]string, 0, 6)
		for i := 0; i < len(mac); i += 2 {
			pairs = append(pairs, mac[i:i+2])
		}
		return "Yealink SIP-" + m.Model + " " + m.Firmware + " " + strings.Join(pairs, ":")
	case "poly":
		return "FileTransport PolycomVVX-" + strings.ReplaceAll(m.Model, " ", "_") + "-UA/" + m.Firmware
	case "grandstream":
		return "Grandstream Model HW " + m.Model + " SW " + m.Firmware + " DevId " + mac
	case "snom":
		return "Mozilla/4.0 (compatible; snom" + m.Model + "-SIP " + m.Firmware + ")"
	case "fanvil":
		return "Fanvil " + m.Model + " " + m.Firmware + " " + mac
	}
	return "provclient"
}

// Fetch is one request the phone made.
type Fetch struct {
	Method, Name string
	Status       int
}

// Result is what a phone took from one provisioning pass.
type Result struct {
	Config  Config
	Fetches []Fetch
}

// Client fetches like a phone. HTTP must trust the server's CA.
type Client struct {
	HTTP *http.Client
}

// Boot is a DHCP-booted phone's pass against bootURL (the option 66
// value, ending in /p/boot/): the trust-on-first-use hand-off configures
// its per-device URL, which Result.Config.ProvURL carries.
func (c Client) Boot(ctx context.Context, m Model, mac, bootURL string) (Result, error) {
	return c.pass(ctx, m, mac, bootURL, true)
}

// Provision is the pass against the per-device URL the phone was given
// (https://…/p/<token>/), which carries its account.
func (c Client) Provision(ctx context.Context, m Model, mac, provURL string) (Result, error) {
	return c.pass(ctx, m, mac, provURL, false)
}

// pass runs one vendor sequence; base ends in "/".
func (c Client) pass(ctx context.Context, m Model, mac, base string, boot bool) (Result, error) {
	s := &session{c: c, ctx: ctx, base: base, ua: m.UserAgent(mac)}
	var (
		cfg Config
		err error
	)
	switch m.Vendor {
	case "yealink":
		s.optional("y000000000000.boot")
		s.optional(m.Common)
		dev := s.required(mac + ".cfg")
		s.optional(mac + "-local.cfg")
		s.optional(mac + "-contact.xml")
		if s.err == nil {
			cfg, err = ParseYealink(dev)
		}
	case "poly":
		cfg, err = s.poly(mac, boot)
	case "grandstream":
		s.optional("cfg" + mac)
		s.optional("cfg" + mac + ".bin")
		dev := s.required("cfg" + mac + ".xml")
		s.optional(m.Common)
		s.optional("cfg.xml")
		if s.err == nil {
			cfg, err = ParseGrandstream(dev, mac)
		}
	case "snom":
		// Booted, a Snom asks for its model and MAC files; once its
		// setting_server holds Hello's "<url>{mac}", it fetches that URL
		// with {mac} replaced by its MAC in upper case.
		var dev []byte
		if boot {
			s.optional(m.Common)
			dev = s.required("snom" + m.Model + "-" + strings.ToUpper(mac) + ".htm")
		} else {
			dev = s.required(strings.ToUpper(mac))
		}
		if s.err == nil {
			cfg, err = ParseSnom(dev)
		}
	case "fanvil":
		s.optional(m.Common)
		dev := s.required(mac + ".cfg")
		if s.err == nil {
			cfg, err = ParseFanvil(dev)
		}
	default:
		return Result{}, fmt.Errorf("provclient: no sequence for vendor %q", m.Vendor)
	}
	if s.err != nil {
		err = s.err
	}
	return Result{Config: cfg, Fetches: s.fetches}, err
}

// configFilesRE reads the CONFIG_FILES attribute of a Poly master.
var configFilesRE = regexp.MustCompile(`CONFIG_FILES="([^"]*)"`)

// poly asks for the per-MAC master, falling back to 000000000000.cfg,
// then for every file its CONFIG_FILES names ([PHONE_MAC_ADDRESS]
// replaced), and on the per-device URL uploads its boot log.
func (s *session) poly(mac string, boot bool) (Config, error) {
	master, status := s.get(http.MethodGet, mac+".cfg", nil)
	if status == http.StatusNotFound {
		master = s.required("000000000000.cfg")
	} else if s.err == nil && status != http.StatusOK {
		s.err = fmt.Errorf("%s.cfg = %d", mac, status)
	}
	if s.err != nil {
		return Config{}, s.err
	}
	m := configFilesRE.FindSubmatch(master)
	if m == nil || len(bytes.TrimSpace(m[1])) == 0 {
		return Config{}, errors.New("poly master names no CONFIG_FILES")
	}
	var device []byte
	for _, name := range strings.Split(string(m[1]), ",") {
		name = strings.ReplaceAll(strings.TrimSpace(name), "[PHONE_MAC_ADDRESS]", mac)
		if b := s.required(name); device == nil {
			device = b
		}
	}
	if !boot {
		if _, status := s.get(http.MethodPut, mac+"-boot.log", []byte("provclient boot log\n")); s.err == nil && status/100 != 2 {
			s.err = fmt.Errorf("PUT %s-boot.log = %d", mac, status)
		}
	}
	if s.err != nil {
		return Config{}, s.err
	}
	return ParsePoly(master, device)
}

// session is one pass's requests; the first error stops the rest.
type session struct {
	c       Client
	ctx     context.Context
	base    string
	ua      string
	fetches []Fetch
	err     error
}

// required fetches name, which must be served.
func (s *session) required(name string) []byte {
	b, status := s.get(http.MethodGet, name, nil)
	if s.err == nil && (status != http.StatusOK || len(b) == 0) {
		s.err = fmt.Errorf("%s = %d (%d bytes), want 200 with a body", name, status, len(b))
	}
	return b
}

// optional fetches name, which a phone tolerates missing.
func (s *session) optional(name string) {
	if _, status := s.get(http.MethodGet, name, nil); s.err == nil && status != http.StatusOK && status != http.StatusNotFound {
		s.err = fmt.Errorf("%s = %d, want 200 or 404", name, status)
	}
}

// maxFile bounds one response, far above any rendered config.
const maxFile = 1 << 20

func (s *session) get(method, name string, body []byte) ([]byte, int) {
	if s.err != nil {
		return nil, 0
	}
	req, err := http.NewRequestWithContext(s.ctx, method, s.base+name, bytes.NewReader(body))
	if err != nil {
		s.err = err
		return nil, 0
	}
	req.Header.Set("User-Agent", s.ua)
	res, err := s.c.HTTP.Do(req)
	if err != nil {
		s.err = fmt.Errorf("%s %s: %w", method, name, err)
		return nil, 0
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxFile))
	if err != nil {
		s.err = fmt.Errorf("%s %s: %w", method, name, err)
		return nil, 0
	}
	s.fetches = append(s.fetches, Fetch{Method: method, Name: name, Status: res.StatusCode})
	return b, res.StatusCode
}
