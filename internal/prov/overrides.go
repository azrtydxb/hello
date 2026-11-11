package prov

// Setting overrides (phone-feature-parity S-12): extra vendor keys an
// administrator sets per phone group and per phone. The resolution order is
// the template, then the group, then the phone: a key the group sets
// replaces the template's value, and a key the phone sets replaces the
// group's. A secret value is sealed in the database and masked in every
// view.
//
// Overrides are applied to the finished device file, not through the
// template language, so they work for the built-in templates and for an
// administrator's copy alike. The two key-value formats Hello can edit
// without guessing are supported: Grandstream's XML P-values and Yealink's
// "key = value" lines. A key for another vendor is refused.

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Override is one resolved setting: a vendor key and the value that replaces
// or adds it in the rendered file.
type Override struct {
	Key    string
	Value  string
	Secret bool
}

// Override limits.
const (
	MaxOverrideValue = 1024
	// MaxOverridesPerScope bounds the keys of one group or one phone.
	MaxOverridesPerScope = 200
)

// OverrideScope is what an override belongs to.
type OverrideScope string

// The scopes.
const (
	ScopeGroup OverrideScope = "group"
	ScopePhone OverrideScope = "phone"
)

// OverrideAAD is the sealing context of a secret override value: the scope,
// the owner and the key, so a sealed value copied to another phone, group
// or key does not open.
func OverrideAAD(scope OverrideScope, ownerID int64, key string) string {
	return fmt.Sprintf("override:%s:%d:%s", scope, ownerID, key)
}

var (
	gsKeyRE = regexp.MustCompile(`^P[0-9]{1,6}$`)
	// A Yealink key is dotted lowercase words and digits
	// ("features.text_message.enable", "static.network.qos.signaltos").
	ylKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

	// gsDenied are the Grandstream keys Hello owns: the account
	// (P47 server, P35 user, P36 auth name, P34 password, P3 display name,
	// P270 label, P271 enable), the provisioning URL the token is in (P212
	// upgrade via, P237 config server path), the CA certificate (P8433), the
	// admin password (P2) and the identity of accounts 2 to 6 (gsAccountKeys).
	gsDenied = append([]string{"P2", "P3", "P34", "P35", "P36", "P47", "P212", "P237", "P270", "P271", "P8433"}, gsAccountKeys()...)

	// ylDenied match the Yealink keys Hello owns: account 1..n identity and
	// registrar, the auto-provisioning URL and credentials, the CA, and the
	// admin password.
	ylDenied = regexp.MustCompile(`^(account\.\d+\.(enable|label|display_name|auth_name|user_name|password|sip_server\.\d+\.(address|port|transport_type))` +
		`|static\.auto_provision\.(server\.[a-z_]+|dhcp_option\.enable)` +
		`|static\.trusted_certificates\.[a-z_]+|static\.security\.(user_password|user_name|trust_certificates))$`)
)

// gsAccountKeys are the identity keys of Grandstream accounts 2 to 6, which
// the lines of a multi-account phone (item 21) are written to: Account Active,
// SIP Server, SIP User ID, SIP Authentication ID, SIP Authentication Password,
// Name (the display name) and Account Name (the label). Account n of the WP8x6,
// WP810/822/825, WP820 and WP856 templates (wp8x6_config_1.0.3.39,
// wp810_822_825_config_1.0.11.83, wp820_config_1.0.7.90, wp856_config_1.0.3.16)
// starts at P401 (2), P501 (3), P601 (4), P1701 (5) and P1801 (6): Active at
// the base, Server +1, User +3, Auth ID +4, Password +5, Name +6, Account Name
// +16. Denying them before the lines are built keeps an override from
// replacing a line's registration.
func gsAccountKeys() []string {
	var out []string
	for _, base := range []int{401, 501, 601, 1701, 1801} {
		for _, off := range []int{0, 1, 3, 4, 5, 6, 16} {
			out = append(out, "P"+strconv.Itoa(base+off))
		}
	}
	return out
}

// OverrideVendors lists the vendors whose files overrides can be applied to.
var OverrideVendors = []Vendor{Grandstream, Yealink}

// ErrOverrideVendor: overrides are not supported for the phone's vendor.
var ErrOverrideVendor = errors.New("prov: setting overrides are not supported for this vendor")

// KeyVendor reports which vendor's key format key has: Grandstream for
// "P123", Yealink for a dotted key, "" for anything else.
func KeyVendor(key string) Vendor {
	switch {
	case gsKeyRE.MatchString(key):
		return Grandstream
	case ylKeyRE.MatchString(key) && len(key) <= 128:
		return Yealink
	}
	return ""
}

