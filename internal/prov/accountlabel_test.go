package prov

import (
	"context"
	"strings"
	"testing"
)

func TestAccountLabel(t *testing.T) {
	for _, c := range []struct{ num, name, want string }{
		{"101", "Office", "101 (Office)"},
		{"101", "", "101"},
		{"101", "  ", "101"},
		{"101", "101", "101"},
		{"", "Office", "Office"},
		{"101", "Zoë <R&D> \"x\"", "101 (Zoë <R&D> \"x\")"},
		{"101", "A\r\nB\tC", "101 (A B C)"},
	} {
		if got := AccountLabel(c.num, c.name); got != c.want {
			t.Errorf("AccountLabel(%q,%q) = %q, want %q", c.num, c.name, got, c.want)
		}
	}
}

// TestAccountLabelRendered fails if a vendor's account name key does not
// carry "number (name)" escaped for its format, or if the SIP user, auth
// name or caller-ID display name changed.
func TestAccountLabelRendered(t *testing.T) {
	cases := []struct {
		v       Vendor
		model   string
		file    string
		want    []string // the label line
		display []string // the unchanged caller ID display name
	}{
		{Yealink, "T54W", "{mac}.cfg", []string{"account.1.label = 1001 (Zoë <R&D> \"Q\")\n"}, []string{"account.1.display_name = Zoë <R&D> \"Q\"\n"}},
		{Fanvil, "X5U", "{mac}.cfg", []string{"SIP1 Sip Name      :1001 (Zoë <R&D> \"Q\")\n"}, []string{"SIP1 Display Name  :Zoë <R&D> \"Q\"\n"}},
		{Grandstream, "GRP2614", "cfg{mac}.xml", []string{"<P270>1001 (Zoë &lt;R&amp;D&gt; &#34;Q&#34;)</P270>"}, []string{"<P3>Zoë &lt;R&amp;D&gt; &#34;Q&#34;</P3>"}},
		{Snom, "D785", "{MAC}", []string{"<user_idle_text idx=\"1\" perm=\"R\">1001 (Zoë &lt;R&amp;D&gt; &#34;Q&#34;)</user_idle_text>"}, []string{"<user_realname idx=\"1\" perm=\"R\">Zoë &lt;R&amp;D&gt; &#34;Q&#34;</user_realname>"}},
		{Poly, "VVX 450", "{mac}-hello.cfg", []string{`reg.1.label="1001 (Zoë &lt;R&amp;D&gt; &#34;Q&#34;)"`}, []string{`reg.1.displayName="Zoë &lt;R&amp;D&gt; &#34;Q&#34;"`}},
	}
	for _, c := range cases {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = c.v, c.model
		d.Line.DisplayName = `Zoë <R&D> "Q"`
		file := ExpandPattern(c.file, d.Phone)
		out, err := Render(context.Background(), builtinFor(t, c.v), file, d)
		if err != nil {
			t.Fatalf("%s: %v", c.v, err)
		}
		for _, w := range append(c.want, c.display...) {
			if !strings.Contains(string(out), w) {
				t.Errorf("%s: %q missing from\n%s", c.v, w, out)
			}
		}
		for _, w := range []string{d.Line.Username, d.Line.AuthName} {
			if !strings.Contains(string(out), w) {
				t.Errorf("%s: %q missing", c.v, w)
			}
		}
	}

	// No name: the bare number.
	d := renderSample(false)
	d.Line.DisplayName = ""
	out, err := Render(context.Background(), builtinFor(t, Yealink), d.Phone.MAC+".cfg", d)
	if err != nil || !strings.Contains(string(out), "account.1.label = 1001\n") {
		t.Errorf("empty name: %v\n%s", err, out)
	}
}
