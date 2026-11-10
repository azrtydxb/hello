package api

// Setting overrides (phone-feature-parity S-12): vendor keys set per phone
// group and per phone, on top of the template (template, then group, then
// phone). Secret values are write-only: they read back as the mask.

import (
	"context"
	"net/http"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// settingOverrideStore is the persistence of setting overrides;
// *store.Store implements it.
type settingOverrideStore interface {
	ListSettingOverrides(ctx context.Context, scope prov.OverrideScope, ownerID int64) ([]store.SettingOverride, error)
	ReplaceSettingOverrides(ctx context.Context, actor string, scope prov.OverrideScope, ownerID int64, in []store.SettingOverrideInput) ([]store.SettingOverride, error)
}

func (s *server) overrideStore(w http.ResponseWriter) (settingOverrideStore, bool) {
	if s.provDisabled(w) {
		return nil, false
	}
	os, ok := s.ProvStore.(settingOverrideStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "setting overrides are not available")
	}
	return os, ok
}

func (s *server) listSettings(scope prov.OverrideScope, what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		st, ok := s.overrideStore(w)
		if !ok {
			return
		}
		out, err := st.ListSettingOverrides(r.Context(), scope, id)
		if err != nil {
			s.provError(w, what, err)
			return
		}
		writeJSON(w, http.StatusOK, items(out))
	}
}

func (s *server) putSettings(scope prov.OverrideScope, what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		st, ok := s.overrideStore(w)
		if !ok {
			return
		}
		var in struct {
			Overrides []store.SettingOverrideInput `json:"overrides"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Overrides == nil {
			badRequest(w, "overrides is required (an empty list clears them)")
			return
		}
		out, err := st.ReplaceSettingOverrides(r.Context(), actor(r).String(), scope, id, in.Overrides)
		if err != nil {
			s.provError(w, what, err)
			return
		}
		writeJSON(w, http.StatusOK, items(out))
	}
}
