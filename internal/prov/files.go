package prov

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// NormalizeMAC accepts a MAC as 12 hex digits, as six colon- or
// dash-separated pairs, or as three dot-separated groups of four, in any
// case, and returns it as 12 lowercase hex digits.
func NormalizeMAC(s string) (string, error) {
	var groups []string
	switch {
	case len(s) == 12:
		groups = []string{s}
	case len(s) == 17 && (strings.Count(s, ":") == 5 || strings.Count(s, "-") == 5):
		sep := s[2:3]
		groups = strings.Split(s, sep)
		for _, g := range groups {
			if len(g) != 2 {
				return "", errBadMAC
			}
		}
	case len(s) == 14 && strings.Count(s, ".") == 2:
		groups = strings.Split(s, ".")
		for _, g := range groups {
			if len(g) != 4 {
				return "", errBadMAC
			}
		}
	default:
		return "", errBadMAC
	}
	mac := strings.ToLower(strings.Join(groups, ""))
	if !isHex12(mac) {
		return "", errBadMAC
	}
	return mac, nil
}

var errBadMAC = errors.New("prov: a MAC is 12 hex digits, optionally separated by ':', '-' or '.'")

func isHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range []byte(s) {
		if !isHexByte(c) {
			return false
		}
	}
	return true
}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// zeroMAC is the all-zero name Poly's master and Yealink's common boot file
// use; it never claims a MAC.
const zeroMAC = "000000000000"

// MatchFile classifies a requested file name for a phone of vendor v and
// model whose MAC is mac (spec S-7). It returns false when the name is not
// in the vendor's file set or embeds another MAC.
func MatchFile(v Vendor, model, mac, name string) (FileKind, bool) {
	kind, claimed, ok := classify(v, model, name)
	if !ok || (claimed != "" && claimed != mac) {
		return KindOther, false
	}
	return kind, true
}

// classify maps name to its kind in v's file set and returns the MAC it
// claims ("" when it embeds none). Names embedding a MAC are matched in
// any case, because vendor guides and integrators disagree (Fanvil, Snom).
func classify(v Vendor, model, name string) (kind FileKind, claimed string, ok bool) {
	if !safeName(name) {
		return KindOther, "", false
	}
	lower := strings.ToLower(name)
	switch v {
	case Yealink:
		return classifyYealink(model, lower)
	case Poly:
		return classifyPoly(lower)
	case Grandstream:
		return classifyGrandstream(model, lower)
	case Snom:
		return classifySnom(model, lower)
	case Fanvil:
		return classifyFanvil(model, lower)
	case Generic:
		if m := genericMAC(lower); m != "" {
			return KindDevice, m, true
		}
		return KindCommon, "", true
	}
	return KindOther, "", false
}

// safeName is a single path segment of plain characters.
func safeName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	for _, c := range []byte(name) {
		plain := c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '_' || c == '+'
		if !plain {
			return false
		}
	}
	return true
}

// macStem returns the MAC a name of the form <mac><suffix> carries.
func macStem(lower, suffix string) (string, bool) {
	stem, found := strings.CutSuffix(lower, suffix)
	if !found || !isHex12(stem) {
		return "", false
	}
	return stem, true
}

func classifyYealink(model, lower string) (FileKind, string, bool) {
	if lower == "y000000000000.boot" {
		return KindOther, "", true // optional boot file; Hello uses the .cfg flow
	}
	if m, ok := macStem(lower, ".boot"); ok {
		return KindOther, m, true
	}
	for _, suffix := range []string{"-local.cfg", "-contact.xml"} {
		if m, ok := macStem(lower, suffix); ok {
			return KindOther, m, true // answered with an empty 404
		}
	}
	if m, ok := macStem(lower, ".cfg"); ok && m != zeroMAC {
		return KindDevice, m, true
	}
	if id, ok := YealinkModelID(model); ok && lower == yealinkCommon(id) {
		return KindCommon, "", true
	}
	return KindOther, "", false
}

func yealinkCommon(id int) string { return "y" + leftPad(strconv.Itoa(id), 12) + ".cfg" }

func leftPad(s string, n int) string { return strings.Repeat("0", max(n-len(s), 0)) + s }

// yealinkCommonRE is any Yealink model common file, for the boot path,
// where the model is not known yet.
var yealinkCommonRE = regexp.MustCompile(`^y[0-9]{12}\.cfg$`)

// yealinkModelIDs maps a Yealink model to the hardware ID of its common
// file y0000000000XX.cfg, which is also the first number of its firmware
// version (a T54W runs 96.x firmware). Models missing here get no common
// file; their per-MAC file still works (spec edge cases).
var yealinkModelIDs = map[string]int{
	"T19PE2": 53, "T21PE2": 52, "T23P": 44, "T23G": 44, "T27G": 69, "T29G": 46,
	"T40P": 54, "T40G": 76, "T41P": 36, "T41S": 68, "T42G": 29, "T42S": 67,
	"T46G": 28, "T46S": 66, "T48G": 35, "T48S": 65, "T52S": 70, "T54S": 70,
	"T53": 95, "T53W": 95, "T54W": 96, "T57W": 97,
	"T43U": 107, "T46U": 108, "T48U": 109, "T42U": 116,
	"T30": 124, "T30P": 124, "T31": 124, "T31P": 124, "T31G": 124, "T33G": 124,
	"CP920": 78,
}

// YealinkModelID returns the hardware ID of a Yealink model ("T54W",
// "SIP-T54W" and "t54w" alike).
func YealinkModelID(model string) (int, bool) {
	m := strings.ToUpper(strings.NewReplacer(" ", "", "_", "").Replace(model))
	m = strings.TrimPrefix(m, "SIP-")
	id, ok := yealinkModelIDs[m]
	return id, ok
}

