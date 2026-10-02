package routing

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Reject codes the engine produces.
const (
	codeForbidden    = 403 // call from an unknown or disabled trunk
	codeNotFound     = 404 // no route matched
	codeAddrInvalid  = 484 // the number is, or rewrites to, something that is not a number
	codeInternal     = 500 // malformed Call, or an outbound caller-ID rewrite fails
	codeNoTrunk      = 503 // no usable trunk on the matched route
	codeNoTableReady = 503 // nil Table
)

// Trunk returns the trunk with id. The result must not be modified.
func (t *Table) Trunk(id int64) (*Trunk, bool) {
	if t == nil {
		return nil, false
	}
	i, ok := t.trunkIdx[id]
	if !ok {
		return nil, false
	}
	return &t.trunks[i], true
}

// TrunkForSource implements source validation (spec S-6): of the enabled
// trunks whose SourceCIDRs or resolved destination IPs (Config.ResolvedIPs)
// contain ip, it returns the lowest-ID one. When several match and did is
// not empty, it first prefers the lowest-ID one that an enabled inbound
// route names explicitly (TrunkID) with a DID condition matching did;
// routes for any trunk do not discriminate and are ignored for this. The
// route bound to a trunk with didKind "any" claims every DID for it. The
// route's schedule, SIP domain and header conditions are not considered:
// the request is not known yet. IPv4-mapped IPv6 addresses compare as IPv4.
func (t *Table) TrunkForSource(ip netip.Addr, did string) (*Trunk, bool) {
	if t == nil || !ip.IsValid() {
		return nil, false
	}
	ip = ip.Unmap()
	var first *Trunk
	matches := 0
	for i := range t.trunks {
		tr := &t.trunks[i]
		if !tr.Enabled || !t.sourceMatches(tr, ip) {
			continue
		}
		matches++
		if first == nil {
			first = tr
		}
		if did == "" {
			break
		}
		if t.hasInboundFor(tr.ID, did) {
			return tr, true
		}
	}
	return first, first != nil
}

func (t *Table) sourceMatches(tr *Trunk, ip netip.Addr) bool {
	for _, p := range tr.SourceCIDRs {
		if p.Contains(ip) {
			return true
		}
	}
	return slices.Contains(t.resolved[tr.ID], ip)
}

func (t *Table) hasInboundFor(trunkID int64, did string) bool {
	norm := stripSpace(did)
	for i := range t.inbound {
		r := &t.inbound[i]
		if r.r.Enabled && r.r.TrunkID == trunkID {
			if ok, _ := r.matchDID(norm); ok {
				return true
			}
		}
	}
	return false
}

// Decide routes c. It is pure and deterministic given (t, c, usable): it
// reads only the Table, the Call (including c.At, the clock — a zero At is
// evaluated literally) and the usability callback, and never panics. A nil
// usable treats every enabled trunk as usable. Callbacks (usable, c.Header)
// must not panic themselves.
//
// From an extension: the dialled number is first looked up as an internal
// extension (KindInternal), then matched against outbound routes in
// position order. From a trunk: inbound routes in position order; an
// "external" destination continues through outbound routing as a call
// from that trunk. No match is KindReject 404; a route with no usable trunk
// is KindReject 503.
func (t *Table) Decide(c Call, usable TrunkUsability) Decision {
	var d Decision
	switch {
	case t == nil:
		d.reject(codeNoTableReady, "No routing table loaded")
	case c.FromExtension != "" && c.FromTrunk != 0:
		d.reject(codeInternal, "Invalid call: both a source extension and a source trunk are set")
	case c.FromExtension != "":
		t.fromExtension(&d, c, usable)
	case c.FromTrunk != 0:
		t.fromTrunk(&d, c, usable)
	default:
		d.reject(codeInternal, "Invalid call: no source extension or trunk")
	}
	return d
}

func (d *Decision) reject(code int, text string) {
	d.Trace.Add(text)
	d.Kind, d.RejectCode, d.Reason = KindReject, code, text
	d.Candidates, d.FailoverCodes = nil, nil
}

