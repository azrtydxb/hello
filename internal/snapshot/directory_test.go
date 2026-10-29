package snapshot

import (
	"slices"
	"testing"
)

func dirNumbers(es []DirectoryEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Number)
	}
	return out
}

// TestDirectorySearch covers the keypad matching: first or last name, last
// name order, hidden and nameless extensions, accents, and bad keys.
func TestDirectorySearch(t *testing.T) {
	s := New(1, "d", nil).WithExtensionData([]Extension{
		{Number: "1", Name: "Bob Smith"}, {Number: "2", Name: "Carol Jones"}, {Number: "3", Name: "Cathy Jonas"},
		{Number: "4", Name: "Zed Zebra", DirectoryHidden: true}, {Number: "5", Name: ""},
		{Number: "6", Name: "José Müller"}, {Number: "7", Name: "Madonna"},
	})
	for _, c := range []struct {
		keys string
		want []string
	}{
		{"764", []string{"1"}},                               // Smi
		{"262", []string{"1"}},                               // Bob, the first name
		{"566", []string{"3", "2"}},                          // Jon: Jonas before Jones
		{"933", nil},                                         // Zed is hidden
		{"56", []string{"3", "2", "6"}},                      // jo: Jonas, Jones, José (first name; last Müller sorts after)
		{"6855", []string{"6"}},                              // Müll: the accent folds to u
		{"62366", []string{"7"}},                             // Madon: a single word is first and last
		{"", nil}, {"0", nil}, {"1", nil}, {"76476476", nil}, // empty, no letters, eight keys
	} {
		if got := dirNumbers(s.DirectorySearch(c.keys)); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.keys, got, c.want)
		}
	}
}

func TestKeypadSpelling(t *testing.T) {
	if got := KeypadSpelling("Zoë-Ann O'Neil"); got != "96326666345" {
		t.Fatalf("got %q", got)
	}
}
