package api

// The phone LDAP directory's management routes (spec pbx-gaps S-24 to S-26):
// the group switch, the phone's directory state and the password rotation.

import (
	"net/http"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// groupLDAPView is GET /phone-groups/{id}/ldap.
type groupLDAPView struct {
	Enabled    bool   `json:"enabled"`
	Served     bool   `json:"served"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Base       string `json:"base"`
	PhoneCount int    `json:"phoneCount"`
}

func (s *server) groupLDAP(g groupLDAPSource) groupLDAPView {
	st := s.Prov.Settings
	v := groupLDAPView{Enabled: g.ldapEnabled, PhoneCount: g.phones, Served: st.LDAPServed()}
	if v.Served {
		v.Host, v.Port, v.Base = st.LDAPHost(), st.LDAPPort, "ou=phonebook,"+st.LDAPBase
	}
	return v
}

type groupLDAPSource struct {
	ldapEnabled bool
	phones      int
}

func (s *server) getPhoneGroupLDAP(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.booksDisabled(w) {
		return
	}
	g, err := s.Phonebooks.GetPhoneGroup(r.Context(), id)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, s.groupLDAP(groupLDAPSource{g.LDAPEnabled, g.PhoneCount}))
}

func (s *server) putPhoneGroupLDAP(w http.ResponseWriter, r *http.Request) {
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
		badRequest(w, "enabled is required")
		return
	}
	if *in.Enabled && !s.Prov.Settings.LDAPServed() {
		writeError(w, http.StatusConflict, "conflict", "the LDAP directory is not enabled on this server (HELLO_LDAP_ENABLED and HELLO_PROV_PUBLIC_URL)")
		return
	}
	g, err := s.Phonebooks.SetPhoneGroupLDAP(r.Context(), actor(r).String(), id, *in.Enabled)
	if err != nil {
		s.bookError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, s.groupLDAP(groupLDAPSource{g.LDAPEnabled, g.PhoneCount}))
}

func (s *server) getPhoneLDAP(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	l, err := s.ProvStore.GetPhoneLDAP(r.Context(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// rotatePhoneLDAPPassword generates a new LDAP password for the phone and
// shows it once. The phone is queued a check-sync so it picks the new
// password up; the old one keeps working until it has.
func (s *server) rotatePhoneLDAPPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	pw, err := s.ProvStore.RotateLDAPPassword(r.Context(), actor(r).String(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	resync := false
	if _, err := s.ProvStore.QueuePush(r.Context(), actor(r).String(), id, store.PushResync); err == nil {
		resync = true
	}
	st := s.Prov.Settings
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"bindDn": prov.LDAPBindDN(st.LDAPBase, id), "password": pw, "resyncQueued": resync,
	})
}
