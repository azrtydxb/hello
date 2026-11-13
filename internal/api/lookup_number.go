package api

// Contact lookup for integrations (spec integrations I-43): one number, the
// best name Hello has for it.

import (
	"errors"
	"net/http"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

// personalPage is how many contacts one read of a personal book takes; the
// book holds at most phonebook.MaxPersonalEntries.
const personalPage = 500

// lookupNumber is GET /api/v1/lookup/number?number=: the extensions book and
// the company book answer as they do for a caller's name
// (GET /phonebooks/lookup, without a callee); a credential that belongs to a
// user and found nothing there also searches that user's personal book. The
// personal book is the last resort so that the answer for a number never
// depends on who asks when the company already names it.
func (s *server) lookupNumber(w http.ResponseWriter, r *http.Request) {
	l, ok := s.Phonebooks.(LookupStore)
	if !ok || s.booksDisabled(w) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "address books are not configured")
		return
	}
	number := r.URL.Query().Get("number")
	if number == "" || len(number) > 128 {
		badRequest(w, "number is required (at most 128 characters)")
		return
	}
	res, err := l.LookupPhonebook(r.Context(), s.Dialing, number, "")
	if err != nil {
		s.bookError(w, "lookup", err)
		return
	}
	if res.Result == phonebook.LookupNone {
		if a := actor(r); a.Kind != auth.KindService && a.UserID > 0 {
			if err := s.lookupPersonal(r, a.UserID, &res); err != nil {
				s.contactError(w, "lookup", err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// lookupPersonal completes res from the user's personal book. A user with no
// linked extension has no book, which is "nothing found", not an error.
func (s *server) lookupPersonal(r *http.Request, userID int64, res *phonebook.LookupResult) error {
	cs, ok := s.Phonebooks.(ContactStore)
	if !ok {
		return nil
	}
	var all []phonebook.Entry
	var after int64
	for {
		page, next, err := cs.ListContacts(r.Context(), userID, after, personalPage)
		if errors.Is(err, store.ErrNoExtensionLink) {
			return nil
		}
		if err != nil {
			return err
		}
		all = append(all, page...)
		if next == 0 || len(page) == 0 {
			break
		}
		after = next
	}
	res.Searched = append(res.Searched, "personal")
	e, via, matches, found := phonebook.PersonalMatch(all, res.Key)
	switch {
	case found:
		res.Result, res.Via, res.Source, res.Book = phonebook.LookupMatch, via, string(phonebook.KindPersonal), "Personal"
		res.BookID, res.EntryID, res.Name = e.BookID, e.ID, e.DisplayName()
	case matches > 1:
		res.Result, res.Matches = phonebook.LookupAmbiguous, matches
	}
	return nil
}