func (t *Table) fromExtension(d *Decision, c Call, usable TrunkUsability) {
	d.Number = c.Number
	if _, ok := t.extensions[c.Number]; ok {
		d.Trace.Add(fmt.Sprintf("Internal extension lookup %s -> extension %s", quote(c.Number), show(c.Number)))
		d.Kind, d.Extension, d.CallerID = KindInternal, c.Number, c.FromExtension
		return
	}
	d.Trace.Add(fmt.Sprintf("Internal extension lookup %s -> no match", quote(c.Number)))
	t.outboundRoute(d, c.Number, outboundSource{ext: c.FromExtension}, c, usable)
}

// outboundSource is who an outbound call is from: an extension, or (for an
// inbound route with an external destination) a trunk and the caller ID the
// inbound route produced.
type outboundSource struct {
	ext      string
	trunk    *Trunk
	callerID string
}

func (t *Table) outboundRoute(d *Decision, number string, src outboundSource, c Call, usable TrunkUsability) {
	r := t.matchOutbound(&d.Trace, number, src, c.At)
	if r == nil {
		d.reject(codeNotFound, fmt.Sprintf("No outbound route matched %s", quote(number)))
		return
	}
	emergency := ""
	if r.r.Emergency {
		emergency = "; emergency"
	}
	d.Trace.Add(fmt.Sprintf("Route %s matched (%s%s)", strconv.Quote(r.r.Name), r.label, emergency))
	d.Route, d.Emergency = r.r.Name, r.r.Emergency

	out, failed := rewrite(&d.Trace, "Number", "Rewrite", &r.number, number)
	if failed != "" {
		d.reject(codeAddrInvalid, failed)
		return
	}
	d.Number = out

	if skipped := t.candidates(d, r, number, usable); len(d.Candidates) == 0 {
		d.reject(codeNoTrunk, fmt.Sprintf("No usable trunk on route %s (%s)", strconv.Quote(r.r.Name), strings.Join(skipped, "; ")))
		return
	}
	cid, failed := t.outboundCallerID(&d.Trace, r, src, d.Candidates[0].Trunk)
	if failed != "" {
		d.reject(codeInternal, failed)
		return
	}
	d.Kind, d.CallerID = KindOutbound, cid
	d.FailoverCodes = slices.Clone(r.failover)
	names := make([]string, len(d.Candidates))
	for i, cand := range d.Candidates {
		names[i] = cand.Trunk.Name
	}
	d.Trace.Add(fmt.Sprintf("Outbound to %s via %s; failover on %s", d.Number, strings.Join(names, ", "), joinInts(d.FailoverCodes)))
}

// matchOutbound returns the first route, in position order, whose pattern
// matches number and whose conditions hold. Pattern misses are not traced
// (they are the normal case and would make every trace as long as the
// route list); a route whose pattern matched but which is skipped is.
func (t *Table) matchOutbound(tr *Trace, number string, src outboundSource, at time.Time) *compiledOutbound {
	for i := range t.outbound {
		r := &t.outbound[i]
		if !r.matches(number) {
			continue
		}
		name := strconv.Quote(r.r.Name)
		switch {
		case !r.r.Enabled:
			tr.Add(fmt.Sprintf("Route %s skipped: disabled", name))
			continue
		case r.srcExt != nil && src.trunk != nil:
			// A route restricted to some extensions is never used for a
			// call forwarded from a trunk: it has no source extension.
			tr.Add(fmt.Sprintf("Route %s skipped: restricted to source extensions, and the call is from trunk %s", name, src.trunk.Name))
			continue
		case r.srcExt != nil:
			if _, ok := r.srcExt[src.ext]; !ok {
				tr.Add(fmt.Sprintf("Route %s skipped: extension %s is not in its source extensions", name, show(src.ext)))
				continue
			}
		}
		if r.sched != nil && !r.sched.open(at) {
			tr.Add(fmt.Sprintf("Route %s skipped: schedule closed (%s)", name, r.sched.local(at)))
			continue
		}
		return r
	}
	return nil
}

