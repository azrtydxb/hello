package routing

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// defaultFailoverCodes apply to an outbound route whose FailoverCodes is
// empty (spec S-4). No answer from a destination always fails over; that is
// the SIP node's timeout, not a code.
var defaultFailoverCodes = []int{408, 480, 500, 502, 503, 504}

// Table is a compiled routing configuration. It is immutable after Compile
// and safe for concurrent use. Pointers it hands out (Trunk, Candidate.Trunk)
// point into the Table and must not be modified.
type Table struct {
	trunks     []Trunk // sorted by ID
	trunkIdx   map[int64]int
	outbound   []compiledOutbound // sorted by Position
	inbound    []compiledInbound  // sorted by Position
	extensions map[string]string
	resolved   map[int64][]netip.Addr
	// voice is every agent by name; voiceExt an agent extension by number.
	// voiceAddr is Config.VoiceSIPAddress, which derived SIP URIs need at
	// Decide time (an agent's extension dial resolves there, and the engine
	// refuses that when voice agents are not configured).
	voice     map[string]VoiceAgent
	voiceExt  map[string]string // extension number -> agent name
	voiceAddr string
}

// voiceRef is the Decision's agent reference for the agent named name, with
// its SIP URI towards talking-agent. ok is false when name is not in the
// Table (Compile guarantees it is for every destination it accepted).
func (t *Table) voiceRef(name string) (ref VoiceRef, uri string, ok bool) {
	a, ok := t.voice[name]
	if !ok {
		return VoiceRef{}, "", false
	}
	return VoiceRef{Name: a.Name, SIPUser: a.SIPUser}, "sip:" + a.SIPUser + "@" + t.voiceAddr, true
}

// voiceNotConfigured is the compile and Decide reason when
// HELLO_VOICE_SIP_ADDRESS is unset (spec S-17).
const voiceNotConfigured = "voice agents are not configured (HELLO_VOICE_SIP_ADDRESS is unset)"

type compiledOutbound struct {
	r        OutboundRoute
	re       *regexp.Regexp // MatchKind "regex"
	srcExt   map[string]struct{}
	sched    *compiledSchedule
	number   compiledTransform
	callerID compiledTransform
	failover []int
	label    string // "prefix 05" or "regex ^05[0-9]{8}$", for the trace
}

type compiledInbound struct {
	r        InboundRoute
	did      string // exact/prefix: whitespace and leading + removed
	didRe    *regexp.Regexp
	hdrRe    *regexp.Regexp
	sched    *compiledSchedule
	callerID compiledTransform
}

