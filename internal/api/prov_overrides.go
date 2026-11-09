package api

// Setting overrides (phone-feature-parity S-12): vendor keys set per phone
// group and per phone, on top of the template (template, then group, then
// phone). Secret values are write-only: they read back as the mask.

import (
	"context"
	"net/http"

	"github.com/azrtydxb/hello/internal/prov"
	pbook "github.com/azrtydxb/hello/internal/prov/phonebook"
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

// phoneBooksStore is the part of the provisioning store that returns the
// books a phone is delivered; *store.Store implements it.
type phoneBooksStore interface {
	DeliveredBooks(ctx context.Context, phoneID int64) ([]pbook.Book, error)
}

// directoryView is how a phone's address book fits its model.
type directoryView struct {
	// Supported: the vendor has a directory format.
	Supported bool `json:"supported"`
	// Delivered is the number of contacts the phone's books hold after
	// duplicate numbers are dropped; Capacity what its model takes (0: no
	// limit); Dropped the contacts cut from the end of the delivery order.
	Delivered int `json:"delivered"`
	Capacity  int `json:"capacity"`
	Dropped   int `json:"dropped"`
	// VCard: the phone can be given its directory as a vCard file
	// (pb/0.vcf under its provisioning URL).
	VCard bool `json:"vcard"`
}

func (s *server) getPhoneDirectory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	bs, ok := s.ProvStore.(phoneBooksStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the directory is not available")
		return
	}
	ph, err := s.ProvStore.GetPhone(r.Context(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	books, err := bs.DeliveredBooks(r.Context(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	v, m := string(ph.Vendor), ph.Model
	_, supported := pbook.FormatFor(v)
	d, c, x := pbook.Capacity(v, m, books)
	writeJSON(w, http.StatusOK, directoryView{Supported: supported, Delivered: d, Capacity: c, Dropped: x, VCard: pbook.VCardModel(v, m)})
}
