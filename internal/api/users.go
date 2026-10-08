package api

import (
	"errors"
	"net/http"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// Users and roles (spec S-23). Creating and deleting users stays with the
// hello-control CLI.

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.internal(w, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, list[store.User]{Items: us})
}

func (s *server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	role, err := auth.ParseRole(in.Role)
	if err != nil {
		badRequest(w, "role must be viewer, operator or admin")
		return
	}
	u, err := s.Store.SetUserRole(r.Context(), actor(r).String(), id, role)
	if errors.Is(err, store.ErrLastAdmin) {
		writeError(w, http.StatusConflict, "last_admin", "at least one user must remain admin")
		return
	}
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