// candidates appends the route's usable trunks, in route order, to
// d.Candidates, each with its destinations in try order, and returns
// "name: reason" for each skipped trunk.
func (t *Table) candidates(d *Decision, r *compiledOutbound, number string, usable TrunkUsability) (skipped []string) {
	for _, id := range r.r.Trunks {
		tr, _ := t.Trunk(id)
		if tr == nil {
			continue // Compile guarantees every trunk exists
		}
		reason := ""
		switch {
		case !tr.Enabled:
			reason = "disabled"
		case usable != nil:
			if ok, why := usable(tr.ID, r.r.Emergency); !ok {
				reason = why
				if reason == "" {
					reason = "unusable"
				}
			}
		}
		if reason != "" {
			d.Trace.Add(fmt.Sprintf("Trunk %s skipped: %s", tr.Name, reason))
			skipped = append(skipped, tr.Name+": "+reason)
			continue
		}
		dsts := orderDestinations(tr.Destinations, destinationSeed(number, tr.ID))
		creds := "none"
		if tr.Password != "" {
			creds = "configured"
		}
		d.Trace.Add(fmt.Sprintf("Trunk %s usable (credentials: %s); destinations: %s", tr.Name, creds, joinDestinations(dsts)))
		d.Candidates = append(d.Candidates, Candidate{Trunk: tr, Destinations: dsts})
	}
	return skipped
}

// outboundCallerID picks and rewrites the caller ID (spec S-10): the
// route's transform applied to the extension's external number, else the
// first candidate's default caller ID, else the extension number. A call
// forwarded from a trunk uses the inbound caller ID in place of the
// extension's numbers. failed is the failure sentence when the rewrite is
// not a number.
func (t *Table) outboundCallerID(tr *Trace, r *compiledOutbound, src outboundSource, first *Trunk) (cid, failed string) {
	switch ext := t.extensions[src.ext]; {
	case src.trunk != nil && src.callerID != "":
		cid = src.callerID
		tr.Add(fmt.Sprintf("Caller ID %s (inbound caller ID)", show(cid)))
	case src.trunk == nil && ext != "":
		cid = ext
		tr.Add(fmt.Sprintf("Caller ID %s (external number of extension %s)", show(cid), show(src.ext)))
	case first.DefaultCallerID != "":
		cid = first.DefaultCallerID
		tr.Add(fmt.Sprintf("Caller ID %s (default caller ID of trunk %s)", show(cid), first.Name))
	case src.trunk == nil:
		cid = src.ext
		tr.Add(fmt.Sprintf("Caller ID %s (extension number)", show(cid)))
	default:
		tr.Add("Caller ID: none available")
	}
	if cid == "" || r.callerID.identity() {
		return cid, ""
	}
	return rewrite(tr, "Caller ID", "Caller ID rewrite", &r.callerID, cid)
}

func (r *compiledOutbound) matches(number string) bool {
	if r.re != nil {
		return r.re.MatchString(number)
	}
	return strings.HasPrefix(number, r.r.Match)
}

// rewrite applies ct to in and traces it: label names the value
// ("Number"), verb the step ("Rewrite"). When the result is not a number
// it returns the failure sentence instead, untraced, for the caller's
// reject.
func rewrite(tr *Trace, label, verb string, ct *compiledTransform, in string) (out, failed string) {
	out, missed, err := ct.apply(in)
	if missed {
		tr.Add(fmt.Sprintf("%s transform regex %s did not match %s; regex step skipped", label, ct.re.String(), show(stripPrefix(ct, in))))
	}
	switch {
	case err != nil && ct.identity():
		return "", fmt.Sprintf("%s %s rejected: %s", label, show(in), errInvalidNumber)
	case err != nil:
		return "", fmt.Sprintf("%s %s failed: %s", verb, show(in), err)
	case ct.identity():
		tr.Add(fmt.Sprintf("%s %s unchanged (no rewrite)", label, out))
	default:
		tr.Add(fmt.Sprintf("%s %s -> %s", verb, show(in), out))
	}
	return out, ""
}

// stripPrefix returns what the regex step saw: in after strip and prefix.
func stripPrefix(ct *compiledTransform, in string) string {
	if ct.strip >= len(in) {
		in = ""
	} else {
		in = in[ct.strip:]
	}
	return ct.prefix + in
}