// polyUploads are the per-MAC files Poly phones PUT back (and some GET
// first); Hello discards them.
var polyUploads = []string{"-phone.cfg", "-web.cfg", "-app.log", "-boot.log", "-directory.xml"}

func classifyPoly(lower string) (FileKind, string, bool) {
	if lower == zeroMAC+".cfg" {
		return KindMaster, "", true
	}
	if m, ok := macStem(lower, "-hello.cfg"); ok {
		return KindDevice, m, true
	}
	for _, suffix := range polyUploads {
		if m, ok := macStem(lower, suffix); ok {
			return KindUpload, m, true
		}
	}
	if m, ok := macStem(lower, ".cfg"); ok {
		return KindMaster, m, true
	}
	return KindOther, "", false
}

func classifyGrandstream(model, lower string) (FileKind, string, bool) {
	rest, found := strings.CutPrefix(lower, "cfg")
	if !found {
		return KindOther, "", false
	}
	if rest == ".xml" {
		return KindCommon, "", true
	}
	if m, ok := macStem(rest, ".xml"); ok {
		return KindDevice, m, true
	}
	for _, suffix := range []string{"", ".bin"} {
		if m, ok := macStem(rest, suffix); ok {
			return KindOther, m, true // legacy binary forms: empty 404
		}
	}
	if model != "" && rest == modelToken(model)+".xml" {
		return KindCommon, "", true
	}
	return KindOther, "", false
}

// modelToken is a model as it appears in a file name: lowercase, without
// spaces.
func modelToken(model string) string {
	return strings.ToLower(strings.ReplaceAll(model, " ", ""))
}

func classifySnom(model, lower string) (FileKind, string, bool) {
	if isHex12(lower) {
		return KindDevice, lower, true // the {mac} URL Hello hands out
	}
	rest, found := strings.CutPrefix(lower, "snom")
	if !found || model == "" {
		return KindOther, "", false
	}
	rest, found = strings.CutPrefix(rest, modelToken(model))
	if !found {
		return KindOther, "", false
	}
	switch rest {
	case ".htm":
		return KindCommon, "", true
	case "-firmware.htm":
		return KindOther, "", true
	}
	if m, ok := macStem(strings.TrimPrefix(rest, "-"), ".htm"); ok && strings.HasPrefix(rest, "-") {
		return KindDevice, m, true
	}
	return KindOther, "", false
}

func classifyFanvil(model, lower string) (FileKind, string, bool) {
	if m, ok := macStem(lower, ".cfg"); ok && m != zeroMAC {
		return KindDevice, m, true
	}
	if name, ok := FanvilCommon(model); ok && lower == strings.ToLower(name) {
		return KindCommon, "", true
	}
	return KindOther, "", false
}

// FanvilCommon is a Fanvil model's common file name: "F0V00", the model,
// then zeros to twelve characters, as F0V00X600000.cfg is the X6's in
// Fanvil's auto-provisioning guide. False for a model too long to fit.
func FanvilCommon(model string) (string, bool) {
	m := strings.ToUpper(strings.ReplaceAll(model, " ", ""))
	if m == "" || len(m) > 7 {
		return "", false
	}
	return "F0V00" + m + strings.Repeat("0", 7-len(m)) + ".cfg", true
}

// fanvilCommonRE is any Fanvil common file, for the boot path.
var fanvilCommonRE = regexp.MustCompile(`^f0v00[0-9a-z]{7}\.cfg$`)

// genericMAC returns a MAC a generic phone's file name embeds: a run of
// exactly 12 hex digits, not all zero, bounded by non-hex characters.
func genericMAC(lower string) string {
	for i := 0; i+12 <= len(lower); i++ {
		if i > 0 && isHexByte(lower[i-1]) {
			continue
		}
		cand := lower[i : i+12]
		if isHex12(cand) && (i+12 == len(lower) || !isHexByte(lower[i+12])) && cand != zeroMAC {
			return cand
		}
	}
	return ""
}

// bootClassify classifies a /p/boot/ name without a phone: the vendor whose
// file set it belongs to, its kind, and the MAC it claims. A per-MAC name
// shared by several vendors (<mac>.cfg) reports Generic: the phone found by
// the MAC decides.
func bootClassify(name string) (v Vendor, kind FileKind, claimed string, ok bool) {
	if !safeName(name) {
		return Generic, KindOther, "", false
	}
	lower := strings.ToLower(name)
	switch {
	case yealinkCommonRE.MatchString(lower):
		return Yealink, KindCommon, "", true
	case lower == zeroMAC+".cfg":
		return Poly, KindMaster, "", true
	case fanvilCommonRE.MatchString(lower):
		return Fanvil, KindCommon, "", true
	case strings.HasPrefix(lower, "cfg"):
		if k, m, ok := classifyGrandstream("", lower); ok {
			return Grandstream, k, m, true
		}
		if strings.HasSuffix(lower, ".xml") {
			return Grandstream, KindCommon, "", true // cfg<model>.xml
		}
	case strings.HasPrefix(lower, "snom") && strings.HasSuffix(lower, ".htm"):
		stem := strings.TrimSuffix(strings.TrimPrefix(lower, "snom"), ".htm")
		if i := strings.LastIndexByte(stem, '-'); i >= 0 && isHex12(stem[i+1:]) {
			return Snom, KindDevice, stem[i+1:], true
		}
		return Snom, KindCommon, "", true
	}
	if m := genericMAC(lower); m != "" {
		return Generic, KindDevice, m, true
	}
	return Generic, KindOther, "", false
}
