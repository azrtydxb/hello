package api

import (
	"encoding/json"
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
		Role        *string         `json:"role"`
		ExtensionID json.RawMessage `json:"extensionId"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role == nil && in.ExtensionID == nil {
		badRequest(w, "role or extensionId is required")
		return
	}
	var role auth.Role
	if in.Role != nil {
		var err error
		if role, err = auth.ParseRole(*in.Role); err != nil {
			badRequest(w, "role must be viewer, operator or admin")
			return
		}
	}
	// extensionId links the user to an extension (spec P-3); null unlinks.
	var ext *int64
	if in.ExtensionID != nil && string(in.ExtensionID) != "null" {
		ext = new(int64)
		if err := json.Unmarshal(in.ExtensionID, ext); err != nil || *ext <= 0 {
			badRequest(w, "extensionId must be an extension id or null")
			return
		}
	}
	var u store.User
	var err error
	if in.ExtensionID != nil {
		if u, err = s.Store.SetUserExtension(r.Context(), actor(r).String(), id, ext); err != nil {
			s.configError(w, "user", err)
			return
		}
	}
	if in.Role != nil {
		u, err = s.Store.SetUserRole(r.Context(), actor(r).String(), id, role)
		if errors.Is(err, store.ErrLastAdmin) {
			writeError(w, http.StatusConflict, "last_admin", "at least one user must remain admin")
			return
		}
		if err != nil {
			s.storeError(w, "user", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, u)
}
