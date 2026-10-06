package api

// Phone auto-provisioning management (spec phone-auto-provisioning-service,
// plan contract 7): phones, tokens, the admin password, the fetch log and
// the preview. Templates, firmware, redirect accounts, settings and the
// CSV import are in prov_admin.go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
	"github.com/azrtydxb/hello/internal/store"
)

// ProvStore is the provisioning persistence the API needs; *store.Store
// implements it.
type ProvStore interface {
	ListPhones(ctx context.Context) ([]store.Phone, error)
	GetPhone(ctx context.Context, id int64) (store.Phone, error)
	CreatePhone(ctx context.Context, actor string, in store.PhoneInput) (store.CreatedPhone, error)
	ImportPhones(ctx context.Context, actor string, in []store.PhoneInput) ([]store.Phone, error)
	UpdatePhone(ctx context.Context, actor string, id int64, c store.PhoneChange) (store.Phone, bool, error)
	DeletePhone(ctx context.Context, actor string, id int64) error
	RotatePhoneToken(ctx context.Context, actor string, id int64, immediate bool) (store.CreatedPhone, error)
	RearmPhone(ctx context.Context, actor string, id int64) (store.CreatedPhone, error)
	RevealAdminPassword(ctx context.Context, actor string, id int64) (string, error)
	RotateAdminPassword(ctx context.Context, actor string, id int64) error
	ExistingMACs(ctx context.Context, macs []string) (map[string]int64, error)
	ListPhoneFetches(ctx context.Context, phoneID, before int64, limit int) ([]store.Fetch, string, error)
	RenderInputs(ctx context.Context, phoneID int64) (prov.RenderData, prov.Template, error)

	ListProvTemplates(ctx context.Context) ([]store.ProvTemplate, error)
	GetProvTemplate(ctx context.Context, id int64) (store.ProvTemplate, error)
	CreateProvTemplate(ctx context.Context, actor string, t prov.Template) (store.ProvTemplate, error)
	UpdateProvTemplate(ctx context.Context, actor string, id int64, apply func(*prov.Template) []prov.FieldError) (store.ProvTemplate, error)
	DeleteProvTemplate(ctx context.Context, actor string, id int64) error

	ListFirmware(ctx context.Context) ([]store.FirmwareFile, error)
	CreateFirmware(ctx context.Context, actor string, f prov.Firmware) (store.FirmwareFile, error)
	DeleteFirmware(ctx context.Context, actor string, id int64) (string, error)
	FirmwareByName(ctx context.Context, v prov.Vendor, filename string) (prov.Firmware, error)
	ListFirmwarePins(ctx context.Context) ([]store.FirmwarePin, error)
	PutFirmwarePins(ctx context.Context, actor string, pins []store.FirmwarePin) ([]store.FirmwarePin, error)

	ListRedirectAccounts(ctx context.Context) (map[prov.Vendor]store.RedirectAccount, error)
	PutRedirectAccount(ctx context.Context, actor string, v prov.Vendor, c store.RedirectAccountChange) error
	DeleteRedirectAccount(ctx context.Context, actor string, v prov.Vendor) error
	RecordRedirectCheck(ctx context.Context, v prov.Vendor, result string) error
	RedirectAccountCredentials(ctx context.Context, v prov.Vendor) (redirect.Account, error)
}

// ProvConfig wires the provisioning management API.
type ProvConfig struct {
	// Settings are the store's provisioning settings (public URL, SIP
	// server), for the URLs the API hands out and the settings view.
	Settings store.ProvSettings
	// CASHA256 is the hex SHA-256 of the provisioning CA certificate (DER);
	// empty without HELLO_PROV_CA_CERT.
	CASHA256 string
	// Firmware stores firmware files; nil answers 503 on upload.
	Firmware FirmwareObjects
	// RedirectClient builds a vendor's redirect client (redirect.New); nil
	// answers 503 on a credential check.
	RedirectClient func(v prov.Vendor, creds redirect.Credentials, settings []byte) (redirect.Client, error)
	// Deployment holds the redirect credentials the deployment sets
	// (redirect.Deployment); they are read-only through the API.
	Deployment map[prov.Vendor]redirect.Credentials
}

const (
	maxLabelLen = 100
	maxBLFKeys  = 100
)

var (
	modelRe  = regexp.MustCompile(`^[A-Za-z0-9 ._+()/-]{1,64}$`)
	serialRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// tokenInPathRe finds a provisioning token in a URL, for masking.
	tokenInPathRe = regexp.MustCompile(`/p/[a-z2-7]{52}/`)
)

// mask replaces every secret a preview could show (spec S-9).
const mask = "********"

// phoneView is a phone with the one-time values of create, rotate and
// re-arm: the URL with the token, and whether binding rotated a device's
// secret.
type phoneView struct {
	store.Phone
	ProvisioningURL string `json:"provisioningUrl,omitempty"`
	SecretRotated   bool   `json:"secretRotated,omitempty"`
}

func (s *server) provDisabled(w http.ResponseWriter) bool {
	if s.ProvStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "provisioning is not configured")
		return true
	}
	return false
}

