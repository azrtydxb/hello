package prov

// Wi-Fi profiles and roaming for Grandstream Wi-Fi handsets (phone-feature-
// parity S-23 and S-24). Every key below is read from Grandstream's published
// configuration templates (config-template-10212026.zip): wp8x6_config_1.0.3.39
// (WP816, WP826, WP836), wp810_822_825_config_1.0.11.83 and wp856_config_1.0.3.16.
// The WP820 template (wp820_config_1.0.7.90) lays a single network out the same
// way as the WP856 but Hello does not provision it. No other vendor's Wi-Fi keys
// are written: no vendor document for them is on hand, and a guessed key can
// strand a handset. Unverified on hardware (verify).

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Wi-Fi security modes of a profile.
const (
	WifiOpen           = "open"
	WifiWPA2PSK        = "wpa2-psk"
	WifiWPA3SAE        = "wpa3-sae"
	WifiOWE            = "owe"
	WifiWPA2Enterprise = "wpa2-enterprise"
	WifiWPA3Enterprise = "wpa3-enterprise"
)

// WifiSecurities lists every security mode.
var WifiSecurities = []string{WifiOpen, WifiWPA2PSK, WifiWPA3SAE, WifiOWE, WifiWPA2Enterprise, WifiWPA3Enterprise}

// WifiEnterprise reports whether the security mode authenticates with 802.1X.
func WifiEnterprise(sec string) bool {
	return sec == WifiWPA2Enterprise || sec == WifiWPA3Enterprise
}

// WifiUsesPSK reports whether the security mode takes a pre-shared key.
func WifiUsesPSK(sec string) bool { return sec == WifiWPA2PSK || sec == WifiWPA3SAE }

// The EAP method and the CA verification of an enterprise profile. Only PEAP
// with MSCHAPv2 is offered: EAP-TLS needs a device certificate (S-25).
const (
	EAPPEAP = "peap"

	CAServerSystem = "system" // P83016 = 1, System Certificates
	CAServerTOFU   = "tofu"   // P83016 = 2, Trust on First Use
	CAServerSkip   = "skip"   // P83016 = 0, Skip Verification
)

// MaxWifiPerScope is the most profiles one phone group, or one phone, carries.
const MaxWifiPerScope = 10

// Limits of a profile's fields.
const (
	MaxSSIDBytes = 32
	MinPSKLen    = 8
	MaxPSKLen    = 63
	MaxWifiName  = 64
)

// WifiProfile is one Wi-Fi network a handset may join.
type WifiProfile struct {
	ID       int64
	Name     string
	SSID     string
	Security string
	PSK      string // the opened pre-shared key; masked in previews
	// EAPMethod, Identity, AnonymousIdentity, Password and CACert are the
	// 802.1X settings of an enterprise profile; Password is masked in previews.
	EAPMethod         string
	Identity          string
	AnonymousIdentity string
	Password          string
	CACert            string
	// Priority is 0 to 10 (WP8x6 P83049); a higher number is listed first.
	Priority int
	Hidden   bool
	// PreferAccount is 0 (auto) or an account number 1 to 3 (WP8x6 P83085).
	PreferAccount int
	// Verified is the administrator's note that the profile joined a test
	// phone: a push is refused until the phone's profiles hold one that has.
	Verified bool
}

// Radio choices.
const (
	BandBoth = "both"
	Band24   = "2.4"
	Band5    = "5"

	RoamDefault = "default"
	RoamBoost   = "boost"
	RoamFast    = "fast"
	RoamCustom  = "custom"
)

// WifiRadio is a phone group's roaming and radio settings. A nil field is not
// written, so the phone keeps its own value.
type WifiRadio struct {
	Band      *string `json:"band,omitempty"`      // both, 2.4 or 5
	RoamMode  *string `json:"roamMode,omitempty"`  // default, boost, fast or custom
	Threshold *int    `json:"threshold,omitempty"` // roaming signal threshold, dBm, -100 to -30
	PoorScan  *int    `json:"poorScan,omitempty"`  // poor signal scan interval, seconds, 5 to 300
	GoodScan  *int    `json:"goodScan,omitempty"`  // good signal scan interval, seconds, 5 to 600
	Gain      *int    `json:"gain,omitempty"`      // roaming gain, dB, 4 to 20
	Dot11r    *bool   `json:"dot11r,omitempty"`    // fast BSS transition
	Dot11v    *bool   `json:"dot11v,omitempty"`    // BSS transition management
	Prompt    *bool   `json:"prompt,omitempty"`    // WP8x6 roaming prompt
}

// WifiKey is one P-value of the Wi-Fi settings.
type WifiKey struct {
	P string
	V string
}

