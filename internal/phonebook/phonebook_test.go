package phonebook

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestPhonebookKey (spec P-8) fails if the stored and dialled forms of one
// number give different keys, a short code, a feature code, anonymous or a
// non-number resolves, or an extension loses its form.
func TestPhonebookKey(t *testing.T) {
	ext := func(n string) bool { return n == "599" || n == "1001" }
	ch := Dialing{CountryCode: "41"}
	for _, tc := range []struct {
		name, in string
		d        Dialing
		want     string // empty: unresolvable
	}{
		{"e164 with spaces", "+41 79 123 45 67", ch, "+41791234567"},
		{"international prefix", "0041791234567", ch, "+41791234567"},
		{"national with country code", "079 123 45 67", ch, "+41791234567"},
		{"national with brackets", "(079) 123-45-67", ch, "+41791234567"},
		{"sip uri", "sip:+41791234567@host;user=phone", ch, "+41791234567"},
		{"sips uri with params in the user", "sips:+41791234567;phone-context=x@host", ch, "+41791234567"},
		{"tel uri", "tel:+41-79-123-45-67;ext=1", ch, "+41791234567"},
		{"encoded plus", "sip:%2B41791234567@host", ch, "+41791234567"},
		{"dots", "+41.79.123.45.67", ch, "+41791234567"},
		{"national without a country code stays national", "079 123 45 67", Dialing{}, "0791234567"},
		{"custom international prefix", "011 41 79 123 45 67", Dialing{IntlPrefix: "011"}, "+41791234567"},
		{"extension", "599", ch, "599"},
		{"long extension", "sip:1001@pbx", ch, "1001"},
		{"feature code", "*72599", ch, ""},
		{"anonymous", "anonymous", ch, ""},
		{"anonymous uri", "sip:Anonymous@anonymous.invalid", ch, ""},
		{"short code that is no extension", "112", ch, ""},
		{"service number", "0800", Dialing{}, "0800"},
		{"letters", "4179abc", ch, ""},
		{"plus in the middle", "41+79123456", ch, ""},
		{"just a plus", "+", ch, ""},
		{"just a prefix", "00", ch, ""},
		{"empty", "  ", ch, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.d.Key(tc.in, ext)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("Key(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
			}
		})
	}

	t.Run("without extension knowledge a short number never resolves", func(t *testing.T) {
		if k, ok := Key("599"); ok {
			t.Fatalf("Key(599) = %q", k)
		}
	})
	t.Run("one number, one key", func(t *testing.T) {
		forms := []string{"+41 79 123 45 67", "0041791234567", "079 123 45 67", "(079) 123-45-67", "sip:+41791234567@host;user=phone"}
		for _, f := range forms {
			if k, _ := ch.Key(f, ext); k != "+41791234567" {
				t.Errorf("%q -> %q", f, k)
			}
		}
	})
}

