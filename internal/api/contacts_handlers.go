package api

// Personal contacts and the caller-name lookup diagnostic (spec
// phonebooks-ptt, P-9, P-10, P-19). The /api/v1/me/contacts routes act on
// the caller's own book only: the owner is always the authenticated user,
// no route takes a user id, and a credential that is not a user (a service
// account) is refused. No route reads another user's contacts, an
// administrator's included.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

// ContactStore is the personal-contact persistence the API needs;
// *store.Store implements it. Every method names the owner by user id.
type ContactStore interface {
	ListContacts(ctx context.Context, userID, after int64, limit int) ([]phonebook.Entry, int64, error)
	CreateContact(ctx context.Context, actor string, userID int64, in phonebook.EntryInput) (phonebook.Entry, error)
	UpdateContact(ctx context.Context, actor string, userID, id int64, in phonebook.EntryInput) (phonebook.Entry, error)
	DeleteContact(ctx context.Context, actor string, userID, id int64) error
	ImportContacts(ctx context.Context, actor string, userID int64, in []phonebook.EntryInput, replace bool) (int, error)
	ExportContacts(ctx context.Context, userID int64) ([]phonebook.EntryInput, error)
}

// LookupStore answers the lookup diagnostic.
type LookupStore interface {
	LookupPhonebook(ctx context.Context, d phonebook.Dialing, number, callee string) (phonebook.LookupResult, error)
}

var (
	_ ContactStore = (*store.Store)(nil)
	_ LookupStore  = (*store.Store)(nil)
)

// Lookup diagnostic.

func (s *server) lookupPhonebook(w http.ResponseWriter, r *http.Request) {
	l, ok := s.Phonebooks.(LookupStore)
	if !ok || s.booksDisabled(w) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "address books are not configured")
		return
	}
	q := r.URL.Query()
	number, callee := q.Get("number"), q.Get("callee")
	if number == "" || len(number) > 128 {
		badRequest(w, "number is required (at most 128 characters)")
		return
	}
	if len(callee) > phonebook.MaxNumberLen {
		badRequest(w, "callee must be an extension number")
		return
	}
	res, err := l.LookupPhonebook(r.Context(), s.Dialing, number, callee)
	if err != nil {
		s.bookError(w, "lookup", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// My contacts.

// contactOwner returns the caller's user id. A credential that is not a user (a
// service account) gets 403 here, so nothing below can be reached with an
// owner of zero.
func (s *server) contactOwner(w http.ResponseWriter, r *http.Request) (int64, bool) {
	a := actor(r)
	if a.Kind == auth.KindService || a.UserID <= 0 {
		writeError(w, http.StatusForbidden, "no_user", "personal contacts belong to a user; this credential has none")
		return 0, false
	}
	return a.UserID, true
}

// contactsStore returns the store, answering 503 itself when there is none.
func (s *server) contactsStore(w http.ResponseWriter) (ContactStore, bool) {
	c, ok := s.Phonebooks.(ContactStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "address books are not configured")
		return nil, false
	}
	return c, true
}

// contactError answers a store failure of a personal contact. A user an
// administrator has not linked to an extension has no book: 403 with the
// code the console shows its explanation for.
func (s *server) contactError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, store.ErrNoExtensionLink) {
		writeError(w, http.StatusForbidden, "no_extension", "your account is not linked to an extension, so you have no personal contacts; ask an administrator to link it")
		return
	}
	s.bookError(w, what, err)
}

// contactSetup is the checks every /me/contacts route starts with.
func (s *server) contactSetup(w http.ResponseWriter, r *http.Request) (ContactStore, int64, bool) {
	uid, ok := s.contactOwner(w, r)
	if !ok {
		return nil, 0, false
	}
	cs, ok := s.contactsStore(w)
	if !ok {
		return nil, 0, false
	}
	return cs, uid, true
}

func (s *server) listContacts(w http.ResponseWriter, r *http.Request) {
	cs, uid, ok := s.contactSetup(w, r)
	if !ok {
		return
	}
	limit, after, ok := pageParams(w, r)
	if !ok {
		return
	}
	es, next, err := cs.ListContacts(r.Context(), uid, after, limit)
	if err != nil {
		s.contactError(w, "contact", err)
		return
	}
	writePage(w, es, next)
}

func (s *server) createContact(w http.ResponseWriter, r *http.Request) {
	cs, uid, ok := s.contactSetup(w, r)
	if !ok {
		return
	}
	in, ok := s.entryBody(w, r)
	if !ok {
		return
	}
	e, err := cs.CreateContact(r.Context(), actor(r).String(), uid, in)
	if err != nil {
		s.contactError(w, "contact", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *server) updateContact(w http.ResponseWriter, r *http.Request) {
	cs, uid, ok := s.contactSetup(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in, ok := s.entryBody(w, r)
	if !ok {
		return
	}
	e, err := cs.UpdateContact(r.Context(), actor(r).String(), uid, id, in)
	if err != nil {
		s.contactError(w, "contact", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) deleteContact(w http.ResponseWriter, r *http.Request) {
	cs, uid, ok := s.contactSetup(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := cs.DeleteContact(r.Context(), actor(r).String(), uid, id); err != nil {
		s.contactError(w, "contact", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) exportContacts(w http.ResponseWriter, r *http.Request) {
	cs, uid, ok := s.contactSetup(w, r)
	if !ok {
		return
	}
	entries, err := cs.ExportContacts(r.Context(), uid)
	if err != nil {
		s.contactError(w, "contact", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.csv"`)
	_ = phonebook.WriteCSV(w, entries)
}

func (s *server) importContacts(w http.ResponseWriter, r *http.Request) {
	cs, uid, ok := s.contactSetup(w, r)
	if !ok {
		return
	}
	dry, mode, ok := importParams(w, r.URL.Query())
	if !ok {
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
		status := http.StatusOK
		if !dry {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, res)
		return
	}
	// The book's size is checked in the import's own transaction (a dry
	// run reads it first, so the user hears about the limit before).
	if len(rows) > phonebook.MaxPersonalEntries {
		writeError(w, http.StatusConflict, "conflict", "a personal book holds at most "+strconv.Itoa(phonebook.MaxPersonalEntries)+" contacts")
		return
	}
	in := make([]phonebook.EntryInput, len(rows))
	for i, row := range rows {
		in[i] = row.Entry
	}
	res.OK = true
	if dry {
		current, err := cs.ExportContacts(r.Context(), uid)
		if err != nil {
			s.contactError(w, "contact", err)
			return
		}
		if mode == "append" && len(current)+len(in) > phonebook.MaxPersonalEntries {
			writeError(w, http.StatusConflict, "conflict", "a personal book holds at most "+strconv.Itoa(phonebook.MaxPersonalEntries)+" contacts")
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	if _, err := cs.ImportContacts(r.Context(), actor(r).String(), uid, in, mode == "replace"); err != nil {
		s.contactError(w, "contact", err)
		return
	}
	res.Imported = len(in)
	writeJSON(w, http.StatusOK, res)
}

// importParams reads dry_run and mode of an import; it answers 400 itself.
func importParams(w http.ResponseWriter, q url.Values) (dry bool, mode string, ok bool) {
	switch q.Get("dry_run") {
	case "true":
		dry = true
	case "false", "":
	default:
		badRequest(w, "dry_run must be true or false")
		return false, "", false
	}
	mode = q.Get("mode")
	if mode == "" {
		mode = "append"
	}
	if mode != "append" && mode != "replace" {
		badRequest(w, "mode must be append or replace")
		return false, "", false
	}
	return dry, mode, true
}
