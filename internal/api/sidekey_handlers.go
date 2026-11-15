package api

import (
	"net/http"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// putPhoneGroupSideKey sets what the side key of a group's Grandstream WP
// phones does (phone-feature-parity S-12): ptt (PTT/paging), alarm (the
// group's alarm call; a WP8x6 only, and only with an alarm destination,
// otherwise the phone keeps PTT/paging) or none.
func (s *server) putPhoneGroupSideKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	var in struct {
		SideKey string `json:"sideKey"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !prov.ValidSideKey(in.SideKey) {
		writePhonebookFields(w, []phonebook.FieldError{{Path: "sideKey", Message: "must be ptt, alarm or none"}})
		return
	}
	g, err := s.Phonebooks.SetPhoneGroupSideKey(r.Context(), actor(r).String(), id, in.SideKey)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