// Compile validates cfg and compiles it into a Table. Every problem found
// is returned (not just the first), each with a JSON path built from the
// API's field names and the index of the item in cfg's slice — for example
// "outbound[3].numberTransform.template" or
// "trunks[1].destinations[0].host". Any FieldError means no Table.
func Compile(cfg Config) (*Table, []FieldError) {
	var errs []FieldError
	t := &Table{
		trunkIdx:   make(map[int64]int, len(cfg.Trunks)),
		extensions: maps.Clone(cfg.Extensions),
		resolved:   make(map[int64][]netip.Addr, len(cfg.ResolvedIPs)),
		voice:      make(map[string]VoiceAgent, len(cfg.VoiceAgents)),
		voiceExt:   make(map[string]string, len(cfg.VoiceAgentExtensions)),
		voiceAddr:  cfg.VoiceSIPAddress,
	}
	if t.extensions == nil {
		t.extensions = map[string]string{}
	}
	t.compileVoiceAgents(cfg.VoiceAgents, cfg.VoiceAgentExtensions, &errs)
	t.compileTrunks(cfg.Trunks, &errs)
	t.compileOutbound(cfg.Outbound, &errs)
	t.compileInbound(cfg.Inbound, &errs)
	for _, num := range slices.Sorted(maps.Keys(cfg.Extensions)) {
		if ext := cfg.Extensions[num]; ext != "" && !validNumber(ext) {
			addErr(&errs, fmt.Sprintf("extensions[%s].externalNumber", quote(num)), "must be a number (+, digits, * and #)")
		}
	}
	for id, ips := range cfg.ResolvedIPs {
		u := make([]netip.Addr, 0, len(ips))
		for _, ip := range ips {
			u = append(u, ip.Unmap())
		}
		t.resolved[id] = u
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return t, nil
}

// compileVoiceAgents indexes the registry input and validates it: a name
// and sip user are required, names and extensions are unique, and an agent
// extension must not collide with an extension-table number (the registry
// refuses that with 409; Compile is the second net). Errors use
// voiceAgents[i] paths, which configPath does not map, so such a leftover
// never blocks an unrelated change twice.
func (t *Table) compileVoiceAgents(in []VoiceAgent, exts map[string]string, errs *[]FieldError) {
	for i, a := range in {
		p := fmt.Sprintf("voiceAgents[%d]", i)
		if a.Name == "" {
			addErr(errs, p+".name", "is required")
			continue
		}
		if _, dup := t.voice[a.Name]; dup {
			addErr(errs, p+".name", fmt.Sprintf("duplicate voice agent name %s", quote(a.Name)))
			continue
		}
		if a.SIPUser == "" {
			addErr(errs, p+".sipUser", fmt.Sprintf("is required for voice agent %s", quote(a.Name)))
			continue
		}
		t.voice[a.Name] = a
	}
	for num, name := range exts {
		if _, dup := t.voiceExt[num]; dup {
			addErr(errs, `voiceAgents[?].extension`, fmt.Sprintf("extension %s is claimed by two voice agents", quote(num)))
			continue
		}
		if _, clash := t.extensions[num]; clash {
			addErr(errs, `voiceAgents[?].extension`, fmt.Sprintf("extension %s is both a voice agent's and an extension-table number", quote(num)))
			continue
		}
		t.voiceExt[num] = name
	}
}

func (t *Table) compileTrunks(in []Trunk, errs *[]FieldError) {
	names := map[string]int{}
	for i, tr := range in {
		p := fmt.Sprintf("trunks[%d]", i)
		if tr.ID <= 0 {
			addErr(errs, p+".id", "must be positive")
		} else if _, dup := t.trunkIdx[tr.ID]; dup {
			addErr(errs, p+".id", fmt.Sprintf("duplicate trunk id %d", tr.ID))
		}
		t.trunkIdx[tr.ID] = -1
		switch {
		case strings.TrimSpace(tr.Name) == "":
			addErr(errs, p+".name", "is required")
		case names[tr.Name] > 0:
			addErr(errs, p+".name", fmt.Sprintf("duplicate trunk name %s", quote(tr.Name)))
		}
		names[tr.Name]++
		switch tr.Mode {
		case "registration":
			if tr.Username == "" {
				addErr(errs, p+".username", "is required for a registration trunk")
			}
		case "ip":
		default:
			addErr(errs, p+".mode", `must be "registration" or "ip"`)
		}
		if tr.RegisterExpires < 0 {
			addErr(errs, p+".registerExpires", "must not be negative")
		}
		if tr.OptionsInterval < 0 {
			addErr(errs, p+".optionsInterval", "must not be negative")
		}
		if tr.MaxCalls < 0 {
			addErr(errs, p+".maxCalls", "must not be negative")
		}
		if tr.DefaultCallerID != "" && !validNumber(tr.DefaultCallerID) {
			addErr(errs, p+".defaultCallerId", "must be a number (+, digits, * and #)")
		}
		for k, c := range tr.SourceCIDRs {
			if !c.IsValid() {
				addErr(errs, fmt.Sprintf("%s.sourceCidrs[%d]", p, k), "is not a valid CIDR")
			}
		}
		if len(tr.Destinations) == 0 {
			addErr(errs, p+".destinations", "at least one destination is required")
		}
		checkDestinations(errs, p, tr.Destinations)
		tr.SourceCIDRs = slices.Clone(tr.SourceCIDRs)
		tr.Destinations = slices.Clone(tr.Destinations)
		t.trunks = append(t.trunks, tr)
	}
	slices.SortStableFunc(t.trunks, func(a, b Trunk) int { return cmp.Compare(a.ID, b.ID) })
	for i := range t.trunks {
		t.trunkIdx[t.trunks[i].ID] = i
	}
}

func (t *Table) compileOutbound(in []OutboundRoute, errs *[]FieldError) {
	positions := map[int]int{}
	ids := map[int64]int{}
	for i, r := range in {
		p := fmt.Sprintf("outbound[%d]", i)
		checkIdentity(errs, p, r.ID, r.Position, r.Name, ids, positions, "outbound", i)
		c := compiledOutbound{r: r}
		switch r.MatchKind {
		case "prefix":
			if r.Match == "" || !validPrefix(r.Match) || r.Match == "+" {
				addErr(errs, p+".match", "a prefix must be a non-empty number (+, digits, * and #)")
			}
			c.label = "prefix " + r.Match
		case "regex":
			re, msg := compileRegex(r.Match)
			if re == nil {
				addErr(errs, p+".match", msg)
			}
			c.re, c.label = re, "regex "+r.Match
		default:
			addErr(errs, p+".matchKind", `must be "prefix" or "regex"`)
		}
		if len(r.SourceExtensions) > 0 {
			c.srcExt = make(map[string]struct{}, len(r.SourceExtensions))
			for k, e := range r.SourceExtensions {
				if e == "" {
					addErr(errs, fmt.Sprintf("%s.sourceExtensions[%d]", p, k), "must not be empty")
				}
				c.srcExt[e] = struct{}{}
			}
		}
		if r.Schedule != nil {
			c.sched, _ = compileSchedule(*r.Schedule, p+".schedule", errs)
		}
		c.number, _ = compileTransform(r.Number, p+".numberTransform", errs)
		c.callerID, _ = compileTransform(r.CallerID, p+".callerIdTransform", errs)
		if len(r.Trunks) == 0 {
			addErr(errs, p+".trunks", "at least one trunk is required")
		}
		seen := map[int64]bool{}
		for k, id := range r.Trunks {
			kp := fmt.Sprintf("%s.trunks[%d]", p, k)
			switch _, ok := t.trunkIdx[id]; {
			case !ok || id <= 0:
				addErr(errs, kp, fmt.Sprintf("unknown trunk %d", id))
			case seen[id]:
				addErr(errs, kp, fmt.Sprintf("trunk %d is listed twice", id))
			}
			seen[id] = true
		}
		c.failover = failoverCodes(errs, p, r.FailoverCodes)
		c.r.SourceExtensions = slices.Clone(r.SourceExtensions)
		c.r.Trunks = slices.Clone(r.Trunks)
		c.r.FailoverCodes = slices.Clone(r.FailoverCodes)
		c.r.Schedule = nil // compiled into sched
		t.outbound = append(t.outbound, c)
	}
	slices.SortStableFunc(t.outbound, func(a, b compiledOutbound) int { return cmp.Compare(a.r.Position, b.r.Position) })
}

func (t *Table) compileInbound(in []InboundRoute, errs *[]FieldError) {
	positions := map[int]int{}
	ids := map[int64]int{}
	for i, r := range in {
		p := fmt.Sprintf("inbound[%d]", i)
		checkIdentity(errs, p, r.ID, r.Position, r.Name, ids, positions, "inbound", i)
		c := compiledInbound{r: r}
		switch r.DIDKind {
		case "any":
			if r.DID != "" {
				addErr(errs, p+".did", `must be empty when didKind is "any"`)
			}
		case "exact", "prefix":
			did := stripSpace(r.DID)
			if !validNumber(did) {
				addErr(errs, p+".did", "must be a number (+, digits, * and #; whitespace is ignored)")
			}
			c.did = strings.TrimPrefix(did, "+")
		case "regex":
			re, msg := compileRegex(r.DID)
			if re == nil {
				addErr(errs, p+".did", msg)
			}
			c.didRe = re
		default:
			addErr(errs, p+".didKind", `must be "any", "exact", "prefix" or "regex"`)
		}
		if r.TrunkID < 0 {
			addErr(errs, p+".trunkId", "must be positive (or null for any trunk)")
		} else if r.TrunkID > 0 {
			if _, ok := t.trunkIdx[r.TrunkID]; !ok {
				addErr(errs, p+".trunkId", fmt.Sprintf("unknown trunk %d", r.TrunkID))
			}
		}
		switch {
		case r.HeaderName == "" && r.HeaderRegex != "":
			addErr(errs, p+".headerName", "is required with a header regex")
		case r.HeaderName != "" && r.HeaderRegex == "":
			addErr(errs, p+".headerRegex", "is required with a header name")
		case r.HeaderName != "":
			if !isToken(r.HeaderName) {
				addErr(errs, p+".headerName", "must be a SIP header name")
			}
			re, msg := compileRegex(r.HeaderRegex)
			if re == nil {
				addErr(errs, p+".headerRegex", msg)
			}
			c.hdrRe = re
		}
		if r.Schedule != nil {
			c.sched, _ = compileSchedule(*r.Schedule, p+".schedule", errs)
		}
		c.callerID, _ = compileTransform(r.CallerID, p+".callerIdTransform", errs)
		t.checkInboundDestination(errs, p, r)
		c.r.Schedule = nil
		t.inbound = append(t.inbound, c)
	}
	slices.SortStableFunc(t.inbound, func(a, b compiledInbound) int { return cmp.Compare(a.r.Position, b.r.Position) })
}

func checkDestinations(errs *[]FieldError, p string, dsts []Destination) {
	for j, d := range dsts {
		dp := fmt.Sprintf("%s.destinations[%d]", p, j)
		if d.Host == "" || strings.IndexFunc(d.Host, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) >= 0 {
			addErr(errs, dp+".host", "is required and must not contain whitespace")
		}
		if d.Port < 0 || d.Port > 65535 {
			addErr(errs, dp+".port", "must be 0 (SRV, else 5060) to 65535")
		}
		if d.Priority < 0 || d.Priority > 65535 {
			addErr(errs, dp+".priority", "must be 0 to 65535")
		}
		if d.Weight < 0 || d.Weight > 65535 {
			addErr(errs, dp+".weight", "must be 0 to 65535")
		}
	}
}

func (t *Table) checkInboundDestination(errs *[]FieldError, p string, r InboundRoute) {
	switch r.DestinationKind {
	case "extension":
		if _, ok := t.extensions[r.Destination]; !ok {
			addErr(errs, p+".destination", fmt.Sprintf("extension %s does not exist", quote(r.Destination)))
		}
	case "external":
		if !validNumber(r.Destination) {
			addErr(errs, p+".destination", "must be a number (+, digits, * and #)")
		}
	case "sip_uri":
		if !validSIPURI(r.Destination) {
			addErr(errs, p+".destination", "must be a sip: or sips: URI without whitespace")
		}
	case "voice_agent":
		a, ok := t.voice[r.Destination]
		switch {
		case !ok:
			addErr(errs, p+".destination", fmt.Sprintf("voice agent %s does not exist", quote(r.Destination)))
		case !a.Enabled:
			addErr(errs, p+".destination", fmt.Sprintf("voice agent %s is disabled", quote(r.Destination)))
		case t.voiceAddr == "":
			addErr(errs, p+".destination", voiceNotConfigured)
		}
	default:
		addErr(errs, p+".destinationKind", `must be "extension", "external", "sip_uri" or "voice_agent"`)
	}
}

// failoverCodes validates a route's codes and returns the effective list:
// the route's own, or defaultFailoverCodes when it has none.
func failoverCodes(errs *[]FieldError, p string, codes []int) []int {
	for k, code := range codes {
		if code < 400 || code > 699 {
			addErr(errs, fmt.Sprintf("%s.failoverCodes[%d]", p, k), "must be a SIP failure code (400 to 699)")
		}
	}
	if len(codes) == 0 {
		return slices.Clone(defaultFailoverCodes)
	}
	return slices.Clone(codes)
}

// checkIdentity validates the fields every route has: a unique ID, a unique
// position (route order is the position) and a name (the trace and CDRs
// name the route). kind is "outbound" or "inbound", for the message.
func checkIdentity(errs *[]FieldError, p string, id int64, pos int, name string, ids map[int64]int, positions map[int]int, kind string, i int) {
	if j, dup := positions[pos]; dup {
		addErr(errs, p+".position", fmt.Sprintf("duplicate position %d (also %s[%d])", pos, kind, j))
	} else {
		positions[pos] = i
	}
	switch j, dup := ids[id]; {
	case id < 0:
		addErr(errs, p+".id", "must not be negative")
	case id > 0 && dup:
		addErr(errs, p+".id", fmt.Sprintf("duplicate id %d (also %s[%d])", id, kind, j))
	case id > 0:
		ids[id] = i
	}
	if strings.TrimSpace(name) == "" {
		addErr(errs, p+".name", "is required")
	}
}

func stripSpace(s string) string {
	if strings.IndexFunc(s, unicode.IsSpace) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// isToken reports whether s is an RFC 3261 token (a header name).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-.!%*_+`'~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}

func validSIPURI(s string) bool {
	rest, ok := strings.CutPrefix(s, "sip:")
	if !ok {
		rest, ok = strings.CutPrefix(s, "sips:")
	}
	return ok && rest != "" && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) < 0
}
