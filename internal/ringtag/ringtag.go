// Package ringtag chooses the ring tag of a call to an internal device
// (spec phone-feature-parity S-39 and S-40) and renders the Alert-Info header
// that carries it (S-41). It is pure: no clock, no database; the snapshot
// holds the compiled table, so the choice never reads PostgreSQL.
//
// The first match wins, in this order:
//
//  1. the call's kind: page or intercom, alarm, door call, callback to an
//     extension within 30 minutes of its emergency call;
//  2. the ordered admin rules;
//  3. the ring group's tag when the call came through a ring group (its own
//     tag, else ringgroup);
//  4. external for a call that entered on a trunk;
//  5. internal.
package ringtag

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/timecond"
)

// Limits.
const (
	MaxCustomTags = 32
	MaxRules      = 100
	MaxNameLen    = 32
)

// The built-in tags.
const (
	Internal          = "internal"
	External          = "external"
	RingGroup         = "ringgroup"
	Page              = "page"
	Doorbell          = "doorbell"
	Alarm             = "alarm"
	EmergencyCallback = "emergency-callback"
)

// Call kinds that fix the tag (S-40 rule 1).
const (
	KindPage              = "page"
	KindIntercom          = "intercom"
	KindAlarm             = "alarm"
	KindDoor              = "door"
	KindEmergencyCallback = "emergency-callback"
)

// Caller kinds a rule may match.
const (
	CallerExtension = "extension"
	CallerDoor      = "door"
	CallerTrunk     = "trunk"
)

// Tag is a ring tag with its Bellcore slot.
type Tag struct {
	Name         string `json:"name"`
	Builtin      bool   `json:"builtin"`
	BellcoreSlot int    `json:"bellcoreSlot"`
	Description  string `json:"description"`
}

// Builtins are the seven built-in tags with their default Bellcore slots.
func Builtins() []Tag {
	return []Tag{
		{Internal, true, 1, "A call from another extension"},
		{External, true, 2, "A call that entered on a trunk"},
		{RingGroup, true, 3, "A call through a ring group"},
		{Page, true, 1, "A page or intercom call"},
		{Doorbell, true, 4, "A door station call"},
		{Alarm, true, 5, "An alarm call"},
		{EmergencyCallback, true, 1, "A callback to an extension that just called an emergency number"},
	}
}

// IsBuiltin reports whether name is one of the seven built-in tags.
func IsBuiltin(name string) bool {
	for _, t := range Builtins() {
		if t.Name == name {
			return true
		}
	}
	return false
}

var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidName reports whether name is a legal tag name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Window is a time window a rule may require: weekly ranges in a time zone.
type Window struct {
	TimeZone string              `json:"timeZone"`
	Ranges   []timecond.DayRange `json:"ranges"`
}

// Match is what a rule requires; every field present must match. Empty
// fields match anything.
type Match struct {
	// CallerPattern is a dial-plan pattern for the caller's number.
	CallerPattern string `json:"callerPattern,omitempty"`
	// Trunk is the name of the inbound trunk.
	Trunk string `json:"trunk,omitempty"`
	// DID is a dial-plan pattern for the dialled number.
	DID string `json:"did,omitempty"`
	// CallerKind is extension, door or trunk.
	CallerKind string `json:"callerKind,omitempty"`
	// Window is a time window the call must fall in.
	Window *Window `json:"window,omitempty"`
}

// Empty reports whether the match requires nothing.
func (m Match) Empty() bool {
	return m.CallerPattern == "" && m.Trunk == "" && m.DID == "" && m.CallerKind == "" && m.Window == nil
}

// Rule is one admin rule; rules are kept in position order.
type Rule struct {
	ID       int64  `json:"id"`
	Position int    `json:"position"`
	Name     string `json:"name"`
	Match    Match  `json:"match"`
	Tag      string `json:"tag"`
}

// Problem is a validation failure on a rule's field.
type Problem struct{ Path, Msg string }

// ValidateMatch checks a match; path prefixes each problem's path.
func ValidateMatch(path string, m Match) []Problem {
	var ps []Problem
	for _, f := range []struct{ name, val string }{{"callerPattern", m.CallerPattern}, {"did", m.DID}} {
		if f.val == "" {
			continue
		}
		if _, msg := routing.CompilePattern(f.val); msg != "" {
			ps = append(ps, Problem{path + "." + f.name, msg})
		}
	}
	if len(m.Trunk) > 64 {
		ps = append(ps, Problem{path + ".trunk", "is longer than 64 characters"})
	}
	switch m.CallerKind {
	case "", CallerExtension, CallerDoor, CallerTrunk:
	default:
		ps = append(ps, Problem{path + ".callerKind", "must be extension, door or trunk"})
	}
	if w := m.Window; w != nil {
		if !timecond.ValidZone(w.TimeZone) {
			ps = append(ps, Problem{path + ".window.timeZone", "must be an IANA time zone name such as Asia/Dubai"})
		}
		if len(w.Ranges) == 0 {
			ps = append(ps, Problem{path + ".window.ranges", "must have at least one range"})
		}
		for _, p := range timecond.ValidateRanges(path+".window.ranges", w.Ranges) {
			ps = append(ps, Problem{p.Path, p.Msg})
		}
	}
	return ps
}

