// Package routing is Hello's routing engine: it compiles trunks and inbound
// and outbound routes into a Table and decides, deterministically, where a
// call goes, recording every step in a trace (spec §11). It is pure — no I/O
// — so hello-control's route tester and hello-sip's call path share it and
// produce identical traces for identical input.
package routing

import (
	"net/netip"
	"time"
)

// Transform rewrites a number: strip leading digits, then add a prefix,
// then — if Regex is set — replace the whole match with Template, where
// ${1}, ${name} refer to capture groups. The zero Transform is identity.
type Transform struct {
	Strip    int    `json:"strip,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Template string `json:"template,omitempty"`
}

// Window is one weekly open interval in a schedule's time zone. Start and
// End are "HH:MM"; End <= Start means the window crosses midnight.
type Window struct {
	Days  []time.Weekday `json:"days"`
	Start string         `json:"start"`
	End   string         `json:"end"`
}

// Schedule opens a route only inside its windows; a nil *Schedule is
// always open.
type Schedule struct {
	TimeZone string   `json:"timeZone"` // IANA name, e.g. "Asia/Dubai"
	Windows  []Window `json:"windows"`
}

// Destination is one address of a trunk. Port 0 means "resolve SRV, else
// 5060".
type Destination struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Priority int    `json:"priority"` // lower is tried first
	Weight   int    `json:"weight"`   // among equal priorities
}

// Trunk is a carrier account or IP peer.
type Trunk struct {
	ID              int64
	Name            string
	Mode            string // "registration" | "ip"
	Username        string
	Password        string // decrypted; never logged, never in a trace
	Realm           string
	FromDomain      string
	RegisterExpires time.Duration
	OptionsInterval time.Duration
	SourceCIDRs     []netip.Prefix
	MaxCalls        int // 0 = unlimited
	DefaultCallerID string
	Enabled         bool
	Destinations    []Destination
}

// OutboundRoute matches calls from extensions to external numbers.
type OutboundRoute struct {
	ID               int64
	Position         int
	Name             string
	MatchKind        string // "prefix" | "regex"
	Match            string
	SourceExtensions []string // empty = every extension
	Schedule         *Schedule
	Number           Transform
	CallerID         Transform
	Trunks           []int64 // trunk IDs in try order
	FailoverCodes    []int
	Emergency        bool
	Enabled          bool
}

// InboundRoute matches calls arriving from trunks.
type InboundRoute struct {
	ID              int64
	Position        int
	Name            string
	DIDKind         string // "any" | "exact" | "prefix" | "regex"
	DID             string
	TrunkID         int64 // 0 = any trunk
	SIPDomain       string
	HeaderName      string
	HeaderRegex     string
	Schedule        *Schedule
	CallerID        Transform
	DestinationKind string // "extension" | "external" | "sip_uri"
	Destination     string
	Enabled         bool
}

// Config is the routing input loaded from PostgreSQL.
type Config struct {
	Trunks   []Trunk
	Outbound []OutboundRoute // any order; Compile sorts by Position
	Inbound  []InboundRoute
	// Extensions maps every extension number to its external number ("" if
	// none); membership means "is an internal extension".
	Extensions map[string]string
	// ResolvedIPs maps a trunk ID to the IPs its destination hostnames
	// resolved to, for source validation. hello-sip fills it off the call
	// path (DNS); hello-control's route tester may leave it empty.
	ResolvedIPs map[int64][]netip.Addr
}

// FieldError is a validation failure on one field, for API 400 responses.
// Path is like "outbound[3].number.template" or "trunks[1].destinations".
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Call is what the engine decides on.
type Call struct {
	// Exactly one of FromExtension and FromTrunk is set.
	FromExtension string
	FromTrunk     int64
	Number        string // as dialled (outbound) or the called DID (inbound)
	CallerID      string // as presented by the caller
	SIPDomain     string // host of the Request-URI
	Header        func(name string) string
	At            time.Time
}

// TrunkUsability reports at decision time whether a trunk can take another
// call; reason is recorded in the trace when it cannot ("disabled",
// "unhealthy", "full", "misconfigured", "state unavailable").
type TrunkUsability func(trunkID int64, emergency bool) (ok bool, reason string)

// Kind is a decision's outcome.
type Kind string

const (
	KindInternal Kind = "internal" // ring Extension
	KindOutbound Kind = "outbound" // try Candidates in order
	KindInbound  Kind = "inbound"  // ring Extension, or route to SIPURI
	KindReject   Kind = "reject"   // answer RejectCode
)

// Candidate is one trunk to try, with its destinations in try order.
type Candidate struct {
	Trunk        *Trunk
	Destinations []Destination
}

// Decision is the engine's answer; Trace explains it.
type Decision struct {
	Kind Kind
	// Extension is the internal/inbound target extension; SIPURI the
	// inbound SIP URI.
	Extension     string
	SIPURI        string
	Number        string      // rewritten number sent to the carrier
	CallerID      string      // caller ID to present
	Route         string      // name of the matched route
	Candidates    []Candidate // outbound, already filtered by usability
	FailoverCodes []int
	Emergency     bool
	RejectCode    int
	Reason        string // one-line explanation when Kind is KindReject
	Trace         Trace
}

// Step is one line of a routing trace.
type Step struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// Trace is the ordered explanation of a call's routing, extended by the
// SIP node with each trunk attempt and its responses.
type Trace []Step

// Add appends a step numbered after the last one.
func (t *Trace) Add(text string) { *t = append(*t, Step{N: len(*t) + 1, Text: text}) }

// The engine's API (implemented in engine.go):
//
//	func Compile(cfg Config) (*Table, []FieldError)
//	    Validates everything (regexes compile and are ≤500 chars, templates
//	    reference existing groups, outbound routes have ≥1 existing trunk,
//	    schedules and time zones parse, destination kinds are valid) and
//	    returns an immutable, concurrency-safe Table with every regex
//	    compiled once. Any FieldError means no Table.
//
//	func (t *Table) Decide(c Call, usable TrunkUsability) Decision
//	    Pure and deterministic given (t, c, usable). Never panics.
//
//	func (t *Table) Trunk(id int64) (*Trunk, bool)
//	func (t *Table) TrunkForSource(ip netip.Addr, did string) (*Trunk, bool)
//	    Source validation (S-6): the lowest-ID enabled trunk whose source
//	    CIDRs or resolved destination IPs contain ip; resolution of
//	    destination hostnames happens outside the engine and is supplied in
//	    Config.ResolvedIPs.
//
//	func ApplyTransform(tr Transform, number string) (string, error)
//	    Exposed for tests and the API's validation preview.