// ValidateOverride checks one key and value. The key must be in a supported
// vendor's format and not be one Hello owns; when v is not empty it must also
// be that vendor's format (a phone override). The value is one line of at
// most MaxOverrideValue bytes without control characters.
func ValidateOverride(v Vendor, key, value string) error {
	kv := KeyVendor(key)
	if kv == "" {
		return fmt.Errorf("the key %q is not a Grandstream P-value (P123) or a Yealink key (features.text_message.enable)", key)
	}
	if v != "" && v != kv {
		return fmt.Errorf("the key %q is a %s key, but the phone is a %s", key, kv, v)
	}
	if OverrideDenied(key) {
		return fmt.Errorf("the key %q is set by Hello (account, provisioning URL, CA or admin password) and cannot be overridden", key)
	}
	if len(value) > MaxOverrideValue {
		return fmt.Errorf("the value is longer than %d bytes", MaxOverrideValue)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("the value must be one line without control characters")
		}
	}
	return nil
}

// OverrideDenied reports whether key is one Hello owns.
func OverrideDenied(key string) bool {
	switch KeyVendor(key) {
	case Grandstream:
		return slices.Contains(gsDenied, key)
	case Yealink:
		return ylDenied.MatchString(key)
	}
	return false
}

// ResolveOverrides merges the group's and the phone's overrides for a phone
// of vendor v: the phone's value wins over the group's. Keys of another
// vendor's format are dropped (a group may hold keys for several vendors).
// The result is sorted by key, so a render is deterministic.
func ResolveOverrides(v Vendor, group, phone []Override) []Override {
	m := map[string]Override{}
	for _, set := range [][]Override{group, phone} {
		for _, o := range set {
			if KeyVendor(o.Key) == v {
				m[o.Key] = o
			}
		}
	}
	out := make([]Override, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b Override) int { return strings.Compare(a.Key, b.Key) })
	return out
}

var gsElemRE = regexp.MustCompile(`<(P[0-9]+)>[^<]*</(P[0-9]+)>`)

// ApplyOverrides sets the keys in a rendered file: a key the file already
// writes gets the new value, any other is added (Grandstream before the
// closing </config>, Yealink at the end). A file that is not the vendor's
// configuration (a Grandstream file without a <config> element, an
// administrator's other file) is returned unchanged.
func ApplyOverrides(v Vendor, body []byte, ov []Override) ([]byte, error) {
	if len(ov) == 0 {
		return body, nil
	}
	var out []byte
	switch v {
	case Grandstream:
		out = applyGrandstream(body, ov)
	case Yealink:
		out = applyYealink(body, ov)
	default:
		return body, nil
	}
	if len(out) > MaxRenderBytes {
		return nil, errTooLarge
	}
	return out, nil
}

func applyGrandstream(body []byte, ov []Override) []byte {
	end := bytes.LastIndex(body, []byte("</config>"))
	if end < 0 {
		return body
	}
	want := map[string]string{}
	for _, o := range ov {
		want[o.Key] = o.Value
	}
	seen := map[string]bool{}
	body = gsElemRE.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := gsElemRE.FindSubmatch(m)
		k := string(sub[1])
		val, ok := want[k]
		if !ok || string(sub[1]) != string(sub[2]) {
			return m
		}
		seen[k] = true
		return []byte("<" + k + ">" + xmlText(val) + "</" + k + ">")
	})
	end = bytes.LastIndex(body, []byte("</config>"))
	var add bytes.Buffer
	for _, o := range ov {
		if !seen[o.Key] {
			add.WriteString("    <" + o.Key + ">" + xmlText(o.Value) + "</" + o.Key + ">\n")
		}
	}
	if add.Len() == 0 {
		return body
	}
	// Insert at the start of the line holding </config>, after the indent.
	at := end
	for at > 0 && (body[at-1] == ' ' || body[at-1] == '\t') {
		at--
	}
	res := make([]byte, 0, len(body)+add.Len())
	res = append(res, body[:at]...)
	res = append(res, add.Bytes()...)
	res = append(res, body[at:]...)
	return res
}

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func applyYealink(body []byte, ov []Override) []byte {
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	want := map[string]string{}
	for _, o := range ov {
		want[o.Key] = o.Value
	}
	seen := map[string]bool{}
	for i, l := range lines {
		k, _, ok := strings.Cut(l, "=")
		k = strings.TrimSpace(k)
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if val, has := want[k]; has {
			lines[i] = k + " = " + val
			seen[k] = true
		}
	}
	for _, o := range ov {
		if !seen[o.Key] {
			lines = append(lines, o.Key+" = "+o.Value)
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
