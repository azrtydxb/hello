package prov

import (
	"context"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Phone onboarding (spec phone-onboarding): a config request for a MAC that
// is not a phone is answered with the usual empty 404 and recorded as a
// candidate an operator can adopt. Nothing about the phone is ever served.

// Limits of the queue. The endpoint is unauthenticated, so the table is
// bounded: a full queue ignores new MACs (counted), and a candidate
// unseen for CandidateTTL is dropped.
const (
	MaxCandidates = 500
	CandidateTTL  = 30 * 24 * time.Hour
	// DefaultOnboardPerMin is the per-source-IP limit on candidate writes.
	DefaultOnboardPerMin = 30
)

// Candidate is one sighting of an unadopted phone.
type Candidate struct {
	MAC      string // 12 lowercase hex digits
	Vendor   Vendor
	Model    string
	Firmware string
	IP       netip.Addr
	Path     string // the requested path, redacted
	At       time.Time
}

// CaptureOutcome is what a capture did.
type CaptureOutcome string

// Capture outcomes.
const (
	// CaptureStored: a new candidate, or a known one seen again.
	CaptureStored CaptureOutcome = "stored"
	// CaptureKnown: the MAC is already a phone; nothing was recorded.
	CaptureKnown CaptureOutcome = "known"
	// CaptureIgnored: an operator ignored the MAC; nothing was recorded.
	CaptureIgnored CaptureOutcome = "ignored"
	// CaptureFull: the queue holds MaxCandidates; the new MAC was not recorded.
	CaptureFull CaptureOutcome = "full"
)

// Onboarding records candidates. internal/store implements it; it is safe
// for several replicas at once (one upsert under the database's own locks).
type Onboarding interface {
	// CaptureCandidate upserts the candidate: a new row, or the existing
	// row's last_seen, request_count, source IP, path and any newly known
	// vendor, model or firmware. It does nothing for a MAC that is a phone
	// or is ignored, and ignores a new MAC while MaxCandidates rows exist.
	CaptureCandidate(ctx context.Context, c Candidate) (CaptureOutcome, error)
}

// CandidateMAC reports whether mac may be recorded: 12 lowercase hex
// digits that can be a device address (not all-zero, not broadcast, not a
// multicast/group address).
func CandidateMAC(mac string) bool {
	if !isLowerHex12(mac) || mac == zeroMAC || mac == "ffffffffffff" {
		return false
	}
	first := hexVal(mac[1]) // low nibble of the first octet; bit 0 is the group bit
	return first&1 == 0
}

func isLowerHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func hexVal(c byte) int {
	if c >= 'a' {
		return int(c-'a') + 10
	}
	return int(c - '0')
}

// Per-vendor User-Agent shapes. Each yields a model and a firmware version.
var (
	// "Grandstream Model HW WP822 V1.0A SW 1.0.9.69 DevId 00:0b:82:…"
	uaGrandstream = regexp.MustCompile(`(?i)Grandstream\s+Model\s+HW\s+(\S+)(?:\s+V\S+)?(?:\s+SW\s+(\S+))?`)
	// "Grandstream GXP2130 1.0.11.5"
	uaGrandstreamOld = regexp.MustCompile(`(?i)Grandstream\s+([A-Za-z]{2,4}\d\w*)(?:\s+(\d+(?:\.\d+)+))?`)
	// "Yealink SIP-T54W 96.86.0.70 00:15:65:…"
	uaYealink = regexp.MustCompile(`(?i)Yealink\s+(?:SIP-)?(\S+)(?:\s+(\d+(?:\.\d+){2,}))?`)
	// "Fanvil X6U 2.4.1.5876 0c:38:3e:…"
	uaFanvil = regexp.MustCompile(`(?i)Fanvil\s+(\S+)(?:\s+(\d+(?:\.\d+){2,}))?`)
	// "snomD785-SIP 10.1.2.3" and "Mozilla/4.0 (compatible; snom370-SIP 8.7.5.35; …)"
	uaSnom = regexp.MustCompile(`(?i)snom\s?([A-Za-z]?\d{3}\w*?)(?:-SIP)?[\s/;]+(\d+(?:\.\d+){2,})?`)
	// "PolycomVVX-VVX_450-UA/5.9.5.0614", "PolycomSoundPointIP-SPIP_550-UA/4.0.13"
	uaPoly = regexp.MustCompile(`(?i)Poly(?:com)?[\w]*?-([A-Za-z0-9_]+)-UA/(\d+(?:\.\d+)+)`)
)

// ParseUserAgent reads the vendor, model and firmware a phone's
// User-Agent states; hint is the vendor the requested file name implies
// (used when the User-Agent names none). Unknown parts are empty. The
// model is cut down to the characters a phone model may hold.
func ParseUserAgent(ua string, hint Vendor) (v Vendor, model, firmware string) {
	v = hint
	m := func(re *regexp.Regexp, vendor Vendor) bool {
		g := re.FindStringSubmatch(ua)
		if g == nil {
			return false
		}
		v, model = vendor, g[1]
		if len(g) > 2 {
			firmware = g[2]
		}
		return true
	}
	switch {
	case m(uaGrandstream, Grandstream), m(uaGrandstreamOld, Grandstream):
	case m(uaYealink, Yealink):
	case m(uaFanvil, Fanvil):
	case m(uaPoly, Poly):
		model = strings.ReplaceAll(model, "_", " ")
	case m(uaSnom, Snom):
	}
	if firmware == "" {
		firmware = uaFirmware(ua)
	}
	if v == "" {
		v = Generic
	}
	return v, cleanModel(model), firmware
}

// cleanModel keeps the characters the console accepts in a model name, at
// most 64 of them; a User-Agent is untrusted input.
func cleanModel(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			strings.IndexByte(" ._+()/-", c) >= 0 {
			b.WriteByte(c)
		}
		if b.Len() == 64 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
