package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// The call blocklist (pbx-gaps S-7). Operators manage the entries; the
// reason note is read and written freely but shown to administrators only.

// BlocklistStore is the blocklist persistence; *store.Store implements it.
type BlocklistStore interface {
	ListBlocklist(ctx context.Context, f store.BlocklistFilter) ([]store.BlocklistEntry, string, int, error)
	GetBlocklistEntry(ctx context.Context, id int64) (store.BlocklistEntry, error)
	CreateBlocklistEntry(ctx context.Context, actor string, in store.BlocklistInput) (store.BlocklistEntry, error)
	UpdateBlocklistEntry(ctx context.Context, actor string, id int64, apply func(cur store.BlocklistInput) (store.BlocklistInput, error)) (store.BlocklistEntry, error)
	DeleteBlocklistEntry(ctx context.Context, actor string, id int64) error
	ImportBlocklist(ctx context.Context, actor string, rows []store.BlocklistInput) (store.BlocklistImportResult, error)
}

const (
	// maxBlocklistCSVBytes and maxBlocklistCSVRows bound an import file.
	maxBlocklistCSVBytes = 2 << 20
	maxBlocklistReason   = 120
)

// blocklistBody is the create and replace body.
type blocklistBody struct {
	Pattern        *string `json:"pattern"`
	Match          *string `json:"match"`
	Action         *string `json:"action"`
	TrunkID        *int64  `json:"trunkId"`
	Reason         *string `json:"reason"`
	AnnouncementID *int64  `json:"announcementId"`
	Enabled        *bool   `json:"enabled"`
}

func (s *server) blocklistDisabled(w http.ResponseWriter) bool {
	if s.Blocklist == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the blocklist is not configured")
		return true
	}
	return false
}

// blocklistView hides the reason from anyone below admin.
func blocklistView(r *http.Request, e store.BlocklistEntry) store.BlocklistEntry {
	if !actor(r).Role.AtLeast(auth.RoleAdmin) {
		e.Reason = ""
	}
	return e
}

// blocklistError maps a store error to a response.
func (s *server) blocklistError(w http.ResponseWriter, err error) {
	var v *store.ValidationError
	switch {
	case errors.As(err, &v):
		writeFields(w, v.Fields)
	case errors.Is(err, store.ErrBlocklistFull):
		writeError(w, http.StatusConflict, "conflict", "the blocklist holds at most "+strconv.Itoa(routing.MaxBlocklistEntries)+" entries")
	default:
		s.storeError(w, "blocklist entry", err)
	}
}

// blocklistInput validates body on top of base (the defaults of a create, or
// the current values of a replace) and returns the stored form.
func (s *server) blocklistInput(b blocklistBody, base store.BlocklistInput) (store.BlocklistInput, fieldErrs) {
	in := base
	setIf(&in.Match, b.Match)
	setIf(&in.Action, b.Action)
	setIf(&in.Enabled, b.Enabled)
	in.TrunkID, in.AnnouncementID = b.TrunkID, b.AnnouncementID
	if b.Reason != nil {
		in.Reason = *b.Reason
	}
	var f fieldErrs
	switch in.Match {
	case routing.BlockExact, routing.BlockPrefix:
	default:
		f.add("match", `must be "exact" or "prefix"`)
	}
	switch in.Action {
	case routing.ActionBlock, routing.ActionAllow:
	default:
		f.add("action", `must be "block" or "allow"`)
	}
	if utf8.RuneCountInString(in.Reason) > maxBlocklistReason || !printable(in.Reason, maxBlocklistReason*4, true) {
		f.add("reason", "must be at most %d printable characters", maxBlocklistReason)
	}
	if in.TrunkID != nil && *in.TrunkID <= 0 {
		f.add("trunkId", "must be a trunk id or null")
	}
	if in.AnnouncementID != nil && *in.AnnouncementID <= 0 {
		f.add("announcementId", "must be an announcement id or null")
	}
	if in.AnnouncementID != nil && in.Action != routing.ActionBlock {
		f.add("announcementId", "is only played for a block entry")
	}
	if b.Pattern != nil && len(f) == 0 {
		p, why := routing.NormalizeBlockPattern(*b.Pattern, in.Match, s.Dialing)
		if why != "" {
			f.add("pattern", "%s", why)
		}
		in.Pattern = p
	} else if b.Pattern == nil && in.Pattern == "" {
		f.add("pattern", "is required")
	}
	return in, f
}

