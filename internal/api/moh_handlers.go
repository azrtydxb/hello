package api

import (
	"net/http"

	"github.com/azrtydxb/hello/internal/store"
)

// Music on hold classes (phone-feature-parity S-33). A class is a named
// playlist of announcements; the global default, the per-extension,
// per-group and per-route picks refer to it by id. Reads are open to a
// viewer, changes need an operator like announcements.

type mohBody struct {
	Name            *string  `json:"name"`
	Mode            *string  `json:"mode"`
	IsDefault       *bool    `json:"isDefault"`
	AnnouncementIDs *[]int64 `json:"announcementIds"`
}

func (b mohBody) toInput() store.MOHClassInput {
	in := store.MOHClassInput{Mode: store.MOHModeLoop, AnnouncementIDs: []int64{}}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Mode != nil {
		in.Mode = *b.Mode
	}
	if b.IsDefault != nil {
		in.IsDefault = *b.IsDefault
	}
	if b.AnnouncementIDs != nil {
		in.AnnouncementIDs = *b.AnnouncementIDs
	}
	return in
}

func (s *server) listMOHClasses(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListMOHClasses(r.Context())
	if err != nil {
		s.internal(w, "list music on hold classes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

func (s *server) getMOHClass(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.GetMOHClass(r.Context(), id)
	if err != nil {
		s.configError(w, "music on hold class", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) createMOHClass(w http.ResponseWriter, r *http.Request) {
	var b mohBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	c, err := s.Store.CreateMOHClass(r.Context(), actor(r).String(), b.toInput())
	if err != nil {
		s.configError(w, "music on hold class", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// putMOHClass replaces a class as a whole.
func (s *server) putMOHClass(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b mohBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	c, err := s.Store.UpdateMOHClass(r.Context(), actor(r).String(), id, b.toInput())
	if err != nil {
		s.configError(w, "music on hold class", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteMOHClass(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteMOHClass(r.Context(), actor(r).String(), id); err != nil {
		s.configError(w, "music on hold class", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
