package api

// Address books, their entries and phone groups (spec phonebooks-ptt, phase
// A: P-1, P-2, P-7, P-18, P-19). Personal books have no route here: the
// store never returns one to an administrator.

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// PhonebookStore is the address-book persistence the API needs;
// *store.Store implements it.
type PhonebookStore interface {
	ListPhonebooks(ctx context.Context, after int64, limit int) ([]phonebook.Book, int64, error)
	GetPhonebook(ctx context.Context, id int64) (phonebook.Book, error)
	CreatePhonebook(ctx context.Context, actor, name string) (phonebook.Book, error)
	RenamePhonebook(ctx context.Context, actor string, id int64, name string) (phonebook.Book, error)
	DeletePhonebook(ctx context.Context, actor string, id int64) error

	ListPhonebookEntries(ctx context.Context, bookID, after int64, limit int) ([]phonebook.Entry, int64, error)
	CreatePhonebookEntry(ctx context.Context, actor string, bookID int64, in phonebook.EntryInput) (phonebook.Entry, error)
	UpdatePhonebookEntry(ctx context.Context, actor string, bookID, id int64, in phonebook.EntryInput) (phonebook.Entry, error)
	DeletePhonebookEntry(ctx context.Context, actor string, bookID, id int64) error
	ImportPhonebook(ctx context.Context, actor string, bookID int64, in []phonebook.EntryInput, replace bool) (int, error)
	ExportPhonebook(ctx context.Context, bookID int64) (phonebook.Book, []phonebook.EntryInput, error)

	ListPhoneGroups(ctx context.Context, after int64, limit int) ([]phonebook.Group, int64, error)
	GetPhoneGroup(ctx context.Context, id int64) (phonebook.Group, error)
	CreatePhoneGroup(ctx context.Context, actor string, in store.GroupInput) (phonebook.Group, error)
	UpdatePhoneGroup(ctx context.Context, actor string, id int64, in store.GroupInput) (phonebook.Group, error)
	DeletePhoneGroup(ctx context.Context, actor string, id int64) error
	SetPhoneGroupBooks(ctx context.Context, actor string, id int64, bookIDs []int64) (phonebook.Group, error)
	SetPhoneGroupMulticast(ctx context.Context, actor string, id int64, in []phonebook.MulticastInput) (phonebook.Group, error)
	SetPhoneGroupPTT(ctx context.Context, actor string, id int64, in phonebook.PTT) (phonebook.Group, error)
}

// Page sizes of the address-book lists.
const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// page is a list with the cursor of its next page ("" on the last).
type page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next"`
}

func writePage[T any](w http.ResponseWriter, v []T, next int64) {
	if v == nil {
		v = []T{}
	}
	p := page[T]{Items: v}
	if next > 0 {
		p.Next = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, p)
}

// pageParams reads limit and cursor; anything out of range is a 400.
func pageParams(w http.ResponseWriter, r *http.Request) (limit int, after int64, ok bool) {
	limit = defaultPageSize
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageSize {
			badRequest(w, "limit must be 1-200")
			return 0, 0, false
		}
		limit = n
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			badRequest(w, "cursor is not one a list returned")
			return 0, 0, false
		}
		after = n
	}
	return limit, after, true
}

func (s *server) booksDisabled(w http.ResponseWriter) bool {
	if s.Phonebooks == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "address books are not configured")
		return true
	}
	return false
}

// pathInt64 parses a path value that must be a positive integer; anything
// else is a 404, as for {id}.
func pathInt64(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return 0, false
	}
	return id, true
}

// bookError answers a store failure of a book, entry or group.
func (s *server) bookError(w http.ResponseWriter, what string, err error) {
	s.configError(w, what, err)
}

func writePhonebookFields(w http.ResponseWriter, fields []phonebook.FieldError) {
	out := make([]routing.FieldError, len(fields))
	for i, f := range fields {
		out[i] = routing.FieldError{Path: f.Path, Message: f.Message}
	}
	writeFields(w, out)
}

// Books.

type bookBody struct {
	Name string `json:"name"`
}

func (b bookBody) validate() []phonebook.FieldError {
	if !phonebook.ValidName(b.Name, phonebook.MaxBookNameLen) {
		return []phonebook.FieldError{{Path: "name", Message: "must be 1-100 printable characters without surrounding space"}}
	}
	return nil
}

func (s *server) listPhonebooks(w http.ResponseWriter, r *http.Request) {
	if s.booksDisabled(w) {
		return
	}
	limit, after, ok := pageParams(w, r)
	if !ok {
		return
	}
	bs, next, err := s.Phonebooks.ListPhonebooks(r.Context(), after, limit)
	if err != nil {
		s.internal(w, "list phonebooks", err)
		return
	}
	writePage(w, bs, next)
}

