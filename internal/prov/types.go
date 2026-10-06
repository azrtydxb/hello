// Package prov is Hello's phone auto-provisioning: the vendor file sets,
// template resolution and rendering, provisioning tokens, the HTTP handler
// phones fetch their configuration from, its rate limiter, the buffered
// fetch audit and the metrics (spec phone-auto-provisioning-service).
//
// This file holds the shared contract the parallel work streams build on;
// see the trailing comment for the functions it promises.
package prov

import (
	"net/netip"
	"slices"
	"strconv"
	"time"
)

// Vendor is a phone brand. The five first-class vendors have built-in
// templates and tested request paths; Generic phones work only through
// administrator templates.
type Vendor string

// The vendors (spec S-1).
const (
	Yealink     Vendor = "yealink"
	Poly        Vendor = "poly"
	Grandstream Vendor = "grandstream"
	Snom        Vendor = "snom"
	Fanvil      Vendor = "fanvil"
	Generic     Vendor = "generic"
)

// Vendors lists every vendor, first-class ones first.
var Vendors = []Vendor{Yealink, Poly, Grandstream, Snom, Fanvil, Generic}

// Valid reports whether v is one of Vendors.
func (v Vendor) Valid() bool { return slices.Contains(Vendors, v) }

// FileKind classifies a requested file (spec S-7, S-17).
type FileKind string

// The file kinds.
const (
	KindCommon   FileKind = "common"   // a vendor's model or site common file
	KindDevice   FileKind = "device"   // the per-MAC file carrying the account
	KindMaster   FileKind = "master"   // Poly's per-MAC master (000000000000.cfg form)
	KindFirmware FileKind = "firmware" // /p/<token>/fw/<file>
	KindCA       FileKind = "ca"       // /p/ca.crt, /p/ca.der
	KindBoot     FileKind = "boot"     // /p/boot/<file>
	KindUpload   FileKind = "upload"   // a discarded PUT
	KindOther    FileKind = "other"    // anything not recognised
)

// FileKinds lists every file kind.
var FileKinds = []FileKind{KindCommon, KindDevice, KindMaster, KindFirmware, KindCA, KindBoot, KindUpload, KindOther}

// Result is the outcome recorded on a fetch (spec S-14).
type Result string

// The fetch results. NotFound and Unavailable extend the spec S-14 list:
// NotFound is a well-formed request with nothing to serve (a file name
// outside the phone's file set, Yealink's -local.cfg, a missing CA file),
// Unavailable is the 503 answered while PostgreSQL or MinIO is down.
const (
	ResultServed          Result = "served"
	ResultNotModified     Result = "not_modified"
	ResultUnknownToken    Result = "unknown_token"
	ResultMACMismatch     Result = "mac_mismatch"
	ResultNotAllowlisted  Result = "not_allowlisted"
	ResultPlainHTTP       Result = "plain_http"
	ResultRateLimited     Result = "rate_limited"
	ResultNoTemplate      Result = "no_template"
	ResultRenderError     Result = "render_error"
	ResultBootServed      Result = "boot_served"
	ResultBootHandoff     Result = "boot_handoff"
	ResultBootReclaim     Result = "boot_reclaim"
	ResultBootDenied      Result = "boot_denied"
	ResultUploadDiscarded Result = "upload_discarded"
	ResultNotFound        Result = "not_found"
	ResultUnavailable     Result = "unavailable"
)

// Results lists every fetch result.
var Results = []Result{
	ResultServed, ResultNotModified, ResultUnknownToken, ResultMACMismatch, ResultNotAllowlisted,
	ResultPlainHTTP, ResultRateLimited, ResultNoTemplate, ResultRenderError, ResultBootServed,
	ResultBootHandoff, ResultBootReclaim, ResultBootDenied, ResultUploadDiscarded, ResultNotFound,
	ResultUnavailable,
}

// RenderData is the only value a template sees (spec S-8). Nothing outside
// it is reachable: its fields are plain data with no methods.
type RenderData struct {
	Phone    Phone
	Line     Line
	Server   Server
	BLF      []BLFKey
	Prov     ProvInfo
	Firmware *FirmwareInfo // nil when no firmware is pinned for the model
	Time     TimeInfo
}

// Phone is the phone itself, as templates and resolution see it.
type Phone struct {
	MAC           string // 12 lowercase hex digits, no separators
	MACUpper      string // the same, uppercase
	Vendor        Vendor
	Model         string
	Label         string
	AdminPassword string // the phone's web UI password (spec S-20); masked in previews
}

// Line is the SIP account of the phone's bound device.
type Line struct {
	Username    string
	AuthName    string
	Password    string // the opened device secret; masked in previews
	DisplayName string // the extension's name
	Label       string // the extension number
	Domain      string // HELLO_SIP_DOMAIN
	// VoicemailCode is the feature code the message key dials (spec S-5);
	// empty when no voicemail feature code is configured.
	VoicemailCode string
}

// Server is the registrar the phone registers with.
type Server struct {
	Host      string
	Port      int
	Transport string // always "udp": Hello is UDP-only
	Expiry    int    // registration expiry, seconds
}

