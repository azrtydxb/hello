package api

// Phone onboarding queue (spec phone-onboarding): unadopted phones that asked
// the provisioning endpoint for a configuration. Operators list them, adopt
// them as phones (one, or several with consecutive extensions), dismiss them
// and keep an ignore list. Adopting is the manual phone creation of
// prov_handlers.go, so validation and arming behave the same.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// maxBulkAdopt bounds one bulk adoption.
const maxBulkAdopt = 100

func (s *server) listOnboarding(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	cs, err := s.ProvStore.ListCandidates(r.Context())
	if err != nil {
		s.internal(w, "list onboarding candidates", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

// pathMAC reads the {mac} of a path; 404 when it is not a MAC.
func pathMAC(w http.ResponseWriter, r *http.Request) (string, bool) {
	mac, err := prov.NormalizeMAC(r.PathValue("mac"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return "", false
	}
	return mac, true
}

func (s *server) dismissCandidate(w http.ResponseWriter, r *http.Request) {
	mac, ok := pathMAC(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	var in struct {
		Ignore bool `json:"ignore"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if err := s.ProvStore.DismissCandidate(r.Context(), actor(r).String(), mac, in.Ignore); err != nil {
		s.provError(w, "onboarding candidate", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listIgnoredMACs(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	ms, err := s.ProvStore.ListIgnored(r.Context())
	if err != nil {
		s.internal(w, "list ignored MACs", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ms))
}

func (s *server) unignoreMAC(w http.ResponseWriter, r *http.Request) {
	mac, ok := pathMAC(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	if err := s.ProvStore.Unignore(r.Context(), actor(r).String(), mac); err != nil {
		s.provError(w, "ignored MAC", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adoptRow is one candidate to adopt. The extension is named by id, by number
// or by the user linked to it; a bulk request may leave it out and give a
// start number instead.
type adoptRow struct {
	MAC             string       `json:"mac"`
	ExtensionID     int64        `json:"extensionId"`
	ExtensionNumber string       `json:"extensionNumber"`
	UserID          int64        `json:"userId"`
	Label           string       `json:"label"`
	Vendor          *prov.Vendor `json:"vendor"`
	Model           *string      `json:"model"`
}

func (a adoptRow) hasTarget() bool {
	return a.ExtensionID > 0 || a.ExtensionNumber != "" || a.UserID > 0
}

// adoptLookup is what resolving a row needs, read once per request.
type adoptLookup struct {
	cands     map[string]store.Candidate
	extByNum  map[string]int64
	extByID   map[int64]store.Extension
	userExtID map[int64]*int64
}

func (s *server) adoptLookup(r *http.Request) (adoptLookup, error) {
	ctx := r.Context()
	l := adoptLookup{
		cands: map[string]store.Candidate{}, extByNum: map[string]int64{},
		extByID: map[int64]store.Extension{}, userExtID: map[int64]*int64{},
	}
	cs, err := s.ProvStore.ListCandidates(ctx)
	if err != nil {
		return l, err
	}
	for _, c := range cs {
		l.cands[c.MAC] = c
	}
	exts, err := s.Store.ListExtensions(ctx)
	if err != nil {
		return l, err
	}
	for _, e := range exts {
		l.extByNum[e.Number] = e.ID
		l.extByID[e.ID] = e
	}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return l, err
	}
	for _, u := range users {
		l.userExtID[u.ID] = u.ExtensionID
	}
	return l, nil
}

// phoneInput validates one row and builds the phone to create from it and its
// candidate. The vendor and model default to what the phone reported. Errors
// are field errors with paths relative to prefix.
func (s *server) phoneInput(row adoptRow, l adoptLookup, prefix string) (store.PhoneInput, fieldErrs) {
	var f fieldErrs
	var in store.PhoneInput
	mac, err := prov.NormalizeMAC(row.MAC)
	if err != nil {
		f.add(prefix+"mac", "must be 12 hex digits, optionally separated by : - or .")
		return in, f
	}
	c, ok := l.cands[mac]
	if !ok {
		f.add(prefix+"mac", "is not in the onboarding queue")
		return in, f
	}
	vendor, model, label := prov.Vendor(c.Vendor), c.Model, row.Label
	if row.Vendor != nil {
		vendor = *row.Vendor
	}
	if row.Model != nil {
		model = *row.Model
	}
	if strings.TrimSpace(model) == "" {
		f.add(prefix+"model", "is required: the phone did not report its model")
	}
	extID, tf := resolveExtension(row, l, prefix)
	f = append(f, tf...)
	var vf fieldErrs
	validatePhoneFields(&vf, nil, &vendor, &model, &label, nil, nil)
	for _, e := range vf {
		f.add(prefix+e.Path, "%s", e.Message)
	}
	if len(f) > 0 {
		return in, f
	}
	return store.PhoneInput{MAC: mac, Vendor: vendor, Model: model, Label: label, ExtensionID: extID,
		Enabled: true, Realm: s.SIPDomain}, nil
}

// resolveExtension finds the extension a row names: exactly one of its id,
// its number, or the extension of a user.
func resolveExtension(row adoptRow, l adoptLookup, prefix string) (int64, fieldErrs) {
	var f fieldErrs
	n := 0
	for _, set := range []bool{row.ExtensionID > 0, row.ExtensionNumber != "", row.UserID > 0} {
		if set {
			n++
		}
	}
	switch {
	case n == 0:
		f.add(prefix+"extensionId", "is required: choose an extension or a user")
	case n > 1:
		f.add(prefix+"extensionId", "give one of extensionId, extensionNumber or userId")
	case row.ExtensionID > 0:
		if _, ok := l.extByID[row.ExtensionID]; !ok {
			f.add(prefix+"extensionId", "no such extension")
		} else {
			return row.ExtensionID, nil
		}
	case row.ExtensionNumber != "":
		if id, ok := l.extByNum[row.ExtensionNumber]; !ok {
			f.add(prefix+"extensionNumber", "no extension has number %s", strconv.Quote(truncateRunes(row.ExtensionNumber, 20)))
		} else {
			return id, nil
		}
	default:
		ext, ok := l.userExtID[row.UserID]
		switch {
		case !ok:
			f.add(prefix+"userId", "no such user")
		case ext == nil:
			f.add(prefix+"userId", "the user has no extension")
		default:
			return *ext, nil
		}
	}
	return 0, f
}

func (s *server) adoptCandidate(w http.ResponseWriter, r *http.Request) {
	mac, ok := pathMAC(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	var row adoptRow
	if !decode(w, r, &row) {
		return
	}
	if row.MAC != "" {
		badRequest(w, "mac belongs in the path")
		return
	}
	row.MAC = mac
	l, err := s.adoptLookup(r)
	if err != nil {
		s.internal(w, "adopt onboarding candidate: lookup", err)
		return
	}
	in, f := s.phoneInput(row, l, "")
	if len(f) > 0 {
		if _, queued := l.cands[mac]; !queued {
			writeError(w, http.StatusNotFound, "not_found", "onboarding candidate: not found")
			return
		}
		writeFields(w, f)
		return
	}
	created, err := s.ProvStore.AdoptCandidates(r.Context(), actor(r).String(), []store.PhoneInput{in})
	if err != nil {
		s.provError(w, "onboarding candidate", err)
		return
	}
	// The one-time provisioning URL is not returned: an adopted phone finds
	// its URL through the boot hand-off, which the new phone is armed for.
	writeJSON(w, http.StatusCreated, created[0].Phone)
}

type bulkAdoptBody struct {
	Items []adoptRow `json:"items"`
	// StartNumber gives the rows with no extension of their own consecutive
	// extension numbers from here, in list order ("2001", "2002", ...).
	StartNumber string `json:"startNumber"`
}

type bulkAdoptRow struct {
	Index     int      `json:"index"`
	MAC       string   `json:"mac"`
	Extension string   `json:"extension,omitempty"`
	Errors    []string `json:"errors"`
}

// nextNumbers returns n consecutive extension numbers from start, keeping
// start's width (leading zeros).
func nextNumbers(start string, n int) ([]string, bool) {
	v, err := strconv.ParseUint(start, 10, 64)
	if err != nil || !numberRe.MatchString(start) {
		return nil, false
	}
	out := make([]string, n)
	for i := range out {
		num := strconv.FormatUint(v+uint64(i), 10)
		if len(num) < len(start) {
			num = strings.Repeat("0", len(start)-len(num)) + num
		}
		if !numberRe.MatchString(num) {
			return nil, false
		}
		out[i] = num
	}
	return out, true
}

func (s *server) bulkAdopt(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	var body bulkAdoptBody
	if !decode(w, r, &body) {
		return
	}
	switch {
	case len(body.Items) == 0:
		badRequest(w, "items must list at least one candidate")
		return
	case len(body.Items) > maxBulkAdopt:
		badRequest(w, "at most "+strconv.Itoa(maxBulkAdopt)+" candidates per request")
		return
	}
	needStart := 0
	for _, it := range body.Items {
		if !it.hasTarget() {
			needStart++
		}
	}
	var numbers []string
	if needStart > 0 {
		if body.StartNumber == "" {
			badRequest(w, "startNumber is required when a row names no extension")
			return
		}
		var ok bool
		if numbers, ok = nextNumbers(body.StartNumber, needStart); !ok {
			badRequest(w, "startNumber must be an extension number and the range must stay within 10 digits")
			return
		}
	}
	l, err := s.adoptLookup(r)
	if err != nil {
		s.internal(w, "bulk adopt: lookup", err)
		return
	}
	rows := make([]bulkAdoptRow, len(body.Items))
	inputs := make([]store.PhoneInput, 0, len(body.Items))
	seen := map[string]bool{}
	ok := true
	for i, it := range body.Items {
		if !it.hasTarget() {
			it.ExtensionNumber, numbers = numbers[0], numbers[1:]
		}
		row := bulkAdoptRow{Index: i, MAC: it.MAC, Errors: []string{}}
		in, f := s.phoneInput(it, l, "")
		if len(f) == 0 {
			row.MAC = in.MAC
			if seen[in.MAC] {
				f.add("mac", "is listed twice")
			}
			seen[in.MAC] = true
		}
		if it.ExtensionNumber != "" {
			row.Extension = it.ExtensionNumber
		} else if e, found := l.extByID[in.ExtensionID]; found {
			row.Extension = e.Number
		}
		for _, e := range f {
			row.Errors = append(row.Errors, e.Path+" "+e.Message)
		}
		if len(f) > 0 {
			ok = false
		} else {
			inputs = append(inputs, in)
		}
		rows[i] = row
	}
	resp := map[string]any{"rows": rows, "ok": ok, "adopted": 0}
	if !ok {
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	created, err := s.ProvStore.AdoptCandidates(r.Context(), actor(r).String(), inputs)
	if err != nil {
		s.provError(w, "onboarding candidates", err)
		return
	}
	resp["adopted"] = len(created)
	writeJSON(w, http.StatusOK, resp)
}