// Input is what the choice reads about one call.
type Input struct {
	// Kind is the call's kind when it fixes the tag: page, intercom, alarm,
	// door or emergency-callback; empty for a plain call.
	Kind string
	// CallerNumber is the caller's number (an extension or a trunk's caller
	// ID), DID the dialled number.
	CallerNumber, DID string
	// Trunk is the inbound trunk's name, empty for a call from a phone.
	Trunk string
	// CallerKind is extension, door or trunk.
	CallerKind string
	// FromTrunk reports that the call entered on a trunk.
	FromTrunk bool
	// GroupTag is the tag of the ring group the call came through, "" when
	// the call did not come through a group. A group without an override
	// passes RingGroup.
	GroupTag string
	// At is the call's time, for time windows.
	At time.Time
}

// Choice is the tag a call rings with and what chose it.
type Choice struct {
	Tag string
	// Why names the deciding step: kind:<kind>, rule:<position>[ name], group,
	// trunk or default.
	Why string
}

type compiled struct {
	rule   Rule
	caller routing.Pattern
	hasCal bool
	did    routing.Pattern
	hasDID bool
}

// Table is the compiled, immutable choice state of one configuration.
type Table struct {
	tags  map[string]Tag
	rules []compiled
}

// NewTable compiles tags and rules. A rule that does not compile, or names a
// tag that does not exist, is skipped (the API validates, so this only guards
// a hand-edited database).
func NewTable(tags []Tag, rules []Rule) *Table {
	t := &Table{tags: map[string]Tag{}}
	for _, b := range Builtins() {
		t.tags[b.Name] = b
	}
	for _, tg := range tags {
		t.tags[tg.Name] = tg
	}
	for _, r := range rules {
		if _, ok := t.tags[r.Tag]; !ok || len(ValidateMatch("match", r.Match)) > 0 {
			continue
		}
		c := compiled{rule: r}
		if r.Match.CallerPattern != "" {
			c.caller, _ = routing.CompilePattern(r.Match.CallerPattern)
			c.hasCal = true
		}
		if r.Match.DID != "" {
			c.did, _ = routing.CompilePattern(r.Match.DID)
			c.hasDID = true
		}
		t.rules = append(t.rules, c)
	}
	return t
}

// Tags returns the known tags (built-in and custom) in no particular order.
func (t *Table) Tags() []Tag {
	if t == nil {
		return Builtins()
	}
	out := make([]Tag, 0, len(t.tags))
	for _, tg := range t.tags {
		out = append(out, tg)
	}
	return out
}

// Slot returns the Bellcore slot (1 to 5) of a tag; 1 for an unknown one.
func (t *Table) Slot(tag string) int {
	if t != nil {
		if tg, ok := t.tags[tag]; ok && tg.BellcoreSlot >= 1 && tg.BellcoreSlot <= 5 {
			return tg.BellcoreSlot
		}
	}
	for _, b := range Builtins() {
		if b.Name == tag {
			return b.BellcoreSlot
		}
	}
	return 1
}

func kindTag(kind string) string {
	switch kind {
	case KindPage, KindIntercom:
		return Page
	case KindAlarm:
		return Alarm
	case KindDoor:
		return Doorbell
	case KindEmergencyCallback:
		return EmergencyCallback
	}
	return ""
}

func (c *compiled) matches(in Input) bool {
	m := c.rule.Match
	if c.hasCal && !c.caller.Matches(in.CallerNumber) {
		return false
	}
	if c.hasDID && !c.did.Matches(in.DID) {
		return false
	}
	if m.Trunk != "" && m.Trunk != in.Trunk {
		return false
	}
	if m.CallerKind != "" && m.CallerKind != in.CallerKind {
		return false
	}
	if w := m.Window; w != nil {
		at := in.At
		if at.IsZero() {
			at = time.Now()
		}
		if !(timecond.Spec{TimeZone: w.TimeZone, Ranges: w.Ranges}).Evaluate(at).Open {
			return false
		}
	}
	return true
}

// Choose picks the tag of a call (S-40). A nil table behaves as one with no
// rules.
func (t *Table) Choose(in Input) Choice {
	if tag := kindTag(in.Kind); tag != "" {
		return Choice{tag, "kind:" + in.Kind}
	}
	if t != nil {
		for i := range t.rules {
			if t.rules[i].matches(in) {
				r := t.rules[i].rule
				why := fmt.Sprintf("rule:%d", r.Position)
				if r.Name != "" {
					why += " " + r.Name
				}
				return Choice{r.Tag, why}
			}
		}
	}
	if in.GroupTag != "" {
		return Choice{in.GroupTag, "group"}
	}
	if in.FromTrunk {
		return Choice{External, "trunk"}
	}
	return Choice{Internal, "default"}
}

// Header returns the canonical Alert-Info value for tag, in the form of
// S-41: <http://127.0.0.1/Bellcore-drN>;info=hello-<tag>. The vendor's form
// replaces it where the vendor table names one.
func (t *Table) Header(tag, userAgent string) string {
	slot := t.Slot(tag)
	ua := strings.ToLower(userAgent)
	if strings.Contains(ua, "snom") {
		// Snom matches on three named classes (S-41 vendor table, verify).
		switch tag {
		case Internal:
			return fmt.Sprintf("<http://127.0.0.1/Bellcore-dr%d>;info=alert-internal", slot)
		case External:
			return fmt.Sprintf("<http://127.0.0.1/Bellcore-dr%d>;info=alert-external", slot)
		case RingGroup:
			return fmt.Sprintf("<http://127.0.0.1/Bellcore-dr%d>;info=alert-group", slot)
		}
	}
	return fmt.Sprintf("<http://127.0.0.1/Bellcore-dr%d>;info=hello-%s", slot, tag)
}