func (t *Table) fromTrunk(d *Decision, c Call, usable TrunkUsability) {
	src, ok := t.Trunk(c.FromTrunk)
	switch {
	case !ok:
		d.reject(codeForbidden, fmt.Sprintf("Source trunk %d is not configured", c.FromTrunk))
		return
	case !src.Enabled:
		d.reject(codeForbidden, fmt.Sprintf("Source trunk %s is disabled", src.Name))
		return
	}
	d.Trace.Add("Source trunk " + src.Name)
	did := stripSpace(c.Number)
	if did != c.Number {
		d.Trace.Add(fmt.Sprintf("Called DID %s normalised to %s", quote(c.Number), show(did)))
	} else {
		d.Trace.Add("Called DID " + show(did))
	}
	d.Number = did

	var r *compiledInbound
	var how string
	for i := range t.inbound {
		cand := &t.inbound[i]
		ok, desc := cand.matchDID(did)
		if !ok {
			continue
		}
		name := strconv.Quote(cand.r.Name)
		if reason := cand.skip(c, src); reason != "" {
			d.Trace.Add(fmt.Sprintf("Route %s skipped: %s", name, reason))
			continue
		}
		r, how = cand, desc
		break
	}
	if r == nil {
		d.reject(codeNotFound, "No inbound route matched DID "+show(did))
		return
	}
	d.Trace.Add(fmt.Sprintf("Route %s matched (%s)", strconv.Quote(r.r.Name), how))
	d.Route = r.r.Name

	// Caller ID as received; the route's transform normalises it. Inbound,
	// a transform that fails (e.g. "anonymous") keeps the received value:
	// rejecting a carrier's call over its caller ID would lose the call.
	cid := inboundCallerID(&d.Trace, &r.callerID, c.CallerID)
	d.CallerID = cid

	switch r.r.DestinationKind {
	case "extension":
		d.Trace.Add("Destination: extension " + show(r.r.Destination))
		d.Kind, d.Extension = KindInbound, r.r.Destination
	case "sip_uri":
		d.Trace.Add("Destination: SIP URI " + r.r.Destination)
		d.Kind, d.SIPURI = KindInbound, r.r.Destination
	case "external":
		// Routed through outbound routing as a call from the source trunk:
		// no internal extension lookup (use an "extension" destination for
		// that), and routes restricted to source extensions do not apply.
		d.Trace.Add(fmt.Sprintf("Destination: external number %s, routed outbound as from trunk %s", r.r.Destination, src.Name))
		// Route becomes the outbound route's name once one matches.
		t.outboundRoute(d, r.r.Destination, outboundSource{trunk: src, callerID: cid}, c, usable)
	}
}

// inboundCallerID presents the caller ID as received, normalised by the
// route's transform when it has one.
func inboundCallerID(tr *Trace, ct *compiledTransform, cid string) string {
	switch {
	case cid == "":
		tr.Add("Caller ID: none received")
	case ct.identity():
		tr.Add(fmt.Sprintf("Caller ID %s as received", show(cid)))
	default:
		out, missed, err := ct.apply(cid)
		if missed {
			tr.Add(fmt.Sprintf("Caller ID transform regex %s did not match %s; regex step skipped", ct.re.String(), show(stripPrefix(ct, cid))))
		}
		if err != nil {
			tr.Add(fmt.Sprintf("Caller ID rewrite %s failed: %s; presenting it as received", show(cid), err))
		} else {
			tr.Add(fmt.Sprintf("Caller ID rewrite %s -> %s", show(cid), out))
			cid = out
		}
	}
	return cid
}

// matchDID reports whether the route's DID condition matches did (already
// whitespace-free), and describes the match for the trace. Exact and prefix
// compare ignoring a leading "+" on either side; a regex is tried on did
// and then on did with its leading "+" toggled.
func (r *compiledInbound) matchDID(did string) (bool, string) {
	switch r.r.DIDKind {
	case "any":
		return true, "any DID"
	case "exact", "prefix":
		bare := strings.TrimPrefix(did, "+")
		ok := bare == r.did
		if r.r.DIDKind == "prefix" {
			ok = strings.HasPrefix(bare, r.did)
		}
		if !ok {
			return false, ""
		}
		desc := "DID " + r.r.DIDKind + " " + r.r.DID
		if strings.HasPrefix(did, "+") != strings.HasPrefix(stripSpace(r.r.DID), "+") {
			desc += ", leading + ignored"
		}
		return true, desc
	case "regex":
		if r.didRe.MatchString(did) {
			return true, "DID regex " + r.r.DID
		}
		alt := "+" + did
		if strings.HasPrefix(did, "+") {
			alt = did[1:]
		}
		if r.didRe.MatchString(alt) {
			return true, fmt.Sprintf("DID regex %s on %s", r.r.DID, show(alt))
		}
	}
	return false, ""
}

