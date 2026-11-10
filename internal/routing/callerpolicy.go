package routing

import (
	"fmt"
	"slices"
	"strings"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// Caller screening for calls that arrive on a trunk (pbx-gaps S-7, S-8): the
// blocklist (global or per trunk), anonymous reject and the country
// restriction. It is pure and allocation-light: hello-sip evaluates it on
// the INVITE from the configuration snapshot, with no database call.

// Blocklist entry kinds and actions.
const (
	BlockExact  = "exact"
	BlockPrefix = "prefix"

	ActionBlock = "block"
	ActionAllow = "allow"
)

// Unknown-caller actions: what a trunk with a country restriction does with
// a caller number that cannot be normalised (pbx-gaps D7).
const (
	UnknownAllow = "allow"
	UnknownBlock = "block"
)

// Reasons a caller is screened out; they are the label of
// hello_blocked_calls_total, so the set is bounded.
const (
	ReasonBlocklist = "blocklist"
	ReasonAnonymous = "anonymous"
	ReasonCountry   = "country"
)

// MaxBlocklistEntries bounds the blocklist (pbx-gaps S-7).
const MaxBlocklistEntries = 10000

// MaxAllowedCountryCodes bounds a trunk's country list.
const MaxAllowedCountryCodes = 50

// Reject codes a trunk may answer a screened call with (pbx-gaps D6).
const (
	RejectDecline  = 603
	RejectBusyHere = 486
)

// CallerPolicy is a trunk's own screening: the zero value screens nothing.
type CallerPolicy struct {
	// RejectAnonymous turns away calls whose caller is withheld.
	RejectAnonymous bool `json:"rejectAnonymous"`
	// AllowedCountryCodes are calling codes in digits ("41", "49"); empty
	// is no restriction.
	AllowedCountryCodes []string `json:"allowedCountryCodes"`
	// UnknownCallerAction is "allow" or "block" for a number that cannot be
	// normalised while a restriction is set; empty is "block".
	UnknownCallerAction string `json:"unknownCallerAction"`
	// RejectCode is the final response of a screened call: 603 (default)
	// or 486.
	RejectCode int `json:"rejectCode"`
}

// Code is the final response status of a screened call.
func (p CallerPolicy) Code() int {
	if p.RejectCode == RejectBusyHere {
		return RejectBusyHere
	}
	return RejectDecline
}

// ValidateCallerPolicy checks p, appending a FieldError under path for each
// problem.
func ValidateCallerPolicy(p CallerPolicy, path string, errs *[]FieldError) {
	if p.RejectCode != 0 && p.RejectCode != RejectDecline && p.RejectCode != RejectBusyHere {
		addErr(errs, path+".rejectCode", "must be 603 or 486")
	}
	switch p.UnknownCallerAction {
	case "", UnknownAllow, UnknownBlock:
	default:
		addErr(errs, path+".unknownCallerAction", `must be "allow" or "block"`)
	}
	if len(p.AllowedCountryCodes) > MaxAllowedCountryCodes {
		addErr(errs, path+".allowedCountryCodes", fmt.Sprintf("at most %d country codes are allowed", MaxAllowedCountryCodes))
	}
	for i, c := range p.AllowedCountryCodes {
		if !validCountryCode(c) {
			addErr(errs, fmt.Sprintf("%s.allowedCountryCodes[%d]", path, i), "must be a country calling code of 1-3 digits such as 41, without a leading 0 or +")
		}
	}
}

func validCountryCode(c string) bool {
	if len(c) < 1 || len(c) > 3 || c[0] == '0' {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < '0' || c[i] > '9' {
			return false
		}
	}
	return true
}

// NormalizeCountryCodes returns codes with a leading "+" removed, sorted and
// without duplicates, and nil when there are none.
func NormalizeCountryCodes(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		c = strings.TrimPrefix(strings.TrimSpace(c), "+")
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// BlockEntry is one blocklist row as the engine sees it. Pattern is the
// stored form: a phonebook key, with a trailing "*" on a prefix entry.
type BlockEntry struct {
	ID      int64
	Pattern string
	Match   string // "exact" | "prefix"
	Action  string // "block" | "allow"
	// TrunkID scopes the entry to one trunk; 0 is global.
	TrunkID int64
	// Announcement names an announcement played to a blocked caller before
	// the call ends; empty plays none.
	Announcement string
}

type blockKey struct {
	trunk int64
	key   string
}

// Blocklist is the compiled blocklist; the zero value and nil match nothing.
type Blocklist struct {
	// allow and block, by exact key and by prefix key, per scope.
	allowExact, blockExact   map[blockKey]*BlockEntry
	allowPrefix, blockPrefix map[blockKey]*BlockEntry
	maxPrefix                int
	entries                  int
}

// Len is the number of compiled entries.
func (b *Blocklist) Len() int {
	if b == nil {
		return 0
	}
	return b.entries
}

// CompileBlocklist indexes entries. A disabled entry is not passed in; an
// entry with a pattern that does not parse is skipped (the API refuses to
// store one). Of two entries with the same pattern and scope the lower id
// wins.
func CompileBlocklist(entries []BlockEntry) *Blocklist {
	b := &Blocklist{
		allowExact: map[blockKey]*BlockEntry{}, blockExact: map[blockKey]*BlockEntry{},
		allowPrefix: map[blockKey]*BlockEntry{}, blockPrefix: map[blockKey]*BlockEntry{},
	}
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(x, y BlockEntry) int { return int(x.ID - y.ID) })
	for i := range sorted {
		e := &sorted[i]
		key := strings.TrimSuffix(e.Pattern, "*")
		if key == "" || e.Action != ActionAllow && e.Action != ActionBlock {
			continue
		}
		var m map[blockKey]*BlockEntry
		switch {
		case e.Match == BlockPrefix && e.Action == ActionAllow:
			m = b.allowPrefix
		case e.Match == BlockPrefix:
			m = b.blockPrefix
		case e.Action == ActionAllow:
			m = b.allowExact
		default:
			m = b.blockExact
		}
		k := blockKey{e.TrunkID, key}
		if _, dup := m[k]; dup {
			continue
		}
		m[k] = e
		b.entries++
		if e.Match == BlockPrefix && len(key) > b.maxPrefix {
			b.maxPrefix = len(key)
		}
	}
	return b
}

// find returns the most specific entry of one action for a caller key on a
// trunk: an exact match beats a prefix, a longer prefix beats a shorter one,
// and a trunk's own entry beats a global one of the same specificity.
func (b *Blocklist) find(exact, prefix map[blockKey]*BlockEntry, trunk int64, key string) *BlockEntry {
	for _, scope := range [2]int64{trunk, 0} {
		if e := exact[blockKey{scope, key}]; e != nil {
			return e
		}
		if trunk == 0 {
			break
		}
	}
	if len(prefix) == 0 {
		return nil
	}
	for n := min(len(key), b.maxPrefix); n > 0; n-- {
		for _, scope := range [2]int64{trunk, 0} {
			if e := prefix[blockKey{scope, key[:n]}]; e != nil {
				return e
			}
			if trunk == 0 {
				break
			}
		}
	}
	return nil
}

// Verdict is the outcome of screening one inbound trunk call.
type Verdict struct {
	// Blocked is true when the call is turned away; Reason is then one of
	// the Reason constants.
	Blocked bool
	Reason  string
	// Code is the final response when Blocked.
	Code int
	// Entry is the blocklist entry that decided the call, if one did.
	Entry *BlockEntry
	// Trace is the one-line explanation, without any number in it.
	Trace string
}

// IsAnonymous reports whether a caller is withheld: a From user of
// "anonymous" (or the other words carriers use for it), an empty user, or a
// Privacy header asking for id privacy.
func IsAnonymous(user string, privacyID bool) bool {
	if privacyID {
		return true
	}
	u := strings.TrimSpace(user)
	if u == "" {
		return true
	}
	switch strings.ToLower(u) {
	case "anonymous", "restricted", "withheld", "unavailable", "unknown", "private":
		return true
	}
	return false
}

// Check screens a call from caller on trunk t in the order of pbx-gaps S-8:
// allow entries, anonymous reject, block entries, then the country
// restriction. It does not apply to internal calls; the caller never asks
// for those. A nil Blocklist has no entries.
func (b *Blocklist) Check(t *Trunk, caller string, privacyID bool, d phonebook.Dialing) Verdict {
	anonymous := IsAnonymous(caller, privacyID)
	var key string
	var keyOK bool
	if !anonymous {
		key, keyOK = d.Key(caller, nil)
	}
	pol := t.CallerPolicy
	block := func(reason string, e *BlockEntry, why string) Verdict {
		return Verdict{Blocked: true, Reason: reason, Code: pol.Code(), Entry: e, Trace: why}
	}
	if b != nil && keyOK {
		if e := b.find(b.allowExact, b.allowPrefix, t.ID, key); e != nil {
			return Verdict{Entry: e, Trace: fmt.Sprintf("Caller allowed by blocklist entry %d", e.ID)}
		}
	}
	if pol.RejectAnonymous && anonymous {
		return block(ReasonAnonymous, nil, fmt.Sprintf("Anonymous caller rejected by trunk %s", t.Name))
	}
	if b != nil && keyOK {
		if e := b.find(b.blockExact, b.blockPrefix, t.ID, key); e != nil {
			return block(ReasonBlocklist, e, fmt.Sprintf("Caller blocked by blocklist entry %d", e.ID))
		}
	}
	if len(pol.AllowedCountryCodes) > 0 {
		if !keyOK || key[0] != '+' {
			if pol.UnknownCallerAction == UnknownAllow {
				return Verdict{Trace: fmt.Sprintf("Caller country unknown, allowed by trunk %s", t.Name)}
			}
			return block(ReasonCountry, nil, fmt.Sprintf("Caller country unknown, blocked by trunk %s", t.Name))
		}
		digits := key[1:]
		for _, c := range pol.AllowedCountryCodes {
			if strings.HasPrefix(digits, c) {
				return Verdict{}
			}
		}
		return block(ReasonCountry, nil, fmt.Sprintf("Caller country not allowed on trunk %s", t.Name))
	}
	return Verdict{}
}

// NormalizeBlockPattern turns an administrator's number into the stored
// pattern: the phonebook key of the number, and for a prefix a trailing "*".
// why is empty when the pattern is valid.
func NormalizeBlockPattern(raw, match string, d phonebook.Dialing) (stored, why string) {
	raw = strings.TrimSpace(raw)
	switch match {
	case BlockExact:
		k, ok := d.Key(raw, nil)
		if !ok {
			return "", "is not a number that can be matched: it needs at least 4 digits, with only digits, +, spaces, dots, dashes and brackets"
		}
		return k, ""
	case BlockPrefix:
		base := strings.TrimSpace(strings.TrimSuffix(raw, "*"))
		if base == "" {
			return "", "is empty"
		}
		// A prefix may be shorter than a whole number (+44), which the key
		// refuses; pad it, normalise, and cut the padding off again.
		const pad = "0000"
		k, ok := d.Key(base+pad, nil)
		if !ok || !strings.HasSuffix(k, pad) {
			return "", "is not a prefix that can be matched: use digits, +, spaces, dots, dashes and brackets"
		}
		k = strings.TrimSuffix(k, pad)
		if digits := strings.TrimPrefix(k, "+"); digits == "" || k[0] == '+' && digits[0] == '0' {
			return "", "is not a prefix that can be matched"
		}
		return k + "*", ""
	}
	return "", `match must be "exact" or "prefix"`
}