func (s *server) createPhonebook(w http.ResponseWriter, r *http.Request) {
	if s.booksDisabled(w) {
		return
	}
	var in bookBody
	if !decode(w, r, &in) {
		return
	}
	if f := in.validate(); len(f) > 0 {
		writePhonebookFields(w, f)
		return
	}
	b, err := s.Phonebooks.CreatePhonebook(r.Context(), actor(r).String(), in.Name)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *server) getPhonebook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	b, err := s.Phonebooks.GetPhonebook(r.Context(), id)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *server) updatePhonebook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	var in bookBody
	if !decode(w, r, &in) {
		return
	}
	if f := in.validate(); len(f) > 0 {
		writePhonebookFields(w, f)
		return
	}
	b, err := s.Phonebooks.RenamePhonebook(r.Context(), actor(r).String(), id, in.Name)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *server) deletePhonebook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	if err := s.Phonebooks.DeletePhonebook(r.Context(), actor(r).String(), id); err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Entries.

func (s *server) listPhonebookEntries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	limit, after, ok := pageParams(w, r)
	if !ok {
		return
	}
	es, next, err := s.Phonebooks.ListPhonebookEntries(r.Context(), id, after, limit)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	writePage(w, es, next)
}

// entryBody decodes and checks an entry; it answers 400 itself.
func (s *server) entryBody(w http.ResponseWriter, r *http.Request) (phonebook.EntryInput, bool) {
	var in phonebook.EntryInput
	if !decode(w, r, &in) {
		return in, false
	}
	if f := in.Normalize(s.Dialing); len(f) > 0 {
		writePhonebookFields(w, f)
		return in, false
	}
	return in, true
}

func (s *server) createPhonebookEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	in, ok := s.entryBody(w, r)
	if !ok {
		return
	}
	e, err := s.Phonebooks.CreatePhonebookEntry(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *server) updatePhonebookEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	entry, ok := pathInt64(w, r, "entryId")
	if !ok || s.booksDisabled(w) {
		return
	}
	in, ok := s.entryBody(w, r)
	if !ok {
		return
	}
	e, err := s.Phonebooks.UpdatePhonebookEntry(r.Context(), actor(r).String(), id, entry, in)
	if err != nil {
		s.bookError(w, "entry", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) deletePhonebookEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	entry, ok := pathInt64(w, r, "entryId")
	if !ok || s.booksDisabled(w) {
		return
	}
	if err := s.Phonebooks.DeletePhonebookEntry(r.Context(), actor(r).String(), id, entry); err != nil {
		s.bookError(w, "entry", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CSV.

// importResult is the answer of an import, a dry run or not.
type importResult struct {
	OK       bool                   `json:"ok"`
	DryRun   bool                   `json:"dryRun"`
	Mode     string                 `json:"mode"`
	Rows     int                    `json:"rows"`
	Imported int                    `json:"imported"`
	Errors   []phonebook.RowError   `json:"errors"`
	Warnings []phonebook.RowWarning `json:"warnings"`
}

// csvUpload returns the "file" part of a multipart request body, limited
// to the CSV size limit. It answers 400 itself.
func csvUpload(w http.ResponseWriter, r *http.Request) (io.Reader, bool) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		badRequest(w, "send the CSV as multipart/form-data in the part \"file\"")
		return nil, false
	}
	// A part header or two on top of the file itself.
	mr := multipart.NewReader(http.MaxBytesReader(w, r.Body, phonebook.MaxCSVBytes+64<<10), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				badRequest(w, "the CSV is larger than 2 MiB")
			} else {
				badRequest(w, "the multipart body has no part \"file\"")
			}
			return nil, false
		}
		if p.FormName() == "file" {
			return p, true
		}
	}
}