// skip returns why a route whose DID matched does not take the call, or "".
func (r *compiledInbound) skip(c Call, src *Trunk) string {
	switch {
	case !r.r.Enabled:
		return "disabled"
	case r.r.TrunkID != 0 && r.r.TrunkID != src.ID:
		return fmt.Sprintf("only for trunk %d, call is from trunk %s", r.r.TrunkID, src.Name)
	case r.r.SIPDomain != "" && !strings.EqualFold(r.r.SIPDomain, c.SIPDomain):
		return fmt.Sprintf("SIP domain %s is not %s", quote(c.SIPDomain), r.r.SIPDomain)
	}
	if r.hdrRe != nil {
		v := ""
		if c.Header != nil {
			v = strings.TrimSpace(c.Header(r.r.HeaderName))
		}
		if !r.hdrRe.MatchString(v) {
			return fmt.Sprintf("header %s %s does not match %s", r.r.HeaderName, quote(v), r.r.HeaderRegex)
		}
	}
	if r.sched != nil && !r.sched.open(c.At) {
		return fmt.Sprintf("schedule closed (%s)", r.sched.local(c.At))
	}
	return ""
}

// destinationSeed seeds a trunk's weighted destination order: FNV-1a of
// the routed number, mixed with the trunk ID, so the same call always
// tries destinations in the same order and different numbers spread
// across equal-priority destinations by weight.
func destinationSeed(number string, trunkID int64) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(number))
	return h.Sum64() ^ uint64(trunkID)*0x9e3779b97f4a7c15 //nolint:gosec // bit mixing, not a conversion that matters
}

// orderDestinations returns a copy of dsts in try order: priority
// ascending; within a priority, a weighted random order (an entry's chance
// of coming next is its weight over the remaining total) drawn from seed;
// weight-0 entries after the weighted ones, in configuration order.
func orderDestinations(dsts []Destination, seed uint64) []Destination {
	out := slices.Clone(dsts)
	slices.SortStableFunc(out, func(a, b Destination) int { return cmp.Compare(a.Priority, b.Priority) })
	rng := seed
	for i := 0; i < len(out); {
		j := i + 1
		for j < len(out) && out[j].Priority == out[i].Priority {
			j++
		}
		weightedOrder(out[i:j], &rng)
		i = j
	}
	return out
}

func weightedOrder(g []Destination, rng *uint64) {
	if len(g) < 2 {
		return
	}
	// Weighted entries first, zero weights last, both stable.
	slices.SortStableFunc(g, func(a, b Destination) int {
		return cmp.Compare(boolInt(a.Weight == 0), boolInt(b.Weight == 0))
	})
	n := 0
	total := 0
	for n < len(g) && g[n].Weight > 0 {
		total += g[n].Weight
		n++
	}
	for k := 0; k < n-1; k++ {
		r := int(splitmix64(rng) % uint64(total)) //nolint:gosec // total is at most 65535 * len(g)
		pick := k
		for acc := g[k].Weight; acc <= r; acc += g[pick].Weight {
			pick++
		}
		total -= g[pick].Weight
		// Move the pick to k, keeping the rest in order.
		p := g[pick]
		copy(g[k+1:pick+1], g[k:pick])
		g[k] = p
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// splitmix64 is a small deterministic PRNG (not for security).
func splitmix64(s *uint64) uint64 {
	*s += 0x9e3779b97f4a7c15
	z := *s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func joinDestinations(dsts []Destination) string {
	parts := make([]string, len(dsts))
	for i, d := range dsts {
		parts[i] = d.Host
		if d.Port != 0 {
			parts[i] = net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
		}
	}
	return strings.Join(parts, ", ")
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}
