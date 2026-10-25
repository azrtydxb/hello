package api

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/azrtydxb/hello/internal/store"
)

// Server paging groups (phonebooks-ptt spec P-12, P-18). Reads are open to a
// viewer; every change is admin only (spec P-19), because a paging group can
// turn a phone into a listening device.

var pageCodeRe = regexp.MustCompile(`^\*[0-9]{2,4}$`)

// pagingBody is the create and replace body. Replace (PUT) takes the whole
// group; create applies defaults to absent optional fields.
type pagingBody struct {
	Name     *string  `json:"name"`
	Code     *string  `json:"code"`
	Priority *string  `json:"priority"`
	Enabled  *bool    `json:"enabled"`
	Members  *[]int64 `json:"members"`
	Callers  *[]int64 `json:"callers"`
}

// toInput applies the body over defaults: priority normal, enabled, no
// caller restriction.
func (b pagingBody) toInput() store.PagingGroupInput {
	in := store.PagingGroupInput{Priority: store.PagingNormal, Enabled: true}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Code != nil {
		in.Code = *b.Code
	}
	if b.Priority != nil {
		in.Priority = *b.Priority
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.Members != nil {
		in.Members = *b.Members
	}
	if b.Callers != nil {
		in.Callers = *b.Callers
	}
	return in
}

// validatePagingGroup checks a full input (stage 1).
func validatePagingGroup(in *store.PagingGroupInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if !pageCodeRe.MatchString(in.Code) {
		f.add("code", "must be '*' plus 2-4 digits")
	}
	if in.Priority != store.PagingNormal && in.Priority != store.PagingEmergency {
		f.add("priority", `must be "normal" or "emergency"`)
	}
	if len(in.Members) == 0 || len(in.Members) > store.MaxPagingMembers {
		f.add("members", "must have 1-%d members", store.MaxPagingMembers)
	}
	checkIDs := func(path string, ids []int64) {
		seen := map[int64]bool{}
		for i, id := range ids {
			if id <= 0 || seen[id] {
				f.add(fmt.Sprintf("%s[%d]", path, i), "must be a distinct extension id")
			}
			seen[id] = true
		}
	}
	checkIDs("members", in.Members)
	if len(in.Callers) > 1000 {
		f.add("callers", "must have at most 1000 extensions")
	}
	checkIDs("callers", in.Callers)
	return f
}

func (s *server) listPagingGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Store.ListPagingGroups(r.Context())
	if err != nil {
		s.internal(w, "list paging groups", err)
		return
	}
	writeJSON(w, http.StatusOK, items(gs))
}

func (s *server) getPagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.Store.GetPagingGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) createPagingGroup(w http.ResponseWriter, r *http.Request) {
	var b pagingBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Code == nil || b.Members == nil {
		badRequest(w, "name, code and members are required")
		return
	}
	in := b.toInput()
	if f := validatePagingGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.CreatePagingGroup(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// putPagingGroup replaces a paging group as a whole.
func (s *server) putPagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b pagingBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Code == nil || b.Members == nil {
		badRequest(w, "name, code and members are required")
		return
	}
	in := b.toInput()
	if f := validatePagingGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.UpdatePagingGroup(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deletePagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeletePagingGroup(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "paging group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
