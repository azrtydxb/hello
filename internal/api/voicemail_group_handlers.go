package api

// Voicemail groups (spec pbx-gaps S-14): a dial number and member extensions
// that have a voicemail box. A message left for the group, or broadcast by a
// member with *63<number>, is copied into every member's box.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/azrtydxb/hello/internal/store"
)

// voicemailGroupNumberRe is a group's dial number: 2 to 16 digits, like an
// IVR menu's number.
var voicemailGroupNumberRe = regexp.MustCompile(`^[0-9]{2,16}$`)

// voicemailGroupBody is the create and replace body of a group.
type voicemailGroupBody struct {
	Number     *string  `json:"number"`
	Name       *string  `json:"name"`
	Enabled    *bool    `json:"enabled"`
	MaxSeconds *int     `json:"maxSeconds"`
	Members    *[]int64 `json:"members"`
}

func (b voicemailGroupBody) toInput() store.VoicemailGroupInput {
	in := store.VoicemailGroupInput{Enabled: true, MaxSeconds: store.DefaultVoicemailGroupSeconds}
	if b.Number != nil {
		in.Number = *b.Number
	}
	if b.Name != nil {
		in.Name = strings.TrimSpace(*b.Name)
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.MaxSeconds != nil {
		in.MaxSeconds = *b.MaxSeconds
	}
	if b.Members != nil {
		in.Members = *b.Members
	}
	return in
}

func validateVoicemailGroup(in *store.VoicemailGroupInput) fieldErrs {
	var f fieldErrs
	if !voicemailGroupNumberRe.MatchString(in.Number) {
		f.add("number", "must be 2-16 digits")
	}
	if n := len([]rune(in.Name)); n < 1 || n > 64 || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		f.add("name", "must be 1-64 printable characters")
	}
	if in.MaxSeconds < store.MinVoicemailGroupSeconds || in.MaxSeconds > store.MaxVoicemailGroupSeconds {
		f.add("maxSeconds", "must be %d-%d seconds", store.MinVoicemailGroupSeconds, store.MaxVoicemailGroupSeconds)
	}
	if len(in.Members) == 0 || len(in.Members) > store.MaxVoicemailGroupMembers {
		f.add("members", "must have 1-%d members", store.MaxVoicemailGroupMembers)
	}
	seen := map[int64]bool{}
	for i, id := range in.Members {
		if id <= 0 || seen[id] {
			f.add(fmt.Sprintf("members[%d]", i), "must be a distinct extension id")
		}
		seen[id] = true
	}
	return f
}

func (s *server) listVoicemailGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Store.ListVoicemailGroups(r.Context())
	if err != nil {
		s.internal(w, "list voicemail groups", err)
		return
	}
	writeJSON(w, http.StatusOK, items(gs))
}

func (s *server) createVoicemailGroup(w http.ResponseWriter, r *http.Request) {
	var b voicemailGroupBody
	if !decode(w, r, &b) {
		return
	}
	if b.Number == nil || b.Name == nil || b.Members == nil {
		badRequest(w, "number, name and members are required")
		return
	}
	in := b.toInput()
	if f := validateVoicemailGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.CreateVoicemailGroup(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "voicemail group", err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *server) putVoicemailGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b voicemailGroupBody
	if !decode(w, r, &b) {
		return
	}
	if b.Number == nil || b.Name == nil || b.Members == nil {
		badRequest(w, "number, name and members are required")
		return
	}
	in := b.toInput()
	if f := validateVoicemailGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.UpdateVoicemailGroup(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "voicemail group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deleteVoicemailGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteVoicemailGroup(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "voicemail group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