func (s *server) listBlocklist(w http.ResponseWriter, r *http.Request) {
	if s.blocklistDisabled(w) {
		return
	}
	q := r.URL.Query()
	f := store.BlocklistFilter{Q: strings.TrimSpace(q.Get("q")), Action: q.Get("action"), Scope: q.Get("scope"), Limit: 200}
	switch f.Action {
	case "", routing.ActionBlock, routing.ActionAllow:
	default:
		badRequest(w, "action must be block or allow")
		return
	}
	if f.Scope != "" && f.Scope != "global" {
		if id, err := strconv.ParseInt(f.Scope, 10, 64); err != nil || id <= 0 {
			badRequest(w, `scope must be "global" or a trunk id`)
			return
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			badRequest(w, "limit must be 1-1000")
			return
		}
		f.Limit = n
	}
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "after must be an entry id")
			return
		}
		f.After = n
	}
	es, next, total, err := s.Blocklist.ListBlocklist(r.Context(), f)
	if err != nil {
		s.internal(w, "list blocklist", err)
		return
	}
	for i := range es {
		es[i] = blocklistView(r, es[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": es, "next": next, "total": total})
}

func (s *server) createBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	if s.blocklistDisabled(w) {
		return
	}
	var b blocklistBody
	if !decode(w, r, &b) {
		return
	}
	if b.Pattern == nil {
		writeFields(w, fieldErrs{{Path: "pattern", Message: "is required"}})
		return
	}
	in, f := s.blocklistInput(b, store.BlocklistInput{Match: routing.BlockExact, Action: routing.ActionBlock, Enabled: true})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	e, err := s.Blocklist.CreateBlocklistEntry(r.Context(), actor(r).String(), in)
	if err != nil {
		s.blocklistError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, blocklistView(r, e))
}

func (s *server) putBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.blocklistDisabled(w) {
		return
	}
	var b blocklistBody
	if !decode(w, r, &b) {
		return
	}
	if b.Pattern == nil {
		writeFields(w, fieldErrs{{Path: "pattern", Message: "is required"}})
		return
	}
	e, err := s.Blocklist.UpdateBlocklistEntry(r.Context(), actor(r).String(), id, func(cur store.BlocklistInput) (store.BlocklistInput, error) {
		// A replace: absent fields take their defaults, except the reason,
		// which an operator cannot read and so cannot send back.
		base := store.BlocklistInput{Match: routing.BlockExact, Action: routing.ActionBlock, Reason: cur.Reason, Enabled: true}
		in, f := s.blocklistInput(b, base)
		if len(f) > 0 {
			return in, &store.ValidationError{Fields: f}
		}
		return in, nil
	})
	if err != nil {
		s.blocklistError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, blocklistView(r, e))
}