func (s *server) importPhonebook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
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
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "append"
	}
	if mode != "append" && mode != "replace" {
		badRequest(w, "mode must be append or replace")
		return
	}
	book, err := s.Phonebooks.GetPhonebook(r.Context(), id)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	if book.Kind == phonebook.KindExtensions {
		writeError(w, http.StatusConflict, "conflict", "the extensions book is computed from the extensions and cannot be imported into")
		return
	}
	file, ok := csvUpload(w, r)
	if !ok {
		return
	}
	rows, errs, warns, err := phonebook.ParseCSV(file, s.Dialing)
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, phonebook.ErrCSVTooLarge), errors.As(err, &tooBig):
		badRequest(w, "the CSV is larger than 2 MiB")
		return
	case errors.Is(err, phonebook.ErrCSVTooManyRows):
		badRequest(w, "the CSV has more than 5000 rows")
		return
	case err != nil:
		badRequest(w, "the CSV could not be read")
		return
	}
	res := importResult{DryRun: dry, Mode: mode, Rows: len(rows) + len(errs), Errors: errs, Warnings: warns}
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
	if mode == "append" && book.EntryCount+len(rows) > phonebook.MaxEntriesPerBook {
		writeError(w, http.StatusConflict, "conflict", "a book holds at most "+strconv.Itoa(phonebook.MaxEntriesPerBook)+" entries")
		return
	}
	res.OK = true
	if !dry {
		in := make([]phonebook.EntryInput, len(rows))
		for i, row := range rows {
			in[i] = row.Entry
		}
		if _, err := s.Phonebooks.ImportPhonebook(r.Context(), actor(r).String(), id, in, mode == "replace"); err != nil {
			s.bookError(w, "phonebook", err)
			return
		}
		res.Imported = len(in)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) exportPhonebook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	_, entries, err := s.Phonebooks.ExportPhonebook(r.Context(), id)
	if err != nil {
		s.bookError(w, "phonebook", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="phonebook-`+strconv.FormatInt(id, 10)+`.csv"`)
	_ = phonebook.WriteCSV(w, entries)
}

// Phone groups.

type groupBody struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	IncludeExtensions *bool  `json:"includeExtensions"`
	IncludeCompany    *bool  `json:"includeCompany"`
}

// input checks the body; the switches default to on.
func (b groupBody) input() (store.GroupInput, []phonebook.FieldError) {
	var f []phonebook.FieldError
	if !phonebook.ValidName(b.Name, phonebook.MaxBookNameLen) {
		f = append(f, phonebook.FieldError{Path: "name", Message: "must be 1-100 printable characters without surrounding space"})
	}
	if len([]rune(b.Description)) > phonebook.MaxDescriptionLen || !printable(b.Description, phonebook.MaxDescriptionLen, true) {
		f = append(f, phonebook.FieldError{Path: "description", Message: "must be at most 255 printable characters"})
	}
	in := store.GroupInput{Name: b.Name, Description: b.Description, IncludeExtensions: true, IncludeCompany: true}
	if b.IncludeExtensions != nil {
		in.IncludeExtensions = *b.IncludeExtensions
	}
	if b.IncludeCompany != nil {
		in.IncludeCompany = *b.IncludeCompany
	}
	return in, f
}

func (s *server) listPhoneGroups(w http.ResponseWriter, r *http.Request) {
	if s.booksDisabled(w) {
		return
	}
	limit, after, ok := pageParams(w, r)
	if !ok {
		return
	}
	gs, next, err := s.Phonebooks.ListPhoneGroups(r.Context(), after, limit)
	if err != nil {
		s.internal(w, "list phone groups", err)
		return
	}
	writePage(w, gs, next)
}

func (s *server) createPhoneGroup(w http.ResponseWriter, r *http.Request) {
	if s.booksDisabled(w) {
		return
	}
	var body groupBody
	if !decode(w, r, &body) {
		return
	}
	in, f := body.input()
	if len(f) > 0 {
		writePhonebookFields(w, f)
		return
	}
	g, err := s.Phonebooks.CreatePhoneGroup(r.Context(), actor(r).String(), in)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *server) getPhoneGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	g, err := s.Phonebooks.GetPhoneGroup(r.Context(), id)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) updatePhoneGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	var body groupBody
	if !decode(w, r, &body) {
		return
	}
	in, f := body.input()
	if len(f) > 0 {
		writePhonebookFields(w, f)
		return
	}
	g, err := s.Phonebooks.UpdatePhoneGroup(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deletePhoneGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	if err := s.Phonebooks.DeletePhoneGroup(r.Context(), actor(r).String(), id); err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) putPhoneGroupBooks(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	var in struct {
		BookIDs *[]int64 `json:"bookIds"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.BookIDs == nil {
		badRequest(w, "bookIds is required (an empty list removes every book)")
		return
	}
	g, err := s.Phonebooks.SetPhoneGroupBooks(r.Context(), actor(r).String(), id, *in.BookIDs)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// putPhoneGroupMulticast replaces a group's multicast paging channels
// (spec P-15): every phone of the group is provisioned to listen on the
// channels that listen and to send on those that send.
func (s *server) putPhoneGroupMulticast(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	var in struct {
		Channels *[]phonebook.MulticastInput `json:"channels"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Channels == nil {
		badRequest(w, "channels is required (an empty list removes every channel)")
		return
	}
	if f := phonebook.ValidateMulticast(*in.Channels); len(f) > 0 {
		writePhonebookFields(w, f)
		return
	}
	g, err := s.Phonebooks.SetPhoneGroupMulticast(r.Context(), actor(r).String(), id, *in.Channels)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// putPhoneGroupPTT replaces a group's push-to-talk settings and channels
// (phone-feature-parity S-30). Only phones that support PTT (Grandstream
// WP8x6) are given them.
func (s *server) putPhoneGroupPTT(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	in := phonebook.PTT{Address: phonebook.DefaultPTTAddress, Codec: phonebook.CodecPCMU, DefaultChannel: 1}
	if !decode(w, r, &in) {
		return
	}
	if in.Channels == nil {
		in.Channels = []phonebook.PTTChannel{}
	}
	if f := phonebook.ValidatePTT(in); len(f) > 0 {
		writePhonebookFields(w, f)
		return
	}
	g, err := s.Phonebooks.SetPhoneGroupPTT(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
