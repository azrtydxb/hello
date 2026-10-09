package api

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// Field-level validation of trunks and routes (stage 1). Stage 2, the
// whole-configuration routing.Compile, runs in the store transaction.

const maxRegexLen = 500

var (
	trunkNameRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	voiceAgentNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	digitsRe         = regexp.MustCompile(`^[0-9+*#]{0,32}$`)
	didRe            = regexp.MustCompile(`^\+?[0-9*#]{1,32}$`)
	externalNumRe    = regexp.MustCompile(`^\+?[0-9*#]{2,32}$`)
	callerIDRe       = regexp.MustCompile(`^(\+?[0-9]{2,20})?$`)
	headerNameRe     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	timeZoneRe       = regexp.MustCompile(`^[A-Za-z0-9_+/-]{1,64}$`)
	hhmmRe           = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	hostLabelRe      = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

type fieldErrs []routing.FieldError

func (f *fieldErrs) add(path, format string, args ...any) {
	*f = append(*f, routing.FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// printable reports whether s is at most max runes, none of them control or
// space characters other than ' ' when spaces is set.
func printable(s string, maxLen int, spaces bool) bool {
	if len([]rune(s)) > maxLen {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || (!spaces && unicode.IsSpace(r)) {
			return false
		}
	}
	return true
}

// minPrefixBits is the broadest source CIDR a trunk may trust: /8 for IPv4,
// /32 for IPv6.
func minPrefixBits(a netip.Addr) int {
	if a.Is4() {
		return 8
	}
	return 32
}

func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !hostLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// compileRegex checks an RE2 pattern of at most 500 characters.
func compileRegex(f *fieldErrs, path, re string) *regexp.Regexp {
	if utf8.RuneCountInString(re) > maxRegexLen {
		f.add(path, "regex is longer than %d characters", maxRegexLen)
		return nil
	}
	c, err := regexp.Compile(re)
	if err != nil {
		f.add(path, "regex does not compile: %s", strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		return nil
	}
	return c
}

// templateParts splits a replacement template into its group references
// and its literal text. References are ${1} or ${name}; "$$" is a literal
// "$". A bare $1 or $name is refused, as the routing engine does, because
// "$1x" silently means "${1x}".
func templateParts(tpl string) (refs []string, literal string, ok bool) {
	var lit strings.Builder
	for i := 0; i < len(tpl); i++ {
		if tpl[i] != '$' {
			lit.WriteByte(tpl[i])
			continue
		}
		rest := tpl[i+1:]
		switch {
		case strings.HasPrefix(rest, "$"):
			lit.WriteByte('$')
			i++
		case strings.HasPrefix(rest, "{"):
			end := strings.IndexByte(rest, '}')
			if end < 2 {
				return nil, "", false
			}
			refs = append(refs, rest[1:end])
			i += end + 1
		default:
			return nil, "", false
		}
	}
	return refs, lit.String(), true
}

func validateTransform(f *fieldErrs, path string, t routing.Transform) {
	if t.Strip < 0 || t.Strip > 32 {
		f.add(path+".strip", "must be 0-32")
	}
	if !digitsRe.MatchString(t.Prefix) {
		f.add(path+".prefix", "must be up to 32 of 0-9 + * #")
	}
	if t.Regex == "" {
		if t.Template != "" {
			f.add(path+".template", "needs a regex")
		}
		return
	}
	re := compileRegex(f, path+".regex", t.Regex)
	if t.Template == "" {
		f.add(path+".template", "is required with a regex")
		return
	}
	if len(t.Template) > 200 {
		f.add(path+".template", "is longer than 200 characters")
		return
	}
	refs, literal, ok := templateParts(t.Template)
	if !ok {
		f.add(path+".template", "has a malformed group reference: write ${1} or ${name}")
		return
	}
	if !digitsRe.MatchString(literal) {
		f.add(path+".template", "literal text may only contain 0-9 + * #")
	}
	if re == nil {
		return
	}
	for _, ref := range refs {
		if n, err := strconv.Atoi(ref); err == nil {
			if n > re.NumSubexp() {
				f.add(path+".template", "references group %d, but the regex has %d", n, re.NumSubexp())
			}
		} else if re.SubexpIndex(ref) < 0 {
			f.add(path+".template", "references group %q, which the regex does not define", ref)
		}
	}
}

func validateSchedule(f *fieldErrs, path string, s *routing.Schedule) {
	if s == nil {
		return
	}
	if !timeZoneRe.MatchString(s.TimeZone) {
		f.add(path+".timeZone", "must be an IANA time zone name")
	} else if _, err := time.LoadLocation(s.TimeZone); err != nil {
		f.add(path+".timeZone", "unknown time zone")
	}
	if len(s.Windows) == 0 || len(s.Windows) > 50 {
		f.add(path+".windows", "must have 1-50 windows")
	}
	for i, w := range s.Windows {
		p := fmt.Sprintf("%s.windows[%d]", path, i)
		if len(w.Days) == 0 || len(w.Days) > 7 {
			f.add(p+".days", "must list 1-7 days")
		}
		for _, d := range w.Days {
			if d < time.Sunday || d > time.Saturday {
				f.add(p+".days", "days are 0 (Sunday) to 6 (Saturday)")
				break
			}
		}
		if !hhmmRe.MatchString(w.Start) {
			f.add(p+".start", "must be HH:MM")
		}
		if !hhmmRe.MatchString(w.End) {
			f.add(p+".end", "must be HH:MM")
		}
	}
}

func validateTrunk(in *store.TrunkInput, hasPassword bool) fieldErrs {
	var f fieldErrs
	if !trunkNameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	switch in.Mode {
	case "registration":
		if in.Username == "" {
			f.add("username", "is required for a registration trunk")
		}
		if !hasPassword {
			f.add("password", "is required for a registration trunk")
		}
	case "ip":
	default:
		f.add("mode", `must be "registration" or "ip"`)
	}
	if !printable(in.Username, 128, false) {
		f.add("username", "must be at most 128 printable characters without spaces")
	}
	if !printable(in.Realm, 128, false) {
		f.add("realm", "must be at most 128 printable characters without spaces")
	}
	if in.FromDomain != "" && !validHost(in.FromDomain) {
		f.add("fromDomain", "must be a host name or IP address")
	}
	if in.RegisterExpires < 60 || in.RegisterExpires > 86400 {
		f.add("registerExpires", "must be 60-86400 seconds")
	}
	if in.OptionsInterval < 5 || in.OptionsInterval > 3600 {
		f.add("optionsInterval", "must be 5-3600 seconds")
	}
	if in.MaxCalls < 0 || in.MaxCalls > 100000 {
		f.add("maxCalls", "must be 0 (unlimited) to 100000")
	}
	if !callerIDRe.MatchString(in.DefaultCallerID) {
		f.add("defaultCallerId", "must be empty or 2-20 digits with an optional leading +")
	}
	if len(in.SourceCIDRs) > 100 {
		f.add("sourceCidrs", "at most 100 entries")
	}
	for i, c := range in.SourceCIDRs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				f.add(fmt.Sprintf("sourceCidrs[%d]", i), "must be a CIDR such as 203.0.113.0/24 or an IP address")
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if minBits := minPrefixBits(p.Addr()); p.Bits() < minBits {
			f.add(fmt.Sprintf("sourceCidrs[%d]", i),
				"is broader than /%d: every address in it could send calls to the inbound routes, and UDP source addresses can be spoofed", minBits)
			continue
		}
		in.SourceCIDRs[i] = p.Masked().String()
	}
	if len(in.Destinations) == 0 || len(in.Destinations) > 16 {
		f.add("destinations", "must have 1-16 destinations")
	}
	for i := range in.Destinations {
		d := &in.Destinations[i]
		p := fmt.Sprintf("destinations[%d]", i)
		if !validHost(d.Host) {
			f.add(p+".host", "must be a host name or IP address")
		}
		if d.Port < 0 || d.Port > 65535 {
			f.add(p+".port", "must be 0 (SRV, else 5060) to 65535")
		}
		if d.Priority < 0 || d.Priority > 65535 {
			f.add(p+".priority", "must be 0-65535")
		}
		if d.Weight == 0 {
			d.Weight = 1
		}
		if d.Weight < 1 || d.Weight > 65535 {
			f.add(p+".weight", "must be 1-65535")
		}
	}
	return f
}

func validateOutbound(o *store.OutboundRoute) fieldErrs {
	var f fieldErrs
	if name, ok := validName(o.Name); ok {
		o.Name = name
	} else {
		f.add("name", "must be 1-100 printable characters")
	}
	switch o.MatchKind {
	case "prefix":
		if o.Match == "" || o.Match == "+" || !digitsRe.MatchString(o.Match) {
			f.add("match", "a prefix is 1-32 of 0-9 + * # (a regex such as ^ matches every number)")
		}
	case "regex":
		compileRegex(&f, "match", o.Match)
	default:
		f.add("matchKind", `must be "prefix" or "regex"`)
	}
	if len(o.SourceExtensions) > 1000 {
		f.add("sourceExtensions", "at most 1000 entries")
	}
	for i, e := range o.SourceExtensions {
		if !numberRe.MatchString(e) {
			f.add(fmt.Sprintf("sourceExtensions[%d]", i), "must be an extension number (2-10 digits)")
		}
	}
	validateSchedule(&f, "schedule", o.Schedule)
	validateTransform(&f, "numberTransform", o.NumberTransform)
	validateTransform(&f, "callerIdTransform", o.CallerIDTransform)
	if len(o.Trunks) == 0 {
		f.add("trunks", "must list at least one trunk")
	} else if len(o.Trunks) > 32 {
		f.add("trunks", "at most 32 trunks")
	}
	seen := map[int64]bool{}
	for i, t := range o.Trunks {
		if t <= 0 || seen[t] {
			f.add(fmt.Sprintf("trunks[%d]", i), "must be a distinct trunk id")
		}
		seen[t] = true
	}
	if len(o.FailoverCodes) > 32 {
		f.add("failoverCodes", "at most 32 codes")
	}
	codes := map[int]bool{}
	for i, c := range o.FailoverCodes {
		if c < 400 || c > 699 || codes[c] {
			f.add(fmt.Sprintf("failoverCodes[%d]", i), "must be a distinct SIP failure code (400-699)")
		}
		codes[c] = true
	}
	return f
}

func validateInbound(in *store.InboundRoute) fieldErrs {
	var f fieldErrs
	if name, ok := validName(in.Name); ok {
		in.Name = name
	} else {
		f.add("name", "must be 1-100 printable characters")
	}
	switch in.DIDKind {
	case "any":
		if in.DID != "" {
			f.add("did", `must be empty when didKind is "any"`)
		}
	case "exact", "prefix":
		if !didRe.MatchString(in.DID) {
			f.add("did", "must be 1-32 of 0-9 * # with an optional leading +")
		}
	case "regex":
		compileRegex(&f, "did", in.DID)
	default:
		f.add("didKind", `must be "any", "exact", "prefix" or "regex"`)
	}
	if in.TrunkID != nil && *in.TrunkID <= 0 {
		f.add("trunkId", "must be a trunk id or null")
	}
	if in.SIPDomain != "" && !validHost(in.SIPDomain) {
		f.add("sipDomain", "must be a host name or IP address")
	}
	switch {
	case in.HeaderName == "" && in.HeaderRegex == "":
	case in.HeaderName == "" || in.HeaderRegex == "":
		f.add("headerName", "headerName and headerRegex are set together")
	default:
		if !headerNameRe.MatchString(in.HeaderName) {
			f.add("headerName", "must be a SIP header name")
		}
		compileRegex(&f, "headerRegex", in.HeaderRegex)
	}
	validateSchedule(&f, "schedule", in.Schedule)
	validateTransform(&f, "callerIdTransform", in.CallerIDTransform)
	switch in.DestinationKind {
	case "extension":
		if !numberRe.MatchString(in.Destination) {
			f.add("destination", "must be an extension number (2-10 digits)")
		}
	case "external":
		if !externalNumRe.MatchString(in.Destination) {
			f.add("destination", "must be 2-32 of 0-9 * # with an optional leading +")
		}
	case "sip_uri":
		switch {
		case strings.HasPrefix(strings.ToLower(in.Destination), "sips:"):
			f.add("destination", "sips: needs SIP over TLS, and Hello is UDP-only in this phase; use a sip: URI")
		case !validSIPURI(in.Destination):
			f.add("destination", "must be a sip: URI of at most 255 characters")
		}
	case "voice_agent":
		// Stage 1 checks the shape only: the agent's existence, enabled
		// state and the configured talking-agent address are the
		// whole-configuration compile (stage 2), which answers
		// voice_not_configured and names the route (spec S-10).
		if !voiceAgentNameRe.MatchString(in.Destination) {
			f.add("destination", "must be a voice agent name (1-64 of A-Z a-z 0-9 . _ -)")
		}
	default:
		f.add("destinationKind", `must be "extension", "external", "sip_uri" or "voice_agent"`)
	}
	return f
}

func validSIPURI(s string) bool {
	if len(s) > 255 || !printable(s, 255, false) {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "sip" || u.Opaque == "" {
		return false
	}
	host := u.Opaque
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if semi := strings.IndexByte(host, ';'); semi >= 0 {
		host = host[:semi]
	}
	if h, _, err := splitHostPortOpt(host); err == nil {
		host = h
	}
	return validHost(strings.Trim(host, "[]"))
}

// splitHostPortOpt splits "host[:port]".
func splitHostPortOpt(s string) (string, string, error) {
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") && strings.Count(s, ":") == 1 {
		if _, err := strconv.ParseUint(s[i+1:], 10, 16); err != nil {
			return "", "", err
		}
		return s[:i], s[i+1:], nil
	}
	return s, "", nil
}
