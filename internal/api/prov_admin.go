package api

// Provisioning templates, firmware, redirect accounts, settings and the
// CSV phone import (plan contract 7).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
	"github.com/azrtydxb/hello/internal/store"
)

// Templates.

// templateView is a template as the API returns it; a built-in has id 0
// and is addressed by its builtinRef.
type templateView struct {
	ID         int64               `json:"id"`
	Vendor     prov.Vendor         `json:"vendor"`
	ModelGlob  string              `json:"modelGlob"`
	Priority   int                 `json:"priority"`
	Name       string              `json:"name"`
	Files      []prov.TemplateFile `json:"files"`
	Builtin    bool                `json:"builtin"`
	BuiltinRef string              `json:"builtinRef"`
	Version    int                 `json:"version"`
	UpdatedAt  *time.Time          `json:"updatedAt"`
}

func viewTemplate(t prov.Template, updated *time.Time) templateView {
	files := t.Files
	if files == nil {
		files = []prov.TemplateFile{}
	}
	return templateView{ID: t.ID, Vendor: t.Vendor, ModelGlob: t.ModelGlob, Priority: t.Priority, Name: t.Name,
		Files: files, Builtin: t.Builtin(), BuiltinRef: t.BuiltinRef, Version: t.Version, UpdatedAt: updated}
}

func storedView(t store.ProvTemplate) templateView {
	u := t.UpdatedAt
	return viewTemplate(t.Template, &u)
}

func (s *server) listTemplates(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	ts, err := s.ProvStore.ListProvTemplates(r.Context())
	if err != nil {
		s.internal(w, "list templates", err)
		return
	}
	out := []templateView{}
	for _, b := range prov.Builtins() {
		out = append(out, viewTemplate(b, nil))
	}
	for _, t := range ts {
		out = append(out, storedView(t))
	}
	writeJSON(w, http.StatusOK, items(out))
}

// template finds {id}: a stored template's id, or a built-in's name.
func (s *server) template(w http.ResponseWriter, r *http.Request) (prov.Template, *time.Time, bool) {
	ref := r.PathValue("id")
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		t, err := s.ProvStore.GetProvTemplate(r.Context(), id)
		if err != nil {
			s.provError(w, "template", err)
			return prov.Template{}, nil, false
		}
		u := t.UpdatedAt
		return t.Template, &u, true
	}
	for _, b := range prov.Builtins() {
		if b.BuiltinRef == ref {
			return b, nil, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "template: not found")
	return prov.Template{}, nil, false
}

func (s *server) getTemplate(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	if t, u, ok := s.template(w, r); ok {
		writeJSON(w, http.StatusOK, viewTemplate(t, u))
	}
}

type templateBody struct {
	Vendor    *prov.Vendor         `json:"vendor"`
	ModelGlob *string              `json:"modelGlob"`
	Priority  *int                 `json:"priority"`
	Name      *string              `json:"name"`
	Files     *[]prov.TemplateFile `json:"files"`
}

func (b templateBody) applyTo(t *prov.Template) {
	setIf(&t.Vendor, b.Vendor)
	setIf(&t.ModelGlob, b.ModelGlob)
	setIf(&t.Priority, b.Priority)
	setIf(&t.Name, b.Name)
	setIf(&t.Files, b.Files)
}

var globRe = regexp.MustCompile(`^[A-Za-z0-9 ._+()/*?\[\]-]{1,64}$`)

// validateTemplate checks the template's own fields, then what prov.Validate
// checks (parse, variables, the sample render's time and size).
func validateTemplate(t prov.Template) []prov.FieldError {
	var out []prov.FieldError
	if !t.Vendor.Valid() {
		out = append(out, prov.FieldError{Path: "vendor", Message: "must be one of yealink, poly, grandstream, snom, fanvil, generic"})
	}
	if !globRe.MatchString(t.ModelGlob) {
		out = append(out, prov.FieldError{Path: "modelGlob", Message: "must be 1-64 characters of a model name or * ? [ ]"})
	}
	if t.Priority < -1000 || t.Priority > 1000 {
		out = append(out, prov.FieldError{Path: "priority", Message: "must be -1000 to 1000"})
	}
	if n := strings.TrimSpace(t.Name); n == "" || !printable(n, 128, true) {
		out = append(out, prov.FieldError{Path: "name", Message: "must be 1-128 printable characters"})
	}
	return append(out, prov.Validate(t)...)
}

