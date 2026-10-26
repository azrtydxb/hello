package store

import "testing"

// TestDialConflicts: the overlap rules of the dial space. It fails if a
// feature code prefix, a paging code prefix, an overlapping park range or a
// slot on an extension number is not reported, or two disjoint claims are.
func TestDialConflicts(t *testing.T) {
	feat := func(code string) dialItem { return dialItem{kind: "feature code", key: code, code: code} }
	page := func(code string) dialItem { return dialItem{kind: "paging code", key: code, code: code} }
	lot := func(lo, hi int) dialItem { return dialItem{kind: "park slots", key: "lot", lo: lo, hi: hi} }

	for name, tc := range map[string]struct {
		items []dialItem
		names map[string]string
		want  int
	}{
		"disjoint":            {items: []dialItem{feat("*40"), feat("*41"), feat("*74"), page("*60")}, want: 0},
		"feature prefix":      {items: []dialItem{feat("*8"), feat("*80")}, want: 1},
		"equal":               {items: []dialItem{feat("*60"), page("*60")}, want: 1},
		"page by feature":     {items: []dialItem{feat("*60"), page("*601")}, want: 1},
		"park range overlap":  {items: []dialItem{lot(701, 720), lot(720, 730)}, want: 1},
		"park range disjoint": {items: []dialItem{lot(701, 720), lot(721, 730)}, want: 0},
		"slot on extension":   {items: []dialItem{lot(701, 720)}, names: map[string]string{"705": "extension 705"}, want: 1},
		"slot on other ext":   {items: []dialItem{lot(701, 720)}, names: map[string]string{"101": "extension 101"}, want: 0},
		"codes vs slots":      {items: []dialItem{feat("*74"), lot(701, 720)}, want: 0},
	} {
		if got := dialConflicts(tc.items, tc.names); len(got) != tc.want {
			t.Errorf("%s: %d conflicts %v, want %d", name, len(got), got, tc.want)
		}
	}
}
