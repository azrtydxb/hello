package prov

import (
	"strings"
	"testing"
)

// TestGrandstreamBookKeys fails if the keys that decide what a phonebook
// download does to the entries on the phone are not those of the model's own
// template: P333 (Remove Manually-edited Entries on Download) in
// wp8x6_config_1.0.3.39 and wp810_822_825_config_1.0.11.83; P8462 (Import
// Group Method, 0 Replace) in the WP8x6 template only; P1435 (Clear The Old
// List, 1 Clear all) in wp820_config_1.0.7.90 and wp856_config_1.0.3.16.
func TestGrandstreamBookKeys(t *testing.T) {
	for _, c := range []struct {
		model     string
		want, not []string
	}{
		{"WP836", []string{"<P333>1</P333>", "<P8462>0</P8462>"}, []string{"<P1435>"}},
		{"WP822", []string{"<P333>1</P333>"}, []string{"<P8462>", "<P1435>"}},
		{"WP820", []string{"<P1435>1</P1435>", "<P332>30</P332>"}, []string{"<P333>", "<P8462>"}},
		{"WP856", []string{"<P1435>1</P1435>", "<P332>30</P332>"}, []string{"<P333>", "<P8462>"}},
	} {
		d := gsFamilyData(c.model)
		d.Book.RefreshMinutes = 15
		got := renderDevice(t, Grandstream, d)
		for _, k := range append([]string{"<P330>3</P330>"}, c.want...) {
			if !strings.Contains(got, k) {
				t.Errorf("%s lacks %s", c.model, k)
			}
		}
		for _, k := range c.not {
			if strings.Contains(got, k) {
				t.Errorf("%s writes %s, which its template does not define", c.model, k)
			}
		}
	}
}
