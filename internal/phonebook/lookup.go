package phonebook

import "strings"

// Lookup outcomes (spec P-10).
const (
	LookupMatch        = "match"        // exactly one entry names the number
	LookupAmbiguous    = "ambiguous"    // several entries share the last 9 digits: no name is set
	LookupNone         = "none"         // no book has the number
	LookupUnresolvable = "unresolvable" // anonymous, a feature or short code, or not a number
)

// LookupResult answers the lookup diagnostic: which book and entry would
// name a number. It never reads a personal book.
type LookupResult struct {
	Number string `json:"number"`
	Callee string `json:"callee,omitempty"`
	// Key is the lookup key of Number; empty when it is unresolvable.
	Key    string `json:"key"`
	Result string `json:"result"`
	// Via is how the entry matched: "exact" on the key or "suffix" on the
	// last 9 digits.
	Via string `json:"via,omitempty"`
	// Source is the kind of book that names the number (extensions, named
	// or company), as a CDR's caller_name_source holds it.
	Source string `json:"source,omitempty"`
	Book   string `json:"book,omitempty"`
	BookID int64  `json:"bookId,omitempty"`
	// EntryID is the matching entry's id (an extension's id for the
	// extensions book) and Name what the callee's phone would show.
	EntryID int64  `json:"entryId,omitempty"`
	Name    string `json:"name,omitempty"`
	// Matches is how many entries share the suffix when ambiguous.
	Matches int `json:"matches,omitempty"`
	// Searched lists the books looked in, in order.
	Searched []string `json:"searched"`
}

// DisplayName is the name an entry gives a caller: first and last name, or
// the company when it has neither.
func (in EntryInput) DisplayName() string {
	if n := strings.TrimSpace(in.FirstName + " " + in.LastName); n != "" {
		return n
	}
	return in.Company
}