// WifiInfo is the Wi-Fi setup of a phone: the profiles that reach it (its
// group's, then its own), whether the push is allowed and the group's radio
// settings. Render fills Keys from them for the phone's model.
type WifiInfo struct {
	Profiles []WifiProfile
	// Verified is set when Profiles holds one marked verified on a test phone.
	Verified bool
	Radio    *WifiRadio
	// Keys are the P-values the phone's model takes; empty when the model
	// has no Wi-Fi keys here, or when profiles exist but none is verified.
	Keys []WifiKey
}

// WifiFamily returns the Wi-Fi family of a model (FamilyWP8x6, FamilyWP8xx or
// FamilyWP856), or "" for a model Hello writes no Wi-Fi keys to.
func WifiFamily(v Vendor, model string) string {
	if v != Grandstream || strings.HasPrefix(normModel(model), "WP820") {
		return ""
	}
	return GrandstreamFamily(model)
}

// wifiSlots is how many networks a family's template names with all the keys
// Hello writes. The WP810/822/825 template lists 20 but gives SSIDs 18 to 20
// a security key outside the pattern of the others (P84772, P84872, P84972),
// so Hello fills 17.
var wifiSlots = map[string]int{FamilyWP8x6: 10, FamilyWP8xx: 17, FamilyWP856: 1}

// WifiSlots is the number of networks the family takes; 0 for none.
func WifiSlots(family string) int { return wifiSlots[family] }

// wifiSecurityCode is the family's value for a security mode.
func wifiSecurityCode(family, sec string) (int, bool) {
	var codes map[string]int
	switch family {
	case FamilyWP8x6:
		codes = map[string]int{WifiOpen: 0, WifiWPA2PSK: 4, WifiWPA3SAE: 11, WifiOWE: 14, WifiWPA2Enterprise: 5, WifiWPA3Enterprise: 12}
	case FamilyWP8xx:
		codes = map[string]int{WifiOpen: 0, WifiWPA2PSK: 4, WifiWPA3SAE: 11}
	case FamilyWP856:
		codes = map[string]int{WifiOpen: 0, WifiWPA2PSK: 2}
	}
	c, ok := codes[sec]
	return c, ok
}

// WifiSupports reports whether a family can take a profile of this security.
func WifiSupports(family, sec string) bool {
	_, ok := wifiSecurityCode(family, sec)
	return ok
}

