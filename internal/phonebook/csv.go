package phonebook

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// CSVColumns is the header of the CSV form (spec P-7).
var CSVColumns = []string{"first_name", "last_name", "company", "work", "mobile", "home", "other", "note"}

var (
	// ErrCSVTooLarge rejects a file over MaxCSVBytes.
	ErrCSVTooLarge = errors.New("phonebook: csv is over 2 MiB")
	// ErrCSVTooManyRows rejects a file over MaxCSVRows data rows.
	ErrCSVTooManyRows = errors.New("phonebook: csv has more than 5000 rows")
)

// RowError is one problem of one CSV cell. Row 1 is the header, so the
// first entry is row 2, as a spreadsheet numbers it.
type RowError struct {
	Row    int    `json:"row"`
	Column string `json:"column"`
	Reason string `json:"reason"`
}

// RowWarning is something allowed but worth a look in one CSV row.
type RowWarning struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

// CSVRow is one parsed data row.
type CSVRow struct {
	Row   int
	Entry EntryInput
}

// ParseCSV reads the CSV form: a header naming columns of CSVColumns (any
// order, none twice), UTF-8 with or without a byte order mark, comma or
// semicolon separated (detected from the header), CRLF or LF. Every row is
// checked; the entries of all rows are returned alongside the problems, so a
// dry run can report them all. A file over MaxCSVBytes or MaxCSVRows is an
// error and nothing else is reported.
func ParseCSV(r io.Reader, d Dialing) (rows []CSVRow, errs []RowError, warns []RowWarning, err error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxCSVBytes+1))
	if err != nil {
		return nil, nil, nil, err
	}
	if len(raw) > MaxCSVBytes {
		return nil, nil, nil, ErrCSVTooLarge
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	head, _, _ := bytes.Cut(raw, []byte("\n"))
	cr := csv.NewReader(bytes.NewReader(raw))
	cr.Comma = ','
	if bytes.Count(head, []byte(";")) > bytes.Count(head, []byte(",")) {
		cr.Comma = ';'
	}
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = false
	header, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, []RowError{{1, "", "the header row is required"}}, nil, nil
		}
		return nil, []RowError{{1, "", csvProblem(err)}}, nil, nil
	}
	cols := make([]string, len(header))
	for i, h := range header {
		cols[i] = strings.ToLower(strings.TrimSpace(h))
		switch {
		case !slices.Contains(CSVColumns, cols[i]):
			errs = append(errs, RowError{1, h, "unknown column; the columns are " + strings.Join(CSVColumns, ", ")})
		case slices.Contains(cols[:i], cols[i]):
			errs = append(errs, RowError{1, h, "column given twice"})
		}
	}
	if len(errs) > 0 {
		return nil, errs, nil, nil
	}
	seen := map[string]int{} // lookup key to the first row with it
	total := 0
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if total++; total > MaxCSVRows {
			return nil, nil, nil, ErrCSVTooManyRows
		}
		if err != nil {
			errs = append(errs, RowError{line, "", csvProblem(err)})
			continue
		}
		if blankRecord(rec) {
			continue
		}
		if len(rec) > len(cols) {
			errs = append(errs, RowError{line, "", fmt.Sprintf("%d cells, the header has %d columns", len(rec), len(cols))})
			continue
		}
		row, rowErrs := csvEntry(line, cols, rec, d)
		errs = append(errs, rowErrs...)
		if len(rowErrs) > 0 {
			continue
		}
		for _, n := range row.Entry.Numbers {
			if n.Key == "" {
				continue
			}
			if first, dup := seen[n.Key]; dup {
				warns = append(warns, RowWarning{line, fmt.Sprintf("number %s is also in row %d", n.Number, first)})
			} else {
				seen[n.Key] = line
			}
		}
		rows = append(rows, row)
	}
	return rows, errs, warns, nil
}

// csvProblem describes a malformed CSV record without echoing its content.
func csvProblem(err error) string {
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		switch {
		case errors.Is(pe.Err, csv.ErrQuote), errors.Is(pe.Err, csv.ErrBareQuote):
			return "malformed quoting"
		case errors.Is(pe.Err, csv.ErrFieldCount):
			return "wrong number of cells"
		}
	}
	return "malformed row"
}

func blankRecord(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// csvEntry builds the entry of one record and checks it, naming the column
// of each failure.
func csvEntry(line int, cols, rec []string, d Dialing) (CSVRow, []RowError) {
	cell := func(name string) string {
		i := slices.Index(cols, name)
		if i < 0 || i >= len(rec) {
			return ""
		}
		return unneutralize(strings.TrimSpace(rec[i]))
	}
	in := EntryInput{FirstName: cell("first_name"), LastName: cell("last_name"), Company: cell("company"), Note: cell("note")}
	var labels []string
	for _, l := range Labels {
		if v := cell(l); v != "" {
			in.Numbers = append(in.Numbers, Number{Label: l, Number: v})
			labels = append(labels, l)
		}
	}
	if len(in.Numbers) > MaxNumbersPerEntry {
		return CSVRow{}, []RowError{{line, labels[MaxNumbersPerEntry], fmt.Sprintf("an entry has at most %d numbers", MaxNumbersPerEntry)}}
	}
	var errs []RowError
	for _, f := range in.Normalize(d) {
		errs = append(errs, RowError{line, csvColumn(f.Path, in.Numbers), f.Message})
	}
	return CSVRow{Row: line, Entry: in}, errs
}

// csvColumn maps an EntryInput field path to the CSV column holding it.
func csvColumn(path string, numbers []Number) string {
	switch {
	case path == "firstName":
		return "first_name"
	case path == "lastName":
		return "last_name"
	case strings.HasPrefix(path, "numbers["):
		var i int
		if _, err := fmt.Sscanf(path, "numbers[%d]", &i); err == nil && i < len(numbers) {
			return numbers[i].Label
		}
		return "work"
	case path == "numbers":
		return "work"
	}
	return path
}

// WriteCSV writes entries in the CSV form, header first. A cell a
// spreadsheet would evaluate as a formula (starting with = + - or @) gets a
// leading apostrophe; ParseCSV removes it again.
func WriteCSV(w io.Writer, entries []EntryInput) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVColumns); err != nil {
		return err
	}
	for _, e := range entries {
		rec := []string{e.FirstName, e.LastName, e.Company, "", "", "", "", e.Note}
		for _, n := range e.Numbers {
			if i := slices.Index(CSVColumns, n.Label); i >= 0 {
				rec[i] = n.Number
			}
		}
		for i := range rec {
			rec[i] = neutralize(rec[i])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func formulaStart(s string) bool {
	return s != "" && strings.ContainsRune("=+-@", rune(s[0]))
}

func neutralize(s string) string {
	if formulaStart(s) {
		return "'" + s
	}
	return s
}

// unneutralize undoes neutralize: an apostrophe in front of a formula
// start is the export's.
func unneutralize(s string) string {
	if len(s) > 1 && s[0] == '\'' && formulaStart(s[1:]) {
		return s[1:]
	}
	return s
}