// BLFKey is one busy-lamp-field key.
type BLFKey struct {
	Number string // extension number
	Label  string // the extension's name
	URI    string // sip:<number>@<domain>
}

// ProvInfo is the phone's own provisioning URL and re-check settings.
type ProvInfo struct {
	URL           string // https://…/p/<current token>/; masked in previews
	CAURL         string // the plain-HTTP CA certificate URL
	ResyncSeconds int    // re-check interval, jitter from the MAC included
}

// FirmwareInfo is the firmware pinned for the phone's model.
type FirmwareInfo struct {
	URL     string
	Version string
}

// TimeInfo is the phone's clock setup.
type TimeInfo struct {
	Zone string // HELLO_PROV_TIMEZONE
	NTP  string // HELLO_PROV_NTP
}

// Template is a set of files rendered for the phones it matches (spec S-8).
// A built-in template lives in the binary and has ID 0 and BuiltinRef set
// to its own name; an administrator template has the database ID, and
// BuiltinRef names the built-in it copies, if any.
type Template struct {
	ID         int64
	Vendor     Vendor
	ModelGlob  string
	Priority   int
	Name       string
	Files      []TemplateFile
	BuiltinRef string
	Version    int
}

// Builtin reports whether t ships in the binary (read-only).
func (t Template) Builtin() bool { return t.ID == 0 }

// TemplateFile is one file of a template. Pattern is the requested file
// name with {mac}, {MAC} and {model} placeholders; Body is a Go
// text/template over RenderData. Its JSON is the prov_templates.files
// element shape.
type TemplateFile struct {
	Pattern     string `json:"pattern"`
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

// FieldError is one template validation failure. Path names the field
// ("files[1].body", "files[0].pattern", "modelGlob") as in the Phase 1
// error envelope's fields; Line is the 1-based body line when known.
type FieldError struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Firmware is one uploaded firmware file (a prov_firmware row).
type Firmware struct {
	ID         int64
	Vendor     Vendor
	ModelGlob  string
	Version    string
	Filename   string
	ObjectKey  string // <vendor>/<sha256>/<filename> in bucket hello-firmware
	Size       int64
	SHA256     string // lowercase hex
	UploadedAt time.Time
}

// FetchRecord is one prov_fetches row. PathRedacted must already have
// passed through RedactPath; the database refuses a path that still
// carries a token.
type FetchRecord struct {
	At           time.Time
	PhoneID      int64 // 0 when neither the token nor the MAC resolved a phone
	MACClaimed   string
	IP           netip.Addr
	UserAgent    string // at most 256 bytes, cut on a rune boundary
	PathRedacted string
	Kind         FileKind
	Result       Result
	Status       int
	Bytes        int64
	// UAMismatch: the User-Agent named a MAC other than the phone's
	// (evidence only, spec S-6).
	UAMismatch bool
}

// Sealing contexts (additional data) for internal/secret. hello-control
// seals with them; whatever opens the value uses the same one, so a sealed
// value copied to another row does not open.

// DeviceSecretAAD is the context of a bound device's SIP secret.
func DeviceSecretAAD(deviceID int64) string { return "device:" + strconv.FormatInt(deviceID, 10) }

// PhoneTokenAAD is the context of a phone's sealed provisioning token.
// It and PhoneAdminAAD need the row's ID before the insert that stores the
// sealed values: reserve it with nextval('phones_id_seq').
func PhoneTokenAAD(phoneID int64) string { return "phone-token:" + strconv.FormatInt(phoneID, 10) }

// PhoneAdminAAD is the context of a phone's sealed admin password.
func PhoneAdminAAD(phoneID int64) string { return "phone-admin:" + strconv.FormatInt(phoneID, 10) }

// RedirectAAD is the context of a vendor's sealed redirect credentials.
func RedirectAAD(v Vendor) string { return "redirect:" + string(v) }

// Functions this package provides (plan Task 2), with exactly these
// signatures:
//
//	NormalizeMAC(s string) (string, error)
//	    accepts 12 hex digits with ':', '-' or '.' separators in any case
//	    and returns them lowercase without separators
//	MatchFile(v Vendor, model, mac, name string) (FileKind, bool)
//	    classifies a requested file name for a phone of vendor v and model
//	    whose MAC is mac; false when the name is not in the file set or
//	    embeds another MAC
//	Resolve(phone Phone, override *Template, all []Template) (Template, bool)
//	    the override, else the highest priority match, then the more
//	    specific glob, then the lower ID
//	Render(ctx context.Context, t Template, file string, d RenderData) ([]byte, error)
//	    renders the file of t matching the requested name, within 100 ms
//	    and 256 KiB, byte-identical for identical input
//	Validate(t Template) []FieldError
//	NewToken() (plain string, hash []byte)
//	    32 random bytes, base32 lowercase without padding, and its SHA-256
//	RedactPath(p string) string
//	    /p/<token>/… becomes /p/****/…
//	Builtins() []Template
//	    the embedded built-in templates (ID 0, BuiltinRef their name), for
//	    resolution and for the templates API to list and copy
