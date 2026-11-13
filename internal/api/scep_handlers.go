package api

// SCEP and 802.1X (migration 00076, spec phone-feature-parity S-25): the
// Hello device CA, the certificates it issued and their revocation, each
// phone's one-time SCEP challenge, the reusable 802.1X profiles and each
// phone group's SCEP switch and wired profile. Viewers read; changes are
// admin only (spec Interfaces, RBAC). Nothing here returns a key, a
// challenge or a profile password.

import (
	"context"
	"crypto/rsa"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// SCEPStore is the SCEP and 802.1X persistence; *store.Store implements it.
type SCEPStore interface {
	GetSCEPCA(ctx context.Context) (*store.SCEPCAView, error)
	SetSCEPCA(ctx context.Context, actor, certPEM string, key *rsa.PrivateKey, source string, replace bool) (*store.SCEPCAView, error)
	ListSCEPCertificates(ctx context.Context, f store.SCEPCertFilter) ([]store.SCEPCertificate, error)
	RevokeSCEPCertificate(ctx context.Context, actor string, id int64, reason string) (store.SCEPCertificate, error)
	RotateSCEPChallenge(ctx context.Context, actor string, phoneID int64) error
	ListDot1XProfiles(ctx context.Context) ([]store.Dot1XProfile, error)
	GetDot1XProfile(ctx context.Context, id int64) (store.Dot1XProfile, error)
	CreateDot1XProfile(ctx context.Context, actor string, in store.Dot1XInput) (store.Dot1XProfile, error)
	UpdateDot1XProfile(ctx context.Context, actor string, id int64, in store.Dot1XInput) (store.Dot1XProfile, error)
	DeleteDot1XProfile(ctx context.Context, actor string, id int64) error
	GetGroupSecurity(ctx context.Context, groupID int64) (store.GroupSecurity, error)
	SetGroupSecurity(ctx context.Context, actor string, in store.GroupSecurity) (store.GroupSecurity, error)
}

var _ SCEPStore = (*store.Store)(nil)

// scepStore is the SCEP persistence: the provisioning store when it has it,
// nil (503) while provisioning is off.
func (s *server) scepStore(w http.ResponseWriter) (SCEPStore, bool) {
	if s.provDisabled(w) {
		return nil, false
	}
	ss, ok := s.ProvStore.(SCEPStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "SCEP is not available")
		return nil, false
	}
	return ss, true
}

// scepView is GET /api/v1/scep.
type scepView struct {
	ServerEnabled bool              `json:"serverEnabled"`
	CA            *store.SCEPCAView `json:"ca"`
	CAURL         string            `json:"caUrl"`
	CRLURL        string            `json:"crlUrl"`
}

func (s *server) getSCEP(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	ca, err := ss.GetSCEPCA(r.Context())
	if err != nil {
		s.provError(w, "SCEP CA", err)
		return
	}
	v := scepView{ServerEnabled: s.Prov.Settings.SCEP, CA: ca}
	if base := strings.TrimSuffix(s.Prov.Settings.CAURL(), "/p/ca.crt"); base != "" {
		v.CAURL, v.CRLURL = base+"/p/scep-ca.crt", base+"/p/scep-ca.crl"
	}
	writeJSON(w, http.StatusOK, v)
}

type scepCABody struct {
	Mode          string `json:"mode"`
	CommonName    string `json:"commonName"`
	ValidityYears int    `json:"validityYears"`
	CertPEM       string `json:"certPem"`
	KeyPEM        string `json:"keyPem"`
	Replace       bool   `json:"replace"`
}

// putSCEPCA generates or imports the device CA.
func (s *server) putSCEPCA(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	var b scepCABody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	var (
		certPEM string
		key     *rsa.PrivateKey
		source  string
	)
	switch b.Mode {
	case "generate":
		if b.CertPEM != "" || b.KeyPEM != "" {
			f.add("mode", "generate takes no certPem or keyPem")
		}
		if n := len([]rune(b.CommonName)); n > 64 || strings.ContainsAny(b.CommonName, "\r\n") {
			f.add("commonName", "must be at most 64 characters on one line")
		}
		if b.ValidityYears < 0 || b.ValidityYears > 30 {
			f.add("validityYears", "must be 1 to 30 (0 is the default 10)")
		}
		if len(f) > 0 {
			writeFields(w, f)
			return
		}
		var err error
		if certPEM, key, err = prov.GenerateSCEPCA(b.CommonName, b.ValidityYears, time.Now()); err != nil {
			s.internal(w, "generate SCEP CA", err)
			return
		}
		source = "generated"
	case "import":
		if b.CommonName != "" || b.ValidityYears != 0 {
			f.add("mode", "import takes no commonName or validityYears")
			writeFields(w, f)
			return
		}
		_, k, err := prov.ParseSCEPCA(b.CertPEM, b.KeyPEM, time.Now())
		if err != nil {
			path := "certPem"
			if strings.HasPrefix(err.Error(), "keyPem") {
				path = "keyPem"
			}
			f.add(path, "%s", err.Error())
			writeFields(w, f)
			return
		}
		certPEM, key, source = b.CertPEM, k, "imported"
	default:
		f.add("mode", "must be generate or import")
		writeFields(w, f)
		return
	}
	ca, err := ss.SetSCEPCA(r.Context(), actor(r).String(), certPEM, key, source, b.Replace)
	if err != nil {
		s.provError(w, "SCEP CA", err)
		return
	}
	writeJSON(w, http.StatusOK, ca)
}

