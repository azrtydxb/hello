package api

import (
	"net/http"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// Wi-Fi profiles and roaming (migration 00074, spec phone-feature-parity S-23
// and S-24). A profile's pre-shared key and 802.1X password are write-only:
// the API shows pskSet and passwordSet, never the secret. Reads are open to a
// viewer; changes need the admin scope, as the phone groups they apply to do.

type wifiProfileBody struct {
	Name              *string `json:"name"`
	SSID              *string `json:"ssid"`
	Security          *string `json:"security"`
	PSK               *string `json:"psk"`
	EAPMethod         *string `json:"eapMethod"`
	Identity          *string `json:"identity"`
	AnonymousIdentity *string `json:"anonymousIdentity"`
	Password          *string `json:"password"`
	CACert            *string `json:"caCert"`
	Priority          *int    `json:"priority"`
	Hidden            *bool   `json:"hidden"`
	PreferAccount     *int    `json:"preferAccount"`
	Verified          *bool   `json:"verified"`
}

// input merges the body over the stored profile (base) and validates the
// result. A secret the new security mode does not use is cleared.
func (b wifiProfileBody) input(base store.WifiProfile) (store.WifiProfileInput, fieldErrs) {
	in := store.WifiProfileInput{
		Name: base.Name, SSID: base.SSID, Security: base.Security, EAPMethod: base.EAPMethod, Identity: base.Identity,
		AnonymousIdentity: base.AnonymousIdentity, CACert: base.CACert, Priority: base.Priority, Hidden: base.Hidden,
		PreferAccount: base.PreferAccount, Verified: base.Verified, PSK: b.PSK, Password: b.Password,
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&in.Name, b.Name)
	set(&in.SSID, b.SSID)
	set(&in.Security, b.Security)
	set(&in.EAPMethod, b.EAPMethod)
	set(&in.Identity, b.Identity)
	set(&in.AnonymousIdentity, b.AnonymousIdentity)
	set(&in.CACert, b.CACert)
	if b.Priority != nil {
		in.Priority = *b.Priority
	}
	if b.Hidden != nil {
		in.Hidden = *b.Hidden
	}
	if b.PreferAccount != nil {
		in.PreferAccount = *b.PreferAccount
	}
	if b.Verified != nil {
		in.Verified = *b.Verified
	}
	var f fieldErrs
	empty := ""
	if prov.WifiEnterprise(in.Security) {
		if in.EAPMethod == "" {
			in.EAPMethod = prov.EAPPEAP
		}
		if in.CACert == "" {
			in.CACert = prov.CAServerSystem
		}
	} else {
		if b.Password != nil && *b.Password != "" {
			f.add("password", "is used by an enterprise profile only")
		}
		in.EAPMethod, in.Identity, in.AnonymousIdentity, in.Password = "", "", "", &empty
		if in.CACert == "" || b.CACert == nil {
			in.CACert = prov.CAServerSystem
		}
	}
	if !prov.WifiUsesPSK(in.Security) {
		if b.PSK != nil && *b.PSK != "" {
			f.add("psk", "is used by a wpa2-psk or wpa3-sae profile only")
		}
		in.PSK = &empty
	}
	hasPSK := (in.PSK != nil && *in.PSK != "") || (in.PSK == nil && base.PSKSet)
	hasPass := (in.Password != nil && *in.Password != "") || (in.Password == nil && base.PasswordSet)
	p := prov.WifiProfile{Name: in.Name, SSID: in.SSID, Security: in.Security, EAPMethod: in.EAPMethod, Identity: in.Identity,
		AnonymousIdentity: in.AnonymousIdentity, CACert: in.CACert, Priority: in.Priority, PreferAccount: in.PreferAccount}
	if in.PSK != nil {
		p.PSK = *in.PSK
	}
	for _, e := range prov.CheckWifiProfile(p, hasPSK, hasPass) {
		f.add(e.Path, "%s", e.Message)
	}
	return in, f
}

func (s *server) listWifiProfiles(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListWifiProfiles(r.Context())
	if err != nil {
		s.internal(w, "list Wi-Fi profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getWifiProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	v, err := s.Store.GetWifiProfile(r.Context(), id)
	if err != nil {
		s.configError(w, "Wi-Fi profile", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createWifiProfile(w http.ResponseWriter, r *http.Request) {
	var b wifiProfileBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	for _, c := range []struct {
		name string
		set  bool
	}{{"name", b.Name != nil}, {"ssid", b.SSID != nil}, {"security", b.Security != nil}} {
		if !c.set {
			f.add(c.name, "is required")
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	in, f := b.input(store.WifiProfile{CACert: prov.CAServerSystem})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.CreateWifiProfile(r.Context(), actor(r).String(), in)
	if err != nil {
		s.configError(w, "Wi-Fi profile", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) updateWifiProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var b wifiProfileBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := s.Store.GetWifiProfile(r.Context(), id)
	if err != nil {
		s.configError(w, "Wi-Fi profile", err)
		return
	}
	in, f := b.input(cur)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.UpdateWifiProfile(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.configError(w, "Wi-Fi profile", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteWifiProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if err := s.Store.DeleteWifiProfile(r.Context(), actor(r).String(), id); err != nil {
		s.configError(w, "Wi-Fi profile", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getPhoneGroupWifi(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	v, err := s.Store.GetWifiGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type wifiGroupBody struct {
	ProfileIDs *[]int64        `json:"profileIds"`
	Radio      *prov.WifiRadio `json:"radio"`
}

// putPhoneGroupWifi replaces the profiles and the roaming settings of a phone
// group; an absent radio removes the settings.
func (s *server) putPhoneGroupWifi(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var b wifiGroupBody
	if !decode(w, r, &b) {
		return
	}
	if b.ProfileIDs == nil {
		badRequest(w, "profileIds is required (an empty list removes every profile)")
		return
	}
	var radio prov.WifiRadio
	if b.Radio != nil {
		radio = *b.Radio
		var f fieldErrs
		for _, e := range prov.CheckWifiRadio(radio) {
			f.add(e.Path, "%s", e.Message)
		}
		if len(f) > 0 {
			writeFields(w, f)
			return
		}
	}
	v, err := s.Store.PutWifiGroup(r.Context(), actor(r).String(), id, *b.ProfileIDs, radio)
	if err != nil {
		s.configError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) getPhoneWifi(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	v, err := s.Store.GetWifiPhone(r.Context(), id)
	if err != nil {
		s.configError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putPhoneWifi replaces the profiles assigned to one phone. They come after
// its group's, and one with the same SSID replaces the group's.
func (s *server) putPhoneWifi(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		ProfileIDs *[]int64 `json:"profileIds"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.ProfileIDs == nil {
		badRequest(w, "profileIds is required (an empty list removes every profile)")
		return
	}
	v, err := s.Store.PutWifiPhone(r.Context(), actor(r).String(), id, *b.ProfileIDs)
	if err != nil {
		s.configError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