func (s *server) createTemplate(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	var b templateBody
	if !decode(w, r, &b) {
		return
	}
	var t prov.Template
	b.applyTo(&t)
	if f := validateTemplate(t); len(f) > 0 {
		writeProvFields(w, f)
		return
	}
	st, err := s.ProvStore.CreateProvTemplate(r.Context(), actor(r).String(), t)
	if err != nil {
		s.provError(w, "template", err)
		return
	}
	writeJSON(w, http.StatusCreated, storedView(st))
}

func (s *server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.storedTemplateID(w, r)
	if !ok {
		return
	}
	var b templateBody
	if !decode(w, r, &b) {
		return
	}
	if b == (templateBody{}) {
		badRequest(w, "a field to change is required")
		return
	}
	st, err := s.ProvStore.UpdateProvTemplate(r.Context(), actor(r).String(), id, func(t *prov.Template) []prov.FieldError {
		b.applyTo(t)
		return validateTemplate(*t)
	})
	if err != nil {
		s.provError(w, "template", err)
		return
	}
	writeJSON(w, http.StatusOK, storedView(st))
}

// storedTemplateID parses {id} of a change; a built-in is read-only.
func (s *server) storedTemplateID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if s.provDisabled(w) {
		return 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		for _, b := range prov.Builtins() {
			if b.BuiltinRef == r.PathValue("id") {
				writeError(w, http.StatusConflict, "conflict", "built-in templates are read-only; copy it to edit")
				return 0, false
			}
		}
		writeError(w, http.StatusNotFound, "not_found", "template: not found")
		return 0, false
	}
	return id, true
}