// TestSuffix fails if the fallback key is not the last nine digits, or is
// made of fewer.
func TestSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"+41791234567": "791234567", "0791234567": "791234567", "791234567": "791234567",
		"+4179123456": "179123456", "79123456": "", "599": "",
	} {
		got, ok := Suffix(in)
		if got != want || ok != (want != "") {
			t.Errorf("Suffix(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// TestEntryNormalize fails if an entry without a name or a number is
// accepted, an unknown or repeated label, a field over its limit counted in
// bytes rather than characters, or a control character passes.
func TestEntryNormalize(t *testing.T) {
	ok := func() EntryInput {
		return EntryInput{FirstName: " Ada ", LastName: "Lovelace", Numbers: []Number{{Label: "work", Number: " +41 79 123 45 67 "}}}
	}
	in := ok()
	if f := in.Normalize(Dialing{}); len(f) != 0 {
		t.Fatalf("a valid entry: %v", f)
	}
	if in.FirstName != "Ada" || in.Numbers[0].Number != "+41 79 123 45 67" || in.Numbers[0].Key != "+41791234567" {
		t.Fatalf("not normalised: %+v", in)
	}
	for _, tc := range []struct {
		name string
		edit func(*EntryInput)
		path string
	}{
		{"no name", func(e *EntryInput) { e.FirstName, e.LastName = "", "" }, "lastName"},
		{"company is a name", func(e *EntryInput) { e.FirstName, e.LastName, e.Company = "", "", "ACME" }, ""},
		{"no number", func(e *EntryInput) { e.Numbers = nil }, "numbers"},
		{"four numbers", func(e *EntryInput) {
			e.Numbers = []Number{{"work", "1234", ""}, {"mobile", "2345", ""}, {"home", "3456", ""}, {"other", "4567", ""}}
		}, "numbers"},
		{"unknown label", func(e *EntryInput) { e.Numbers[0].Label = "fax" }, "numbers[0].label"},
		{"repeated label", func(e *EntryInput) { e.Numbers = append(e.Numbers, Number{Label: "work", Number: "1234"}) }, "numbers[1].label"},
		{"empty number", func(e *EntryInput) { e.Numbers[0].Number = " " }, "numbers[0].number"},
		{"number over 32", func(e *EntryInput) { e.Numbers[0].Number = strings.Repeat("1", 33) }, "numbers[0].number"},
		{"name over 60", func(e *EntryInput) { e.LastName = strings.Repeat("x", 61) }, "lastName"},
		{"name of 60 multibyte characters passes", func(e *EntryInput) { e.LastName = strings.Repeat("é", 60) }, ""},
		{"note over 255", func(e *EntryInput) { e.Note = strings.Repeat("n", 256) }, "note"},
		{"control character", func(e *EntryInput) { e.FirstName = "A\x00da" }, "firstName"},
		{"note keeps line breaks", func(e *EntryInput) { e.Note = "a\nb\tc" }, ""},
		{"line break in a name", func(e *EntryInput) { e.LastName = "a\nb" }, "lastName"},
		{"invalid utf-8", func(e *EntryInput) { e.Company = "a\xffb" }, "company"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := ok()
			tc.edit(&e)
			f := e.Normalize(Dialing{})
			switch {
			case tc.path == "" && len(f) != 0:
				t.Fatalf("rejected: %v", f)
			case tc.path != "" && (len(f) == 0 || f[0].Path != tc.path):
				t.Fatalf("errors %v, want one at %s", f, tc.path)
			}
		})
	}
}

func parse(t *testing.T, s string) ([]CSVRow, []RowError, []RowWarning) {
	t.Helper()
	rows, errs, warns, err := ParseCSV(strings.NewReader(s), Dialing{CountryCode: "41"})
	if err != nil {
		t.Fatal(err)
	}
	return rows, errs, warns
}

// TestParseCSV fails if a BOM, semicolons, CRLF, quoted newlines or a
// reordered header are not read, a bad cell is not reported with its row
// and column, one bad row lets the rest through unreported, or the limits
// are not enforced.
func TestParseCSV(t *testing.T) {
	t.Run("forms", func(t *testing.T) {
		const body = "first_name,last_name,company,work,mobile,home,other,note\r\n" +
			"Ada,Lovelace,Analytical,+41 44 111 22 33,079 123 45 67,,,\"line one\r\nline two\"\r\n"
		want := EntryInput{FirstName: "Ada", LastName: "Lovelace", Company: "Analytical",
			Numbers: []Number{{"work", "+41 44 111 22 33", "+41441112233"}, {"mobile", "079 123 45 67", "+41791234567"}},
			Note:    "line one\nline two"}
		for name, in := range map[string]string{
			"comma":     body,
			"bom":       "\xef\xbb\xbf" + body,
			"semicolon": strings.ReplaceAll(body, ",", ";"),
			"lf":        strings.ReplaceAll(body, "\r\n", "\n"),
		} {
			rows, errs, _ := parse(t, in)
			if len(errs) != 0 || len(rows) != 1 {
				t.Fatalf("%s: rows %v errors %v", name, rows, errs)
			}
			got := rows[0].Entry
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: got %+v, want %+v", name, got, want)
			}
			if rows[0].Row != 2 {
				t.Errorf("%s: row = %d, want 2", name, rows[0].Row)
			}
		}
	})
	t.Run("reordered and partial header", func(t *testing.T) {
		rows, errs, _ := parse(t, "mobile,Last_Name\n+41791234567,Smith\n")
		if len(errs) != 0 || len(rows) != 1 || rows[0].Entry.LastName != "Smith" || rows[0].Entry.Numbers[0].Label != "mobile" {
			t.Fatalf("rows %+v errors %v", rows, errs)
		}
	})
	t.Run("errors name row and column and do not stop the rest", func(t *testing.T) {
		rows, errs, _ := parse(t, "first_name,last_name,work,mobile\n"+
			"A,One,1234,\n"+ // row 2 fine
			",,5678,\n"+ // row 3 no name
			"B,Two,,\n"+ // row 4 no number
			"C,Three,"+strings.Repeat("9", 40)+",\n"+ // row 5 number too long, in column work
			"D,Four,x\x01y,\n"+ // row 6 control character, in column work
			"E,Five,4321,\n") // row 7 fine
		if len(rows) != 2 || rows[0].Row != 2 || rows[1].Row != 7 {
			t.Fatalf("good rows = %+v", rows)
		}
		want := []RowError{{3, "last_name", ""}, {4, "work", ""}, {5, "work", ""}, {6, "work", ""}}
		if len(errs) != len(want) {
			t.Fatalf("errors = %+v", errs)
		}
		for i, w := range want {
			if errs[i].Row != w.Row || errs[i].Column != w.Column || errs[i].Reason == "" {
				t.Errorf("error %d = %+v, want row %d column %s", i, errs[i], w.Row, w.Column)
			}
		}
	})
	t.Run("header problems", func(t *testing.T) {
		for _, in := range []string{"", "last_name,bogus\nx,y\n", "last_name,last_name,work\nx,y,1234\n", "\"unclosed,last_name\n"} {
			rows, errs, _ := parse(t, in)
			if len(rows) != 0 || len(errs) == 0 || errs[0].Row != 1 {
				t.Errorf("%q: rows %v errors %v", in, rows, errs)
			}
		}
	})
	t.Run("too many cells", func(t *testing.T) {
		_, errs, _ := parse(t, "last_name,work\nx,1234,extra\n")
		if len(errs) != 1 || errs[0].Row != 2 {
			t.Fatalf("errors = %v", errs)
		}
	})
	t.Run("blank lines are skipped", func(t *testing.T) {
		rows, errs, _ := parse(t, "last_name,work\n\nx,1234\n,\ny,2345\n")
		if len(errs) != 0 || len(rows) != 2 {
			t.Fatalf("rows %v errors %v", rows, errs)
		}
	})
	t.Run("duplicate numbers warn", func(t *testing.T) {
		rows, errs, warns := parse(t, "last_name,work\nx,+41 79 123 45 67\ny,0791234567\nz,2222\n")
		if len(rows) != 3 || len(errs) != 0 || len(warns) != 1 || warns[0].Row != 3 {
			t.Fatalf("rows %d errors %v warnings %v", len(rows), errs, warns)
		}
	})
	t.Run("row limit", func(t *testing.T) {
		build := func(n int) string {
			var b strings.Builder
			b.WriteString("last_name,work\n")
			for i := range n {
				fmt.Fprintf(&b, "n%d,%d\n", i, 10000+i)
			}
			return b.String()
		}
		if rows, errs, _ := parse(t, build(MaxCSVRows)); len(rows) != MaxCSVRows || len(errs) != 0 {
			t.Fatalf("%d rows, errors %d", len(rows), len(errs))
		}
		if _, _, _, err := ParseCSV(strings.NewReader(build(MaxCSVRows+1)), Dialing{}); !errors.Is(err, ErrCSVTooManyRows) {
			t.Fatalf("5001 rows: %v", err)
		}
	})
	t.Run("size limit", func(t *testing.T) {
		big := "last_name,work\nx," + strings.Repeat("1", MaxCSVBytes) + "\n"
		if _, _, _, err := ParseCSV(strings.NewReader(big), Dialing{}); !errors.Is(err, ErrCSVTooLarge) {
			t.Fatalf("over 2 MiB: %v", err)
		}
	})
}

// TestCSVRoundTrip fails if export neutralises a formula cell wrongly (or
// not at all), or an exported book does not import back to the same entries.
func TestCSVRoundTrip(t *testing.T) {
	d := Dialing{CountryCode: "41"}
	in := []EntryInput{
		{FirstName: "=HYPERLINK(\"x\")", LastName: "@evil", Company: "-1+1", Note: "+cmd|' /C calc'!A0",
			Numbers: []Number{{Label: "work", Number: "+41 44 111 22 33"}, {Label: "other", Number: "=1+1"}}},
		{FirstName: "Zoë", LastName: "Müller, \"Jr\"", Company: "Ünï;cödé 😀", Note: "multi\nline",
			Numbers: []Number{{Label: "home", Number: "044 111 22 33"}}},
		{LastName: "RTL שלום", Numbers: []Number{{Label: "mobile", Number: "1234"}}},
	}
	for i := range in {
		if f := in[i].Normalize(d); len(f) != 0 {
			t.Fatalf("entry %d: %v", i, f)
		}
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, in); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, cell := range []string{"'=HYPERLINK", "'@evil", "'-1+1", "'+cmd", "'+41 44 111 22 33", "'=1+1"} {
		if !strings.Contains(out, cell) {
			t.Errorf("export lacks %q:\n%s", cell, out)
		}
	}
	if !strings.HasPrefix(out, strings.Join(CSVColumns, ",")+"\n") {
		t.Errorf("header: %q", out)
	}
	rows, errs, _ := parse(t, out)
	if len(errs) != 0 || len(rows) != len(in) {
		t.Fatalf("re-import: rows %d errors %v", len(rows), errs)
	}
	for i, r := range rows {
		if !reflect.DeepEqual(r.Entry, in[i]) {
			t.Errorf("entry %d:\n got %+v\nwant %+v", i, r.Entry, in[i])
		}
	}
}