func (s *server) listSCEPCertificates(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	var flt store.SCEPCertFilter
	if v := q.Get("phoneId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			badRequest(w, "phoneId must be a phone id")
			return
		}
		flt.PhoneID = &id
	}
	switch flt.Status = q.Get("status"); flt.Status {
	case "", "active", "expired", "revoked":
	default:
		badRequest(w, "status must be active, expired or revoked")
		return
	}
	certs, err := ss.ListSCEPCertificates(r.Context(), flt)
	if err != nil {
		s.provError(w, "SCEP certificates", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": certs})
}

func (s *server) revokeSCEPCertificate(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Reason == "" {
		b.Reason = "unspecified"
	}
	if !slices.Contains(store.RevokeReasons, b.Reason) {
		var f fieldErrs
		f.add("reason", "must be unspecified, key_compromise, superseded or cessation_of_operation")
		writeFields(w, f)
		return
	}
	c, err := ss.RevokeSCEPCertificate(r.Context(), actor(r).String(), id, b.Reason)
	if err != nil {
		s.provError(w, "certificate", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) rotatePhoneSCEPChallenge(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := ss.RotateSCEPChallenge(r.Context(), actor(r).String(), id); err != nil {
		s.provError(w, "phone", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dot1xBody is a profile create or replace. A missing password keeps the
// stored one; "" removes it.
type dot1xBody struct {
	Name              *string `json:"name"`
	Method            *string `json:"method"`
	Identity          *string `json:"identity"`
	AnonymousIdentity string  `json:"anonymousIdentity"`
	Password          *string `json:"password"`
	CACertPEM         string  `json:"caCertPem"`
	ServerDomain      string  `json:"serverDomain"`
}

func dot1xInput(w http.ResponseWriter, r *http.Request) (store.Dot1XInput, bool) {
	var b dot1xBody
	if !decode(w, r, &b) {
		return store.Dot1XInput{}, false
	}
	var f fieldErrs
	for name, v := range map[string]*string{"name": b.Name, "method": b.Method, "identity": b.Identity} {
		if v == nil {
			f.add(name, "is required")
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return store.Dot1XInput{}, false
	}
	return store.Dot1XInput{Name: *b.Name, Method: *b.Method, Identity: *b.Identity, AnonymousIdentity: b.AnonymousIdentity,
		Password: b.Password, CACertPEM: b.CACertPEM, ServerDomain: b.ServerDomain}, true
}

func (s *server) listDot1XProfiles(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	ps, err := ss.ListDot1XProfiles(r.Context())
	if err != nil {
		s.provError(w, "802.1X profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (s *server) getDot1XProfile(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := ss.GetDot1XProfile(r.Context(), id)
	if err != nil {
		s.provError(w, "802.1X profile", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) createDot1XProfile(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	in, ok := dot1xInput(w, r)
	if !ok {
		return
	}
	p, err := ss.CreateDot1XProfile(r.Context(), actor(r).String(), in)
	if err != nil {
		s.provError(w, "802.1X profile", err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) updateDot1XProfile(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in, ok := dot1xInput(w, r)
	if !ok {
		return
	}
	p, err := ss.UpdateDot1XProfile(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.provError(w, "802.1X profile", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deleteDot1XProfile(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := ss.DeleteDot1XProfile(r.Context(), actor(r).String(), id); err != nil {
		s.provError(w, "802.1X profile", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getPhoneGroupSecurity(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := ss.GetGroupSecurity(r.Context(), id)
	if err != nil {
		s.provError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) putPhoneGroupSecurity(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.scepStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b struct {
		SCEPEnabled      *bool  `json:"scepEnabled"`
		CertValidityDays *int   `json:"certValidityDays"`
		WiredDot1XID     *int64 `json:"wiredDot1xProfileId"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.SCEPEnabled == nil {
		badRequest(w, "scepEnabled is required")
		return
	}
	in := store.GroupSecurity{GroupID: id, SCEPEnabled: *b.SCEPEnabled, CertValidityDays: prov.SCEPDefaultValidityDays, WiredDot1XID: b.WiredDot1XID}
	if b.CertValidityDays != nil {
		in.CertValidityDays = *b.CertValidityDays
	}
	g, err := ss.SetGroupSecurity(r.Context(), actor(r).String(), in)
	if err != nil {
		s.provError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