func (s *server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.storedTemplateID(w, r)
	if !ok {
		return
	}
	if err := s.ProvStore.DeleteProvTemplate(r.Context(), actor(r).String(), id); err != nil {
		s.provError(w, "template", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// copyTemplate makes an editable copy one priority above its source, so it
// wins resolution; deleting the copy restores the source (spec S-8).
func (s *server) copyTemplate(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	src, _, ok := s.template(w, r)
	if !ok {
		return
	}
	t := src
	t.ID, t.Version, t.Priority = 0, 0, src.Priority+1
	t.Name = truncateRunes(src.Name+" (copy)", 128)
	if !src.Builtin() {
		t.BuiltinRef = src.BuiltinRef
	}
	st, err := s.ProvStore.CreateProvTemplate(r.Context(), actor(r).String(), t)
	if err != nil {
		s.provError(w, "template", err)
		return
	}
	writeJSON(w, http.StatusCreated, storedView(st))
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *server) validateTemplateRoute(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	var b templateBody
	if !decode(w, r, &b) {
		return
	}
	var t prov.Template
	b.applyTo(&t)
	if f := validateTemplate(t); len(f) > 0 {
		writeProvFields(w, f)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Firmware.

// FirmwareObjects is the firmware file store (bucket hello-firmware).
type FirmwareObjects interface {
	// PutFirmware uploads r, of unknown size, under key.
	PutFirmware(ctx context.Context, key string, r io.Reader) error
	// MoveFirmware renames an object (a server-side copy, then a delete).
	MoveFirmware(ctx context.Context, from, to string) error
	// RemoveFirmware deletes an object; an absent one is not an error.
	RemoveFirmware(ctx context.Context, key string) error
}

// maxFirmwareBytes is the largest firmware file (spec S-13).
const maxFirmwareBytes = 512 << 20

var firmwareNameRe = regexp.MustCompile(`^[A-Za-z0-9_+-][A-Za-z0-9._+-]{0,127}$`)

type firmwareView struct {
	ID         int64       `json:"id"`
	Vendor     prov.Vendor `json:"vendor"`
	ModelGlob  string      `json:"modelGlob"`
	Version    string      `json:"version"`
	Filename   string      `json:"filename"`
	Size       int64       `json:"size"`
	SHA256     string      `json:"sha256"`
	UploadedAt time.Time   `json:"uploadedAt"`
	Pinned     bool        `json:"pinned"`
}

func viewFirmware(f store.FirmwareFile) firmwareView {
	return firmwareView{ID: f.ID, Vendor: f.Vendor, ModelGlob: f.ModelGlob, Version: f.Version, Filename: f.Filename,
		Size: f.Size, SHA256: f.SHA256, UploadedAt: f.UploadedAt, Pinned: f.Pinned}
}

func (s *server) listFirmware(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	fs, err := s.ProvStore.ListFirmware(r.Context())
	if err != nil {
		s.internal(w, "list firmware", err)
		return
	}
	out := make([]firmwareView, len(fs))
	for i, f := range fs {
		out[i] = viewFirmware(f)
	}
	writeJSON(w, http.StatusOK, items(out))
}

// uploadFirmware streams a multipart upload to MinIO, hashing it on the
// way: the fields vendor, modelGlob and version come first, then the file
// part. The object lands under a temporary key and is moved to
// <vendor>/<sha256>/<filename> once the hash is known.
func (s *server) uploadFirmware(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	if s.Prov.Firmware == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFirmwareBytes+maxBody)
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "a multipart/form-data body is required")
		return
	}
	var (
		f      prov.Firmware
		fields = map[string]string{}
		tmp    string
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			badRequest(w, "invalid multipart body")
			return
		}
		if part.FormName() != "file" {
			b, err := io.ReadAll(io.LimitReader(part, 257))
			if err != nil || len(b) > 256 {
				badRequest(w, "field "+strconv.Quote(part.FormName())+" is too long")
				return
			}
			fields[part.FormName()] = strings.TrimSpace(string(b))
			continue
		}
		if tmp != "" {
			badRequest(w, "exactly one file is allowed")
			return
		}
		f.Vendor, f.ModelGlob, f.Version = prov.Vendor(fields["vendor"]), fields["modelGlob"], fields["version"]
		f.Filename = fields["filename"]
		if f.Filename == "" {
			f.Filename = part.FileName()
		}
		if fe := validateFirmware(f); len(fe) > 0 {
			writeFields(w, fe)
			return
		}
		// A taken name is refused before anything is uploaded: the same
		// file uploaded again has the same object key, and storing it
		// would overwrite (and its failure remove) the live object.
		switch existing, err := s.ProvStore.FirmwareByName(r.Context(), f.Vendor, f.Filename); {
		case err == nil:
			writeError(w, http.StatusConflict, "conflict",
				fmt.Sprintf("%s firmware %s already exists (id %d); delete it first", f.Vendor, f.Filename, existing.ID))
			return
		case !errors.Is(err, prov.ErrNotFound):
			s.internal(w, "firmware: look up name", err)
			return
		}
		tmp, err = s.streamFirmware(r.Context(), part, &f)
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) || errors.Is(err, errFirmwareTooBig) {
				badRequest(w, "the firmware file is larger than 512 MiB")
				return
			}
			s.Log.Error("firmware: upload", "error", err)
			writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
			return
		}
	}
	if tmp == "" {
		badRequest(w, "a file part is required")
		return
	}
	f.ObjectKey = string(f.Vendor) + "/" + f.SHA256 + "/" + f.Filename
	if err := s.Prov.Firmware.MoveFirmware(r.Context(), tmp, f.ObjectKey); err != nil {
		s.removeFirmware(tmp)
		s.Log.Error("firmware: store", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	created, err := s.ProvStore.CreateFirmware(r.Context(), actor(r).String(), f)
	if err != nil {
		// A concurrent upload of the same name won: its row names this
		// very key, so the object stays.
		var taken *store.InUseError
		if !errors.As(err, &taken) {
			s.removeFirmware(f.ObjectKey)
		}
		s.provError(w, "firmware", err)
		return
	}
	writeJSON(w, http.StatusCreated, viewFirmware(created))
}

var errFirmwareTooBig = errors.New("firmware too big")

// streamFirmware uploads part under a temporary key, filling in f's size
// and SHA-256, and returns the key. A failed upload removes what it wrote.
func (s *server) streamFirmware(ctx context.Context, part *multipart.Part, f *prov.Firmware) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	tmp := "upload/" + hex.EncodeToString(nonce[:])
	h := sha256.New()
	c := &countingReader{r: io.LimitReader(part, maxFirmwareBytes+1)}
	if err := s.Prov.Firmware.PutFirmware(ctx, tmp, io.TeeReader(c, h)); err != nil {
		s.removeFirmware(tmp)
		return "", err
	}
	if c.n > maxFirmwareBytes {
		s.removeFirmware(tmp)
		return "", errFirmwareTooBig
	}
	f.Size, f.SHA256 = c.n, hex.EncodeToString(h.Sum(nil))
	return tmp, nil
}