// provError answers a provisioning store failure.
func (s *server) provError(w http.ResponseWriter, what string, err error) {
	var te *store.TemplateError
	switch {
	case errors.As(err, &te):
		writeProvFields(w, te.Fields)
	case errors.Is(err, prov.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, prov.ErrSealed):
		s.Log.Error(what+": a sealed value does not open under HELLO_SECRET_KEY", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "a stored secret does not open; check HELLO_SECRET_KEY")
	default:
		s.configError(w, what, err)
	}
}

// writeProvFields answers a template validation failure; each field
// carries its body line (contract 7).
func writeProvFields(w http.ResponseWriter, fields []prov.FieldError) {
	msg := "invalid template"
	if len(fields) > 0 {
		msg += ": " + fields[0].Path + ": " + fields[0].Message
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
		"code": "bad_request", "message": msg, "fields": fields,
	}})
}

func (s *server) listPhones(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	ps, err := s.ProvStore.ListPhones(r.Context())
	if err != nil {
		s.internal(w, "list phones", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ps))
}

func (s *server) getPhone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	p, err := s.ProvStore.GetPhone(r.Context(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// validatePhoneFields checks the phone fields that are not nil, normalising
// in place.
func validatePhoneFields(f *fieldErrs, serial *string, vendor *prov.Vendor, model, label *string, blf *[]string, templateID *int64) {
	if serial != nil && *serial != "" && !serialRe.MatchString(*serial) {
		f.add("serial", "must be empty or 1-64 of A-Z a-z 0-9 . _ -")
	}
	if vendor != nil && !vendor.Valid() {
		f.add("vendor", "must be one of yealink, poly, grandstream, snom, fanvil, generic")
	}
	if model != nil {
		*model = strings.TrimSpace(*model)
		if !modelRe.MatchString(*model) {
			f.add("model", "must be 1-64 of A-Z a-z 0-9 space . _ + ( ) / -")
		}
	}
	if label != nil {
		*label = strings.TrimSpace(*label)
		if !printable(*label, maxLabelLen, true) {
			f.add("label", "must be at most 100 printable characters")
		}
	}
	if blf != nil {
		if len(*blf) > maxBLFKeys {
			f.add("blf", "at most %d keys", maxBLFKeys)
		}
		for i, n := range *blf {
			if !numberRe.MatchString(n) {
				f.add("blf["+strconv.Itoa(i)+"]", "must be an extension number")
			}
		}
	}
	if templateID != nil && *templateID <= 0 {
		f.add("templateId", "must be a template id")
	}
}

func (s *server) createPhone(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	var in struct {
		MAC         string      `json:"mac"`
		Serial      string      `json:"serial"`
		Vendor      prov.Vendor `json:"vendor"`
		Model       string      `json:"model"`
		Label       string      `json:"label"`
		ExtensionID int64       `json:"extensionId"`
		DeviceID    *int64      `json:"deviceId"`
		TemplateID  *int64      `json:"templateId"`
		BLF         []string    `json:"blf"`
		Enabled     *bool       `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	mac, err := prov.NormalizeMAC(in.MAC)
	if err != nil {
		f.add("mac", "must be 12 hex digits, optionally separated by : - or .")
	}
	if in.ExtensionID <= 0 {
		f.add("extensionId", "is required")
	}
	if in.DeviceID != nil && *in.DeviceID <= 0 {
		f.add("deviceId", "must be a device id")
	}
	validatePhoneFields(&f, &in.Serial, &in.Vendor, &in.Model, &in.Label, &in.BLF, in.TemplateID)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	var dev int64
	if in.DeviceID != nil {
		dev = *in.DeviceID
	}
	c, err := s.ProvStore.CreatePhone(r.Context(), actor(r).String(), store.PhoneInput{
		MAC: mac, Serial: in.Serial, Vendor: in.Vendor, Model: in.Model, Label: in.Label,
		ExtensionID: in.ExtensionID, DeviceID: dev, BLF: in.BLF, Enabled: in.Enabled == nil || *in.Enabled,
		TemplateID: in.TemplateID, Realm: s.SIPDomain,
	})
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusCreated, phoneView{
		Phone: c.Phone, ProvisioningURL: s.Prov.Settings.PhoneURL(c.Token, c.Phone.Vendor), SecretRotated: c.SecretRotated,
	})
}

func (s *server) updatePhone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	var in struct {
		Serial      *string         `json:"serial"`
		Vendor      *prov.Vendor    `json:"vendor"`
		Model       *string         `json:"model"`
		Label       *string         `json:"label"`
		ExtensionID *int64          `json:"extensionId"`
		DeviceID    json.RawMessage `json:"deviceId"`
		TemplateID  json.RawMessage `json:"templateId"`
		BLF         *[]string       `json:"blf"`
		Enabled     *bool           `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	c := store.PhoneChange{Serial: in.Serial, Vendor: in.Vendor, Model: in.Model, Label: in.Label, BLF: in.BLF,
		Enabled: in.Enabled, Realm: s.SIPDomain}
	var f fieldErrs
	var tpl *int64
	if in.TemplateID != nil {
		if string(in.TemplateID) != "null" {
			tpl = new(int64)
			if err := json.Unmarshal(in.TemplateID, tpl); err != nil {
				f.add("templateId", "must be a template id or null")
			}
		}
		c.TemplateID = &tpl
	}
	switch {
	case string(in.DeviceID) == "null":
		// deviceId null unbinds; with no device the phone is disabled.
		c.Unbind = true
	case in.DeviceID != nil || in.ExtensionID != nil:
		var dev int64
		if in.DeviceID != nil {
			if err := json.Unmarshal(in.DeviceID, &dev); err != nil || dev <= 0 {
				f.add("deviceId", "must be a device id or null")
			}
		}
		if in.ExtensionID == nil || *in.ExtensionID <= 0 {
			f.add("extensionId", "is required to bind a device")
		} else {
			c.Bind = &store.Binding{ExtensionID: *in.ExtensionID, DeviceID: dev}
		}
	}
	validatePhoneFields(&f, in.Serial, in.Vendor, in.Model, in.Label, in.BLF, tpl)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	p, rotated, err := s.ProvStore.UpdatePhone(r.Context(), actor(r).String(), id, c)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, phoneView{Phone: p, SecretRotated: rotated})
}

func (s *server) deletePhone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	if err := s.ProvStore.DeletePhone(r.Context(), actor(r).String(), id); err != nil {
		s.provError(w, "phone", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) rotatePhoneToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	var immediate bool
	switch v := r.URL.Query().Get("immediate"); v {
	case "", "false":
	case "true":
		immediate = true
	default:
		badRequest(w, "immediate must be true or false")
		return
	}
	c, err := s.ProvStore.RotatePhoneToken(r.Context(), actor(r).String(), id, immediate)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, phoneView{Phone: c.Phone, ProvisioningURL: s.Prov.Settings.PhoneURL(c.Token, c.Phone.Vendor)})
}

func (s *server) rearmPhone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	c, err := s.ProvStore.RearmPhone(r.Context(), actor(r).String(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	writeJSON(w, http.StatusOK, phoneView{Phone: c.Phone, ProvisioningURL: s.Prov.Settings.PhoneURL(c.Token, c.Phone.Vendor)})
}

func (s *server) revealAdminPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	pw, err := s.ProvStore.RevealAdminPassword(r.Context(), actor(r).String(), id)
	if err != nil {
		s.provError(w, "phone", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"adminPassword": pw})
}

func (s *server) rotateAdminPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	if err := s.ProvStore.RotateAdminPassword(r.Context(), actor(r).String(), id); err != nil {
		s.provError(w, "phone", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) phoneFetches(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	q := r.URL.Query()
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "before must be a positive fetch id")
			return
		}
		before = n
	}
	limit := defaultCDRLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxCDRLimit {
			badRequest(w, "limit must be 1-200")
			return
		}
		limit = n
	}
	if _, err := s.ProvStore.GetPhone(r.Context(), id); err != nil {
		s.provError(w, "phone", err)
		return
	}
	fs, next, err := s.ProvStore.ListPhoneFetches(r.Context(), id, before, limit)
	if err != nil {
		s.internal(w, "list phone fetches", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": fs, "next": next})
}

// previewTimeout bounds a preview render beyond the renderer's own 100 ms.
const previewTimeout = 2 * time.Second

// previewPhone renders a file as the phone would get it, with the SIP
// secret, the admin password and the token masked before rendering, so a
// template that transforms them cannot leak them (spec S-9). It records no
// fetch.
func (s *server) previewPhone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	file := r.URL.Query().Get("file")
	if file == "" || len(file) > 255 || strings.ContainsAny(file, "/\\") {
		badRequest(w, "file must be the file name a phone requests")
		return
	}
	d, t, err := s.ProvStore.RenderInputs(r.Context(), id)
	switch {
	case errors.Is(err, prov.ErrNoTemplate):
		writeError(w, http.StatusNotFound, "not_found", "no template matches this phone")
		return
	case err != nil:
		s.provError(w, "phone", err)
		return
	}
	maskSecrets(&d)
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()
	body, err := prov.Render(ctx, t, file, d)
	if err != nil {
		if errors.Is(err, prov.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "the phone's template has no file "+strconv.Quote(file))
			return
		}
		// Rendered with the secrets already masked, so the message holds
		// none of them.
		writeError(w, http.StatusUnprocessableEntity, "render_error", "the template does not render: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body) //nolint:gosec // G705: text/plain with nosniff, an administrator's own template output
}

// maskSecrets replaces every secret in d with the mask.
func maskSecrets(d *prov.RenderData) {
	d.Line.Password = mask
	d.Phone.AdminPassword = mask
	d.Prov.URL = tokenInPathRe.ReplaceAllString(d.Prov.URL, "/p/"+mask+"/")
	if d.Firmware != nil {
		fw := *d.Firmware
		fw.URL = tokenInPathRe.ReplaceAllString(fw.URL, "/p/"+mask+"/")
		d.Firmware = &fw
	}
}