func itoa(n int) string { return strconv.Itoa(n) }

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// sortWifi orders profiles for the slots: a higher priority first, then by name.
func sortWifi(ps []WifiProfile) []WifiProfile {
	out := slices.Clone(ps)
	slices.SortStableFunc(out, func(a, b WifiProfile) int {
		switch {
		case a.Priority != b.Priority:
			return b.Priority - a.Priority
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// WifiKeys returns the P-values of the Wi-Fi setup for a family. Nothing is
// written for profiles until one is verified; the radio settings do not wait
// for it. A profile whose security the family cannot take is skipped.
func WifiKeys(family string, w WifiInfo) []WifiKey {
	if WifiSlots(family) == 0 {
		return nil
	}
	var keys []WifiKey
	add := func(p int, v string) { keys = append(keys, WifiKey{P: itoa(p), V: v}) }
	if len(w.Profiles) > 0 && w.Verified {
		switch family {
		case FamilyWP8x6:
			keys = append(keys, wifiKeys8x6(w)...)
		case FamilyWP8xx:
			keys = append(keys, wifiKeys8xx(w)...)
		case FamilyWP856:
			keys = append(keys, wifiKeys856(w)...)
		}
	}
	if r := w.Radio; r != nil {
		radioKeys(family, r, add)
	}
	return keys
}

// usable lists the profiles the family can take, in slot order.
func usable(family string, ps []WifiProfile) []WifiProfile {
	var out []WifiProfile
	for _, p := range sortWifi(ps) {
		if WifiSupports(family, p.Security) {
			out = append(out, p)
		}
	}
	return out
}

func wifiKeys8x6(w WifiInfo) []WifiKey {
	var keys []WifiKey
	add := func(p int, v string) { keys = append(keys, WifiKey{P: itoa(p), V: v}) }
	ps := usable(FamilyWP8x6, w.Profiles)
	next := 0
	for slot := 1; slot <= wifiSlots[FamilyWP8x6]; slot++ {
		base := 83000 + 100*(slot-1)
		if next >= len(ps) {
			add(base, "") // a free slot is cleared
			continue
		}
		p := ps[next]
		// The template names the EAP method of network 5 P83304, not P83404
		// as the pattern of the other nine requires, so an enterprise
		// profile does not take that slot.
		if slot == 5 && WifiEnterprise(p.Security) {
			add(base, "")
			continue
		}
		next++
		code, _ := wifiSecurityCode(FamilyWP8x6, p.Security)
		add(base, p.SSID)
		add(base+2, itoa(code))
		add(base+49, itoa(p.Priority))
		add(base+85, itoa(p.PreferAccount))
		switch {
		case WifiUsesPSK(p.Security):
			add(base+3, p.PSK)
		case WifiEnterprise(p.Security):
			ca := map[string]string{CAServerSkip: "0", CAServerSystem: "1", CAServerTOFU: "2"}[p.CACert]
			if ca == "" {
				ca = "1"
			}
			add(base+4, "2") // EAP-PEAP
			add(base+16, ca)
			add(base+13, "0") // phase 2: MSCHAPv2
			add(base+12, p.AnonymousIdentity)
			add(base+14, p.Identity)
			add(base+15, p.Password)
		}
	}
	return keys
}

func wifiKeys8xx(w WifiInfo) []WifiKey {
	var keys []WifiKey
	add := func(p int, v string) { keys = append(keys, WifiKey{P: itoa(p), V: v}) }
	ps := usable(FamilyWP8xx, w.Profiles)
	var dot11r *bool
	if w.Radio != nil {
		dot11r = w.Radio.Dot11r
	}
	for slot := 1; slot <= wifiSlots[FamilyWP8xx]; slot++ {
		base := 83000 + 100*(slot-1)
		if slot > len(ps) {
			add(base, "")
			add(base+1, "0")
			continue
		}
		p := ps[slot-1]
		code, _ := wifiSecurityCode(FamilyWP8xx, p.Security)
		add(base, p.SSID)
		add(base+1, "1")
		add(base+50, b01(p.Hidden))
		add(base+2, itoa(code))
		if WifiUsesPSK(p.Security) {
			add(base+3, p.PSK)
		}
		if dot11r != nil {
			add(base+48, b01(*dot11r))
		}
	}
	return keys
}

func wifiKeys856(w WifiInfo) []WifiKey {
	ps := usable(FamilyWP856, w.Profiles)
	if len(ps) == 0 {
		return nil
	}
	// The template has one network (P7812, P7814, P7830): the first profile
	// by priority.
	p := ps[0]
	code, _ := wifiSecurityCode(FamilyWP856, p.Security)
	keys := []WifiKey{{"7812", p.SSID}, {"7814", itoa(code)}}
	if WifiUsesPSK(p.Security) {
		keys = append(keys, WifiKey{"7830", p.PSK})
	}
	return keys
}

var wifiBand = map[string][2]string{BandBoth: {"1", "1"}, Band24: {"1", "0"}, Band5: {"0", "1"}}

// radioKeys writes the group's roaming settings the family's template has.
func radioKeys(family string, r *WifiRadio, add func(int, string)) {
	switch family {
	case FamilyWP8x6:
		if r.Band != nil {
			add(25438, map[string]string{BandBoth: "0", Band24: "1", Band5: "2"}[*r.Band]) // 0 both, 1 2.4G, 2 5G
		}
		if r.RoamMode != nil && (*r.RoamMode == RoamDefault || *r.RoamMode == RoamBoost) {
			add(82332, map[string]string{RoamDefault: "0", RoamBoost: "1"}[*r.RoamMode])
		}
		if r.Threshold != nil {
			add(22232, itoa(*r.Threshold))
		}
		if r.PoorScan != nil {
			add(22231, itoa(*r.PoorScan))
		}
		if r.Prompt != nil {
			add(82334, b01(*r.Prompt))
		}
	case FamilyWP8xx:
		if r.Threshold != nil {
			add(22232, itoa(*r.Threshold))
		}
		if r.PoorScan != nil {
			add(22231, itoa(*r.PoorScan))
		}
		if r.Dot11v != nil {
			add(84992, b01(*r.Dot11v))
		}
		// 802.11r is a key of each network on this family (see wifiKeys8xx).
	case FamilyWP856:
		if r.Band != nil {
			b := wifiBand[*r.Band]
			add(25420, b[0])
			add(25421, b[1])
		}
		if r.RoamMode != nil {
			add(82332, map[string]string{RoamDefault: "0", RoamBoost: "1", RoamFast: "2", RoamCustom: "3"}[*r.RoamMode])
		}
		if r.Threshold != nil {
			add(22232, itoa(*r.Threshold))
		}
		if r.Gain != nil {
			add(22443, itoa(*r.Gain))
		}
		if r.GoodScan != nil {
			add(22230, itoa(*r.GoodScan))
		}
		if r.PoorScan != nil {
			add(22231, itoa(*r.PoorScan))
		}
		if r.Dot11r != nil {
			add(25315, b01(*r.Dot11r))
		}
		if r.Dot11v != nil {
			add(82333, b01(*r.Dot11v))
		}
	}
}

// WifiFieldError is one validation failure of a Wi-Fi setting.
type WifiFieldError struct{ Path, Message string }

var ssidChars = regexp.MustCompile(`^[^\x00-\x1f\x7f]+$`)

// printableASCII reports whether s is made of printable ASCII only.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

var hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// CheckWifiProfile validates a profile as it would be stored. hasPSK and
// hasPassword say whether a sealed secret exists (or comes with the request),
// since the profile itself does not carry them.
func CheckWifiProfile(p WifiProfile, hasPSK, hasPassword bool) []WifiFieldError {
	var errs []WifiFieldError
	bad := func(path, msg string) { errs = append(errs, WifiFieldError{path, msg}) }
	if n := len(strings.TrimSpace(p.Name)); n == 0 || len(p.Name) > MaxWifiName || !ssidChars.MatchString(p.Name) {
		bad("name", "must be 1 to 64 printable characters")
	}
	if len(p.SSID) == 0 || len(p.SSID) > MaxSSIDBytes || !ssidChars.MatchString(p.SSID) {
		bad("ssid", "must be 1 to 32 bytes of printable characters")
	}
	if !slices.Contains(WifiSecurities, p.Security) {
		bad("security", "must be open, wpa2-psk, wpa3-sae, owe, wpa2-enterprise or wpa3-enterprise")
		return errs
	}
	if p.Priority < 0 || p.Priority > 10 {
		bad("priority", "must be 0 to 10")
	}
	if p.PreferAccount < 0 || p.PreferAccount > 3 {
		bad("preferAccount", "must be 0 (automatic) or an account from 1 to 3")
	}
	if WifiUsesPSK(p.Security) {
		if !hasPSK {
			bad("psk", "is required for "+p.Security)
		}
		if p.PSK != "" && (len(p.PSK) < MinPSKLen || len(p.PSK) > MaxPSKLen || !printableASCII(p.PSK)) && !hex64.MatchString(p.PSK) {
			bad("psk", "must be 8 to 63 printable ASCII characters")
		}
	}
	if WifiEnterprise(p.Security) {
		if p.EAPMethod != EAPPEAP {
			bad("eapMethod", "must be peap")
		}
		if p.Identity == "" || len(p.Identity) > 128 || !ssidChars.MatchString(p.Identity) {
			bad("identity", "is required (at most 128 printable characters)")
		}
		if !hasPassword {
			bad("password", "is required for "+p.Security)
		}
		if len(p.AnonymousIdentity) > 128 || (p.AnonymousIdentity != "" && !ssidChars.MatchString(p.AnonymousIdentity)) {
			bad("anonymousIdentity", "must be at most 128 printable characters")
		}
		if !slices.Contains([]string{CAServerSystem, CAServerTOFU, CAServerSkip}, p.CACert) {
			bad("caCert", "must be system, tofu or skip")
		}
	} else if p.EAPMethod != "" || p.Identity != "" || p.AnonymousIdentity != "" {
		bad("eapMethod", "802.1X settings belong to an enterprise profile")
	}
	return errs
}

// CheckWifiRadio validates a group's radio settings.
func CheckWifiRadio(r WifiRadio) []WifiFieldError {
	var errs []WifiFieldError
	bad := func(path, msg string) { errs = append(errs, WifiFieldError{"radio." + path, msg}) }
	if r.Band != nil && !slices.Contains([]string{BandBoth, Band24, Band5}, *r.Band) {
		bad("band", "must be both, 2.4 or 5")
	}
	if r.RoamMode != nil && !slices.Contains([]string{RoamDefault, RoamBoost, RoamFast, RoamCustom}, *r.RoamMode) {
		bad("roamMode", "must be default, boost, fast or custom")
	}
	rng := func(name string, v *int, lo, hi int) {
		if v != nil && (*v < lo || *v > hi) {
			bad(name, "must be "+itoa(lo)+" to "+itoa(hi))
		}
	}
	rng("threshold", r.Threshold, -100, -30)
	rng("poorScan", r.PoorScan, 5, 300)
	rng("goodScan", r.GoodScan, 5, 600)
	rng("gain", r.Gain, 4, 20)
	return errs
}

// WifiPSKAAD is the sealing context of a profile's pre-shared key.
func WifiPSKAAD(id int64) string { return "wifi-psk:" + strconv.FormatInt(id, 10) }

// WifiPasswordAAD is the sealing context of a profile's 802.1X password.
func WifiPasswordAAD(id int64) string { return "wifi-password:" + strconv.FormatInt(id, 10) }

// MergeWifi lists the profiles that reach a phone: its group's, then its own,
// where a phone's profile replaces a group's with the same SSID.
func MergeWifi(group, phone []WifiProfile) []WifiProfile {
	out := slices.Clone(group)
	for _, p := range phone {
		if i := slices.IndexFunc(out, func(g WifiProfile) bool { return g.SSID == p.SSID }); i >= 0 {
			out[i] = p
			continue
		}
		out = append(out, p)
	}
	return out
}
