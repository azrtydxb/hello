package api

// Phone lines and account sharing (phone-feature-parity S-10, S-11): bind a
// further line (2 and up) of a phone to a device of an extension, and switch
// WP8x6 account sharing on for a phone group.

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// lineNumber reads the {line} path value: 2 and up, the lines a phone holds
// besides its first (which phone create and update bind).
func lineNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("line"))
	if err != nil || n < 2 || n > 6 {
		badRequest(w, "line must be 2 to 6: line 1 is bound with extensionId and deviceId of the phone")
		return 0, false
	}
	return n, true
}

// putPhoneLine binds a line of the phone to a device of an extension. With
// no deviceId a device is created for the extension; with one, that device is
// bound and its secret rotated.
func (s *server) putPhoneLine(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	line, ok := lineNumber(w, r)
	if !ok {
		return
	}
	var in struct {
		ExtensionID int64  `json:"extensionId"`
		DeviceID    *int64 `json:"deviceId"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	if in.ExtensionID <= 0 {
		f.add("extensionId", "is required")
	}
	if in.DeviceID != nil && *in.DeviceID <= 0 {
		f.add("deviceId", "must be a device id")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	b := store.Binding{ExtensionID: in.ExtensionID}
	if in.DeviceID != nil {
		b.DeviceID = *in.DeviceID
	}
	p, rotated, err := s.ProvStore.SetPhoneLine(r.Context(), actor(r).String(), id, line, b, s.SIPDomain)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, phoneView{Phone: p, SecretRotated: rotated})
}

// deletePhoneLine unbinds a line of the phone; the device keeps its
// credentials but the phone no longer receives them.
func (s *server) deletePhoneLine(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	line, ok := lineNumber(w, r)
	if !ok {
		return
	}
	p, err := s.ProvStore.ClearPhoneLine(r.Context(), actor(r).String(), id, line)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// putPhoneGroupAccountSharing switches WP8x6 account sharing on or off for
// the phones of a group.
func (s *server) putPhoneGroupAccountSharing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		var f fieldErrs
		f.add("enabled", "is required")
		writeFields(w, f)
		return
	}
	g, err := s.Phonebooks.SetPhoneGroupAccountSharing(r.Context(), actor(r).String(), id, *in.Enabled)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// postPhoneGroupSharingSecret replaces the group's account sharing password
// with a new generated one. The response is the group; the secret is never
// returned.
func (s *server) postPhoneGroupSharingSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	g, err := s.Phonebooks.RegeneratePhoneGroupSharingSecret(r.Context(), actor(r).String(), id)
	if err != nil {
		if errors.Is(err, prov.ErrSealed) {
			s.provError(w, "phone group", err)
			return
		}
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// validSharingRole adds the field error for a bad sharing role.
func validSharingRole(f *fieldErrs, role *string) {
	if role != nil && !prov.ValidSharingRole(*role) {
		f.add("sharingRole", "must be host or guest")
	}
}