func (s *server) deleteBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.blocklistDisabled(w) {
		return
	}
	if err := s.Blocklist.DeleteBlocklistEntry(r.Context(), actor(r).String(), id); err != nil {
		s.storeError(w, "blocklist entry", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// blocklistImportResult is the answer to an import or its dry run.
type blocklistImportResult struct {
	OK       bool                   `json:"ok"`
	DryRun   bool                   `json:"dryRun"`
	Rows     int                    `json:"rows"`
	Created  int                    `json:"created"`
	Updated  int                    `json:"updated"`
	Errors   []phonebook.RowError   `json:"errors"`
	Warnings []phonebook.RowWarning `json:"warnings"`
}

// blocklistCSVColumns are the columns of the CSV form; number is required.
var blocklistCSVColumns = []string{"number", "action", "match", "scope", "reason"}

// parseBlocklistCSV reads the CSV form and returns the rows to store, with
// every problem of every row (so a dry run reports them all).
func (s *server) parseBlocklistCSV(raw []byte, trunks map[string]int64) (rows []store.BlocklistInput, errs []phonebook.RowError, warns []phonebook.RowWarning, total int, err error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	head, _, _ := bytes.Cut(raw, []byte("\n"))
	cr := csv.NewReader(bytes.NewReader(raw))
	cr.Comma = ','
	if bytes.Count(head, []byte(";")) > bytes.Count(head, []byte(",")) {
		cr.Comma = ';'
	}
	cr.FieldsPerRecord = -1
	header, herr := cr.Read()
	if herr != nil {
		if errors.Is(herr, io.EOF) {
			return nil, []phonebook.RowError{{Row: 1, Reason: "the header row is required"}}, nil, 0, nil
		}
		return nil, []phonebook.RowError{{Row: 1, Reason: "the CSV could not be read"}}, nil, 0, nil
	}
	col := map[string]int{}
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(h))
		switch {
		case !containsStr(blocklistCSVColumns, name):
			errs = append(errs, phonebook.RowError{Row: 1, Column: h, Reason: "unknown column; the columns are " + strings.Join(blocklistCSVColumns, ", ")})
		case hasKey(col, name):
			errs = append(errs, phonebook.RowError{Row: 1, Column: h, Reason: "column given twice"})
		default:
			col[name] = i
		}
	}
	if !hasKey(col, "number") {
		errs = append(errs, phonebook.RowError{Row: 1, Column: "number", Reason: "the number column is required"})
	}
	if len(errs) > 0 {
		return nil, errs, nil, 0, nil
	}
	cell := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	seen := map[string]int{}
	n := 1
	for {
		rec, rerr := cr.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		n++
		if n-1 > routing.MaxBlocklistEntries {
			return nil, nil, nil, 0, errCSVRows
		}
		if rerr != nil {
			total++
			errs = append(errs, phonebook.RowError{Row: n, Reason: "the row could not be read"})
			continue
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // a blank line
		}
		total++
		before := len(errs)
		in := store.BlocklistInput{Match: routing.BlockExact, Action: routing.ActionBlock, Enabled: true}
		number := cell(rec, "number")
		if v := strings.ToLower(cell(rec, "match")); v != "" {
			in.Match = v
		} else if strings.HasSuffix(number, "*") {
			in.Match = routing.BlockPrefix
		}
		if in.Match != routing.BlockExact && in.Match != routing.BlockPrefix {
			errs = append(errs, phonebook.RowError{Row: n, Column: "match", Reason: `must be "exact" or "prefix"`})
		}
		if v := strings.ToLower(cell(rec, "action")); v != "" {
			in.Action = v
		}
		if in.Action != routing.ActionBlock && in.Action != routing.ActionAllow {
			errs = append(errs, phonebook.RowError{Row: n, Column: "action", Reason: `must be "block" or "allow"`})
		}
		if sc := cell(rec, "scope"); sc != "" && !strings.EqualFold(sc, "global") {
			if id, ok := trunks[sc]; ok {
				in.TrunkID = &id
			} else {
				errs = append(errs, phonebook.RowError{Row: n, Column: "scope", Reason: `must be "global" or the name of a trunk`})
			}
		}
		in.Reason = cell(rec, "reason")
		if utf8.RuneCountInString(in.Reason) > maxBlocklistReason {
			errs = append(errs, phonebook.RowError{Row: n, Column: "reason", Reason: "is longer than 120 characters"})
		}
		if in.Match == routing.BlockExact || in.Match == routing.BlockPrefix {
			p, why := routing.NormalizeBlockPattern(number, in.Match, s.Dialing)
			if why != "" {
				errs = append(errs, phonebook.RowError{Row: n, Column: "number", Reason: why})
			}
			in.Pattern = p
		}
		if len(errs) > before {
			continue
		}
		scope := int64(0)
		if in.TrunkID != nil {
			scope = *in.TrunkID
		}
		k := in.Match + "|" + in.Pattern + "|" + strconv.FormatInt(scope, 10)
		if first, dup := seen[k]; dup {
			warns = append(warns, phonebook.RowWarning{Row: n, Reason: "the same number and scope as row " + strconv.Itoa(first) + "; this row wins"})
		}
		seen[k] = n
		rows = append(rows, in)
	}
	return rows, errs, warns, total, nil
}

var errCSVRows = errors.New("blocklist csv has too many rows")

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasKey(m map[string]int, k string) bool { _, ok := m[k]; return ok }

func (s *server) importBlocklist(w http.ResponseWriter, r *http.Request) {
	if s.blocklistDisabled(w) {
		return
	}
	var dry bool
	switch r.URL.Query().Get("dry_run") {
	case "true":
		dry = true
	case "false", "":
	default:
		badRequest(w, "dry_run must be true or false")
		return
	}
	file, ok := csvUpload(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBlocklistCSVBytes+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig) || len(raw) > maxBlocklistCSVBytes:
		badRequest(w, "the CSV is larger than 2 MiB")
		return
	case err != nil:
		badRequest(w, "the CSV could not be read")
		return
	}
	trunks := map[string]int64{}
	ts, err := s.Store.ListTrunks(r.Context())
	if err != nil {
		s.internal(w, "list trunks", err)
		return
	}
	for _, t := range ts {
		trunks[t.Name] = t.ID
	}
	rows, errs, warns, total, err := s.parseBlocklistCSV(raw, trunks)
	if errors.Is(err, errCSVRows) {
		badRequest(w, "the CSV has more than "+strconv.Itoa(routing.MaxBlocklistEntries)+" rows")
		return
	}
	if err != nil {
		badRequest(w, "the CSV could not be read")
		return
	}
	res := blocklistImportResult{DryRun: dry, Rows: total, Errors: errs, Warnings: warns}
	if res.Errors == nil {
		res.Errors = []phonebook.RowError{}
	}
	if res.Warnings == nil {
		res.Warnings = []phonebook.RowWarning{}
	}
	if len(errs) > 0 {
		// A dry run reports the rows; an import that would not apply
		// answers the same result as a 400 and wrote nothing.
		status := http.StatusOK
		if !dry {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, res)
		return
	}
	res.OK = true
	if !dry {
		out, err := s.Blocklist.ImportBlocklist(r.Context(), actor(r).String(), rows)
		if err != nil {
			s.blocklistError(w, err)
			return
		}
		res.Created, res.Updated = out.Created, out.Updated
	}
	writeJSON(w, http.StatusOK, res)
}