func (s *server) removeFirmware(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Prov.Firmware.RemoveFirmware(ctx, key); err != nil {
		s.Log.Warn("firmware: remove object", "key", key, "error", err)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func validateFirmware(f prov.Firmware) fieldErrs {
	var fe fieldErrs
	if !f.Vendor.Valid() {
		fe.add("vendor", "must be one of yealink, poly, grandstream, snom, fanvil, generic")
	}
	if !globRe.MatchString(f.ModelGlob) {
		fe.add("modelGlob", "must be 1-64 characters of a model name or * ? [ ]")
	}
	if f.Version == "" || !printable(f.Version, 64, false) {
		fe.add("version", "must be 1-64 printable characters without spaces")
	}
	if !firmwareNameRe.MatchString(f.Filename) || strings.Contains(f.Filename, "..") {
		fe.add("filename", "must be a plain file name of A-Z a-z 0-9 . _ + -")
	}
	return fe
}

func (s *server) deleteFirmware(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	key, err := s.ProvStore.DeleteFirmware(r.Context(), actor(r).String(), id)
	if err != nil {
		s.provError(w, "firmware", err)
		return
	}
	// The row is gone first, so no rendered URL names the object any more;
	// a removal failure leaves an orphan object, which is harmless.
	if s.Prov.Firmware != nil {
		s.removeFirmware(key)
	}
	w.WriteHeader(http.StatusNoContent)
}

// putFirmwarePins replaces every pin with the body's list.
func (s *server) putFirmwarePins(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	var pins []store.FirmwarePin
	if !decode(w, r, &pins) {
		return
	}
	var fe fieldErrs
	for i, p := range pins {
		if !p.Vendor.Valid() {
			fe.add(fmt.Sprintf("[%d].vendor", i), "must be a vendor")
		}
		if !globRe.MatchString(p.ModelGlob) {
			fe.add(fmt.Sprintf("[%d].modelGlob", i), "must be 1-64 characters of a model name or * ? [ ]")
		}
		if p.FirmwareID <= 0 {
			fe.add(fmt.Sprintf("[%d].firmwareId", i), "is required")
		}
	}
	if len(fe) > 0 {
		writeFields(w, fe)
		return
	}
	out, err := s.ProvStore.PutFirmwarePins(r.Context(), actor(r).String(), pins)
	if err != nil {
		s.provError(w, "firmware pins", err)
		return
	}
	writeJSON(w, http.StatusOK, items(out))
}

// Redirect accounts.

// redirectVendors are the vendors with a redirect service (spec S-11).
var redirectVendors = []prov.Vendor{prov.Yealink, prov.Poly, prov.Grandstream, prov.Snom, prov.Fanvil}

type redirectView struct {
	Vendor          prov.Vendor     `json:"vendor"`
	Enabled         bool            `json:"enabled"`
	HasCredentials  bool            `json:"hasCredentials"`
	FromDeployment  bool            `json:"fromDeployment"`
	Settings        json.RawMessage `json:"settings"`
	LastCheckAt     *time.Time      `json:"lastCheckAt"`
	LastCheckResult string          `json:"lastCheckResult"`
	Supported       bool            `json:"supported"`
}

func (s *server) redirectViews(ctx context.Context) ([]redirectView, error) {
	accts, err := s.ProvStore.ListRedirectAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]redirectView, 0, len(redirectVendors))
	for _, v := range redirectVendors {
		a := accts[v]
		dep := s.Prov.Deployment[v] != nil
		view := redirectView{Vendor: v, Enabled: a.Enabled || dep, HasCredentials: a.HasCredentials || dep,
			FromDeployment: dep, Settings: a.Settings, LastCheckAt: a.LastCheckAt, LastCheckResult: a.LastCheckResult,
			Supported: store.RedirectSupported(v)}
		if view.Settings == nil {
			view.Settings = json.RawMessage(`{}`)
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *server) listRedirect(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	out, err := s.redirectViews(r.Context())
	if err != nil {
		s.internal(w, "list redirect accounts", err)
		return
	}
	writeJSON(w, http.StatusOK, items(out))
}

// redirectVendor parses {vendor}; it answers 404 itself.
func redirectVendor(w http.ResponseWriter, r *http.Request) (prov.Vendor, bool) {
	v := prov.Vendor(r.PathValue("vendor"))
	for _, rv := range redirectVendors {
		if v == rv {
			return v, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "no redirect service for this vendor")
	return "", false
}

func (s *server) redirectView(ctx context.Context, v prov.Vendor) (redirectView, error) {
	all, err := s.redirectViews(ctx)
	if err != nil {
		return redirectView{}, err
	}
	for _, a := range all {
		if a.Vendor == v {
			return a, nil
		}
	}
	return redirectView{}, prov.ErrNotFound
}

// putRedirect stores a vendor's account; new credentials are checked with
// the vendor before they are saved and are never returned.
func (s *server) putRedirect(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	v, ok := redirectVendor(w, r)
	if !ok {
		return
	}
	if s.Prov.Deployment[v] != nil {
		writeError(w, http.StatusConflict, "conflict", "this account is set by the deployment and is read-only")
		return
	}
	var in struct {
		Enabled     *bool                `json:"enabled"`
		Credentials redirect.Credentials `json:"credentials"`
		Settings    json.RawMessage      `json:"settings"`
	}
	if !decode(w, r, &in) {
		return
	}
	var fe fieldErrs
	if in.Settings != nil {
		var obj map[string]any
		if err := json.Unmarshal(in.Settings, &obj); err != nil || obj == nil {
			fe.add("settings", "must be a JSON object")
		}
	}
	for k, val := range in.Credentials {
		if k == "" || len(k) > 64 || val == "" || len(val) > 1024 {
			// Name the key, never the value.
			fe.add("credentials", "each credential needs a key of up to 64 and a value of 1-1024 characters")
			break
		}
	}
	if len(fe) > 0 {
		writeFields(w, fe)
		return
	}
	if in.Credentials != nil {
		if !store.RedirectSupported(v) {
			writeError(w, http.StatusBadRequest, "bad_request", redirect.ErrUnsupported.Error())
			return
		}
		settings := []byte(in.Settings)
		if settings == nil {
			if cur, err := s.ProvStore.RedirectAccountCredentials(r.Context(), v); err == nil {
				settings = cur.Settings
			}
		}
		if msg, ok := s.checkRedirect(r.Context(), v, in.Credentials, settings); !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "credential check failed: "+msg)
			return
		}
	}
	if err := s.ProvStore.PutRedirectAccount(r.Context(), actor(r).String(), v, store.RedirectAccountChange{
		Enabled: in.Enabled, Credentials: in.Credentials, Settings: in.Settings,
	}); err != nil {
		s.provError(w, "redirect account", err)
		return
	}
	s.writeRedirect(w, r, v)
}

func (s *server) writeRedirect(w http.ResponseWriter, r *http.Request, v prov.Vendor) {
	view, err := s.redirectView(r.Context(), v)
	if err != nil {
		s.internal(w, "read redirect account", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// checkTimeout bounds one vendor credential check.
const checkTimeout = 15 * time.Second

// checkRedirect runs the vendor client's Check and records the outcome.
// The message is the client's error, which never carries a credential
// (contract 5).
func (s *server) checkRedirect(ctx context.Context, v prov.Vendor, creds redirect.Credentials, settings []byte) (string, bool) {
	if s.Prov.RedirectClient == nil {
		return "redirect clients are not available", false
	}
	msg, ok := "ok", true
	c, err := s.Prov.RedirectClient(v, creds, settings)
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		err = c.Check(cctx)
		cancel()
	}
	if err != nil {
		msg, ok = truncateRunes(err.Error(), 500), false
	}
	if err := s.ProvStore.RecordRedirectCheck(ctx, v, msg); err != nil {
		s.Log.Warn("redirect: record check", "vendor", v, "error", err)
	}
	return msg, ok
}

func (s *server) deleteRedirect(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	v, ok := redirectVendor(w, r)
	if !ok {
		return
	}
	if s.Prov.Deployment[v] != nil {
		writeError(w, http.StatusConflict, "conflict", "this account is set by the deployment and is read-only")
		return
	}
	if err := s.ProvStore.DeleteRedirectAccount(r.Context(), actor(r).String(), v); err != nil {
		s.provError(w, "redirect account", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// postRedirectCheck tests the effective credentials: the deployment's,
// else the stored ones.
func (s *server) postRedirectCheck(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	v, ok := redirectVendor(w, r)
	if !ok {
		return
	}
	creds := s.Prov.Deployment[v]
	var settings []byte
	a, err := s.ProvStore.RedirectAccountCredentials(r.Context(), v)
	switch {
	case err == nil:
		settings = a.Settings
		if creds == nil {
			creds = a.Credentials
		}
	case !errors.Is(err, prov.ErrNotFound):
		s.provError(w, "redirect account", err)
		return
	}
	if creds == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "no credentials are set for this vendor")
		return
	}
	s.checkRedirect(r.Context(), v, creds, settings)
	s.writeRedirect(w, r, v)
}

// Settings.

type dhcpValue struct {
	Vendor prov.Vendor `json:"vendor"`
	Option int         `json:"option"`
	Value  string      `json:"value"`
}

// provSettings is the computed provisioning setup: the URLs, the CA
// fingerprint and the DHCP option values per vendor (spec S-10: option 66
// for every vendor; Poly also reads 160 and Yealink 43).
func (s *server) provSettings(w http.ResponseWriter, _ *http.Request) {
	ps := s.Prov.Settings
	boot := ps.BootURL()
	dhcp := []dhcpValue{}
	if boot != "" {
		for _, v := range prov.Vendors {
			dhcp = append(dhcp, dhcpValue{Vendor: v, Option: 66, Value: boot})
			switch v {
			case prov.Poly:
				dhcp = append(dhcp, dhcpValue{Vendor: v, Option: 160, Value: boot})
			case prov.Yealink:
				dhcp = append(dhcp, dhcpValue{Vendor: v, Option: 43, Value: boot})
			}
		}
	}
	public := ""
	if ps.PublicURL != "" {
		public = ps.PublicURL + "/"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"publicUrl": public, "bootUrl": boot, "caUrl": ps.CAURL(), "caSha256": s.Prov.CASHA256,
		"dhcp": dhcp, "sipServer": ps.SIPServer,
	})
}

// CSV import.

const (
	maxImportBytes = 1 << 20
	maxImportRows  = 2000
)

type importRow struct {
	Line   int      `json:"line"`
	MAC    string   `json:"mac"`
	Errors []string `json:"errors"`
}

// importPhones is POST /phones/import?dryRun=: columns
// mac,vendor,model,extension[,label][,blf] (blf numbers separated by ';'
// or spaces), an optional header row. Every row is checked (MAC, vendor,
// model, extension, duplicates in the file and against the store); a dry
// run reports the rows, an apply creates every phone in one transaction,
// or none when any row fails (400 with the same rows).
func (s *server) importPhones(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	var dry bool
	switch r.URL.Query().Get("dryRun") {
	case "true":
		dry = true
	case "false", "":
	default:
		badRequest(w, "dryRun must be true or false")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBytes))
	if err != nil {
		badRequest(w, "the CSV body is larger than 1 MiB")
		return
	}
	recs, err := readCSV(body)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	rows, inputs, err := s.checkImport(r.Context(), recs)
	if err != nil {
		s.internal(w, "import phones: check", err)
		return
	}
	ok := true
	for _, row := range rows {
		ok = ok && len(row.Errors) == 0
	}
	resp := map[string]any{"rows": rows, "ok": ok}
	if dry {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["created"] = 0
	if !ok {
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	created, err := s.ProvStore.ImportPhones(r.Context(), actor(r).String(), inputs)
	if err != nil {
		s.provError(w, "phone import", err)
		return
	}
	resp["created"] = len(created)
	writeJSON(w, http.StatusOK, resp)
}

type csvRecord struct {
	line   int
	fields []string
}

func readCSV(body []byte) ([]csvRecord, error) {
	cr := csv.NewReader(strings.NewReader(string(body)))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	var out []csvRecord
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return nil, fmt.Errorf("the CSV is malformed at line %d", pe.Line)
			}
			return nil, errors.New("the CSV is malformed")
		}
		line, _ := cr.FieldPos(0)
		if len(out) == 0 && len(rec) > 0 && strings.EqualFold(strings.TrimSpace(rec[0]), "mac") {
			continue // header
		}
		out = append(out, csvRecord{line: line, fields: rec})
		if len(out) > maxImportRows {
			return nil, fmt.Errorf("at most %d rows per import", maxImportRows)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the CSV has no rows")
	}
	return out, nil
}

func (s *server) checkImport(ctx context.Context, recs []csvRecord) ([]importRow, []store.PhoneInput, error) {
	exts, err := s.Store.ListExtensions(ctx)
	if err != nil {
		return nil, nil, err
	}
	extByNumber := make(map[string]int64, len(exts))
	for _, e := range exts {
		extByNumber[e.Number] = e.ID
	}
	rows := make([]importRow, len(recs))
	inputs := make([]store.PhoneInput, len(recs))
	firstLine := map[string]int{}
	var macs []string
	for i, rec := range recs {
		row := importRow{Line: rec.line, Errors: []string{}}
		f := rec.fields
		if len(f) < 4 || len(f) > 6 {
			row.Errors = append(row.Errors, "needs mac,vendor,model,extension[,label][,blf]")
			rows[i] = row
			continue
		}
		for j := range f {
			f[j] = strings.TrimSpace(f[j])
		}
		row.MAC = f[0]
		mac, err := prov.NormalizeMAC(f[0])
		if err != nil {
			row.Errors = append(row.Errors, "bad MAC")
		} else {
			row.MAC = mac
			if l, dup := firstLine[mac]; dup {
				row.Errors = append(row.Errors, fmt.Sprintf("duplicate MAC (also on line %d)", l))
			} else {
				firstLine[mac] = rec.line
				macs = append(macs, mac)
			}
		}
		in := store.PhoneInput{MAC: mac, Vendor: prov.Vendor(strings.ToLower(f[1])), Model: f[2], Enabled: true, Realm: s.SIPDomain}
		if len(f) > 4 {
			in.Label = f[4]
		}
		if len(f) > 5 && f[5] != "" {
			in.BLF = strings.FieldsFunc(f[5], func(r rune) bool { return r == ';' || r == ' ' })
		}
		var fe fieldErrs
		validatePhoneFields(&fe, nil, &in.Vendor, &in.Model, &in.Label, &in.BLF, nil)
		for _, e := range fe {
			if e.Path == "vendor" {
				row.Errors = append(row.Errors, "unknown vendor")
			} else {
				row.Errors = append(row.Errors, e.Path+" "+e.Message)
			}
		}
		if id, ok := extByNumber[f[3]]; ok {
			in.ExtensionID = id
		} else {
			row.Errors = append(row.Errors, "unknown extension "+strconv.Quote(truncateRunes(f[3], 20)))
		}
		for _, n := range in.BLF {
			if _, ok := extByNumber[n]; !ok {
				row.Errors = append(row.Errors, "unknown BLF extension "+strconv.Quote(truncateRunes(n, 20)))
			}
		}
		rows[i], inputs[i] = row, in
	}
	taken, err := s.ProvStore.ExistingMACs(ctx, macs)
	if err != nil {
		return nil, nil, err
	}
	for i := range rows {
		if id, ok := taken[rows[i].MAC]; ok {
			rows[i].Errors = append(rows[i].Errors, fmt.Sprintf("duplicate MAC (already phone %d)", id))
		}
	}
	return rows, inputs, nil
}
