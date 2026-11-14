// Package prov is Hello's phone auto-provisioning: the vendor file sets,
// template resolution and rendering, provisioning tokens, the HTTP handler
// phones fetch their configuration from, its rate limiter, the buffered
// fetch audit and the metrics (spec phone-auto-provisioning-service).
//
// This file holds the shared contract the parallel work streams build on;
// see the trailing comment for the functions it promises.
package prov

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
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

// SIPMessage is the vendor's capability flag for SIP MESSAGE texting on the
// handset (spec messaging-channels M-26). Only Grandstream is known to show
// it, from its administration guide (P22650 in the built-in template); it is
// unverified on hardware. Every other vendor is off until a lab check proves
// its handset has a text UI, so the console offers desk-phone delivery only
// to an extension with at least one phone that has it.
func (v Vendor) SIPMessage() bool { return v == Grandstream }

// Vendors lists every vendor, first-class ones first.
var Vendors = []Vendor{Yealink, Poly, Grandstream, Snom, Fanvil, Generic}

// Valid reports whether v is one of Vendors.
func (v Vendor) Valid() bool { return slices.Contains(Vendors, v) }

// FileKind classifies a requested file (spec S-7, S-17).
type FileKind string

// The file kinds.
const (
	KindCommon    FileKind = "common"    // a vendor's model or site common file
	KindDevice    FileKind = "device"    // the per-MAC file carrying the account
	KindMaster    FileKind = "master"    // Poly's per-MAC master (000000000000.cfg form)
	KindFirmware  FileKind = "firmware"  // /p/<token>/fw/<file>
	KindCA        FileKind = "ca"        // /p/ca.crt, /p/ca.der
	KindBoot      FileKind = "boot"      // /p/boot/<file>
	KindUpload    FileKind = "upload"    // a discarded PUT
	KindPhonebook FileKind = "phonebook" // /p/<token>/pb/<n>.xml and Poly's <mac>-directory.xml
	KindSCEP      FileKind = "scep"      // /p/<token>/scep (S-25)
	KindRingtone  FileKind = "ringtone"  // /p/<token>/ring/<name>.wav (S-43)
	KindOther     FileKind = "other"     // anything not recognised
)

// FileKinds lists every file kind.
var FileKinds = []FileKind{KindCommon, KindDevice, KindMaster, KindFirmware, KindCA, KindBoot, KindUpload, KindPhonebook, KindRingtone, KindSCEP, KindOther}

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
	// ResultSCEPIssued is a SCEP PKIOperation that issued (or re-sent) a
	// certificate; ResultSCEPRefused one answered with a failure, or a
	// malformed SCEP request (S-25).
	ResultSCEPIssued  Result = "scep_issued"
	ResultSCEPRefused Result = "scep_refused"
)

// Results lists every fetch result.
var Results = []Result{
	ResultServed, ResultNotModified, ResultUnknownToken, ResultMACMismatch, ResultNotAllowlisted,
	ResultPlainHTTP, ResultRateLimited, ResultNoTemplate, ResultRenderError, ResultBootServed,
	ResultBootHandoff, ResultBootReclaim, ResultBootDenied, ResultUploadDiscarded, ResultNotFound,
	ResultUnavailable, ResultSCEPIssued, ResultSCEPRefused,
}

// RenderData is the only value a template sees (spec S-8). Nothing outside
// it is reachable: its fields are plain data with no methods.
type RenderData struct {
	Phone Phone
	// Line is the phone's first line. Lines holds its further lines (2 and
	// up, in order; empty for a one-line phone), each a Line with N set.
	Line   Line
	Lines  []Line
	Server Server
	BLF    []BLFKey
	// VMKey is the transfer-to-voicemail key (see PlaceVMKey); nil when the
	// phone has no key for it. Render fills it, templates only read it.
	VMKey    *VMKey
	Prov     ProvInfo
	Firmware *FirmwareInfo // nil when no firmware is pinned for the model
	Book     *BookInfo     // nil when the phone has no address book to fetch
	// LDAP is the phone's LDAP directory setting (spec pbx-gaps S-26); nil
	// when its group has LDAP off, LDAP is not served or the model has none.
	LDAP *LDAPInfo
	// Multicast is the multicast paging the phone listens to (its group's
	// catalog channels, or every enabled paging channel when the group names
	// none or the phone has no group); nil when there is none. PTT is the
	// phone's push-to-talk, nil unless the phone supports it (SupportsPTT).
	// Paging is how the phone takes a page and an intercom call; never nil
	// once the store has built the data.
	Multicast *MulticastInfo
	PTT       *PTTInfo
	Paging    *PagingOptions
	Time      TimeInfo
	// Syslog is where the phone sends its log (phone-feature-parity S-27);
	// nil when its group has syslog off, which is the default.
	Syslog *SyslogInfo
	// Codes are the PBX's star codes (feature_codes), for phones whose own
	// DND and forward keys dial them; a code the PBX does not define is
	// empty.
	Codes FeatureCodes
	// Codecs is the HD voice codec plan: Opus first where the model has
	// it, then G.722, PCMU and PCMA (see CodecInfo).
	Codecs CodecInfo
	// Overrides are the setting overrides of the phone's group and of the
	// phone, resolved (see ResolveOverrides); Render applies them to the
	// phone's own file after the template. Secret values are masked in
	// previews.
	Overrides []Override

	// Ring is the ring map (phone-feature-parity S-44): the distinctive-ring
	// rules that map each ring tag's Alert-Info to a tone, and the line's
	// default tone (see RingInfo).
	Ring RingInfo
	// Wifi is the Wi-Fi setup of a handset (phone-feature-parity S-23, S-24);
	// nil when the phone has no profile and no radio setting. Render fills
	// Wifi.Keys for the phone's model.
	Wifi *WifiInfo
	// Sharing is the account-sharing role of a Grandstream WP8x6 in a phone
	// group that enabled it (S-11); nil for any other phone.
	Sharing *SharingInfo
	// Emergency is the comma-separated emergency numbers of Hello's
	// emergency-flagged outbound routes (digits only), empty when none.
	Emergency string
	// E911 is the phone's E911 and HELD provisioning (phone-feature-parity
	// S-28); nil unless the phone's group has E911 on and the phone is a
	// WP8x6. AlarmNumber is the number a WP8x6 dials for its alarm (*55), set
	// only when the phone's group has an alarm destination (S-29).
	E911        *E911Info
	AlarmNumber string
	// SCEP is the phone's SCEP client settings (phone-feature-parity
	// S-25), nil when SCEP is not on for it. Dot1X is its 802.1X profiles,
	// nil when there are none.
	SCEP  *SCEPInfo
	Dot1X *Dot1XInfo
	// DoorStations are the door stations that can call the phone's
	// extension (at most MaxDoorStations), for the Grandstream WP door
	// system entries (spec phone-feature-parity S-49).
	DoorStations []DoorStation
}

// E911Info is what a WP8x6 needs to ask Hello for its location (RFC 5985).
type E911Info struct {
	// Server is the location server address without a scheme:
	// host[:port]/p/<token>/held. It carries the phone's token, so previews
	// mask it like Prov.URL. The phone reaches it over HTTPS (P8566 = 1).
	Server string
	// IntervalMinutes is how often the phone asks again (30 to 1440).
	IntervalMinutes int
	// Numbers are the emergency numbers the E911 service watches for,
	// separated by ";" as the phone takes them.
	Numbers string
}

// SyslogInfo is a phone's syslog setting. Host is the sink's bare host and
// Port its UDP port (514 when the configured address names none); Address
// is host or host:port as configured, for settings that take one string.
// Level is the Grandstream P208 value (1 debug, 2 info, 3 warning, 4 error),
// YealinkLevel static.syslog.level (0 to 6) and PolyLevel
// device.syslog.renderLevel; vendors' scales are mapped in NewSyslogInfo.
type SyslogInfo struct {
	Host, Address string
	Port          int
	Level         int
	YealinkLevel  int
	PolyLevel     int
}

// NewSyslogInfo builds the setting from the sink address (host or
// host:port) and a level name (debug, info, warning or error).
func NewSyslogInfo(address, level string) *SyslogInfo {
	host, port := address, 514
	if h, p, err := net.SplitHostPort(address); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			host, port = h, n
		}
	}
	i := &SyslogInfo{Host: host, Address: address, Port: port}
	switch level {
	case "debug":
		i.Level, i.YealinkLevel, i.PolyLevel = 1, 6, 0
	case "warning":
		i.Level, i.YealinkLevel, i.PolyLevel = 3, 4, 4
	case "error":
		i.Level, i.YealinkLevel, i.PolyLevel = 4, 3, 5
	default:
		i.Level, i.YealinkLevel, i.PolyLevel = 2, 6, 1
	}
	return i
}

// MaxDoorStations is how many door system entries a WP takes (10).
const MaxDoorStations = 10

// DoorStation is a door station as a phone lists it: the number it calls
// from and the name the phone shows.
type DoorStation struct {
	Number string
	Name   string
}

// Phone is the phone itself, as templates and resolution see it.
type Phone struct {
	MAC           string // 12 lowercase hex digits, no separators
	MACUpper      string // the same, uppercase
	Vendor        Vendor
	Model         string
	Label         string
	AdminPassword string // the phone's web UI password (spec S-20); masked in previews
	// PTTCapable is set for a phone with the PTT and group paging feature
	// set (SupportsPTT): a Grandstream WP. WP is its family (WPFamily: WP8x6,
	// WP856, WP8xx or WP820), "" for another phone. Render fills both from
	// Vendor and Model.
	PTTCapable bool
	WP         string
	// CWInverted is set for the Grandstream WP856, whose published template
	// reads P26063 the other way round (1 = off, 2 = on). Render fills it
	// from Vendor and Model.
	CWInverted bool
	// Family is the Grandstream WP family of the model ("8x6", "856",
	// "8xx"), empty for any other phone. Render fills it from Model.
	Family string
	// SCEPCapable is set for a model whose built-in template has SCEP keys
	// (SupportsSCEP) and Wired8021X for one with wired 802.1X keys
	// (SupportsWired8021X). Render fills both from Vendor and Model.
	SCEPCapable bool
	Wired8021X  bool
	// FXO is set for the Grandstream HT813 (FXS port plus an FXO port to
	// the PSTN): the template then writes the FXO disconnect supervision
	// keys. Render fills it from Vendor and Model.
	FXO bool
}

// Line is the SIP account of the phone's bound device.
type Line struct {
	// N is the line number: 1 for the first line, 2 and up for Lines. Render
	// sets it.
	N           int
	Username    string
	AuthName    string
	Password    string // the opened device secret; masked in previews
	DisplayName string // the extension's name
	Label       string // the extension number
	// AccountLabel is what the phone shows for the account: "101 (Office)",
	// or the bare number when the extension has no name. Render fills it
	// from Label and DisplayName; it never feeds SIP auth or caller ID.
	AccountLabel string
	Domain       string // HELLO_SIP_DOMAIN
	// VoicemailCode is the feature code the message key dials (spec S-5);
	// empty when no voicemail feature code is configured.
	VoicemailCode string
	// NoVoicemail is set when the extension has voicemail switched off: its
	// box takes no message, so no transfer-to-voicemail key is provisioned.
	// The zero value (a box) is what the sample and tests assume.
	NoVoicemail bool
	// CallWaiting is the extension's call-waiting flag (spec S-18): the
	// templates write the phone's own call-waiting (and tone) setting from
	// it, so the phone and Hello agree about a second call. False means the
	// phone must not announce one.
	CallWaiting bool
	// SendAnonymous is the extension's phoneSendAnonymous flag: the
	// Grandstream template writes the phone's "Send Anonymous" (P65, account
	// 1) as 1 only when it is set, and writes nothing otherwise.
	SendAnonymous bool
	// ParkCode and ParkRetrieveCode are the call-park and park-retrieve
	// feature codes, empty when none is configured.
	ParkCode, ParkRetrieveCode string
	// PickupGroupCode and PickupDirectedCode are the group-pickup and
	// directed-pickup feature codes (phone-feature-parity S-50), empty when
	// none is configured; the directed one is dialled with the extension after it.
	PickupGroupCode, PickupDirectedCode string
	// BLFListURI is the resource-list URI (RFC 4662) a phone that
	// subscribes to its BLF keys as one list uses: sip:blf-<username>@<domain>.
	// Render fills it when the phone has BLF keys; it is empty otherwise.
	// No built-in template writes it: a vendor key for it is added only once
	// the vendor's documentation names it (phone-feature-parity S-35).
	BLFListURI string
}

// Server is the registrar the phone registers with.
type Server struct {
	Host string
	Port int
	// Transport is "udp" (the default), "tcp" (a model listed in
	// HELLO_PROV_SIP_TCP_MODELS, S-13) or "tls": TLS for a phone whose
	// vendor has a transport key and whose model HELLO_PROV_SIP_TLS_MODELS
	// lists, registering with HELLO_PROV_SIP_TLS_SERVER (S-14).
	Transport string
	// DNSMode is DNSA (the default: the host is resolved by its A record)
	// or DNSSRV (Grandstream only: the host is a name whose SRV records list
	// the servers, and Port is 0). See S-20.
	DNSMode string
	// Backup is the backup registrar of a phone that registers over UDP
	// with a fixed address, nil when none is configured or the phone
	// resolves its servers by SRV (S-20).
	Backup *Backup
	// SRTP is the phone's media-encryption policy ("off", "optional" or
	// "required") on a TLS phone; empty on a phone that registers over UDP,
	// where Hello offers no crypto and the templates leave the phone's SRTP
	// setting alone (phone-feature-parity S-15).
	SRTP string
	// SRTPKeyEx is "sdes" or "dtls" for a phone model that has the setting
	// (GrandstreamDTLSKey), empty for the others and for a phone on UDP; ICE
	// is "ice" or "auto" for a model that has the Media NAT Traversal
	// setting (GrandstreamICEKey), empty otherwise (S-16).
	SRTPKeyEx string
	ICE       string
	Expiry    int // registration expiry, seconds
}

// The DNS modes of Server.DNSMode.
const (
	DNSA   = "a"
	DNSSRV = "srv"
)

// Backup is a backup registrar.
type Backup struct {
	Host string
	Port int
}

// BLFKey is one busy-lamp-field key. Render places the keys of a Yealink,
// Snom or Fanvil phone on the model's keys (see PlaceBLF) and fills the
// placement fields; keys the model has no room for are dropped.
type BLFKey struct {
	Number string // extension number
	Label  string // the extension's name
	URI    string // sip:<number>@<domain>

	// Slot is the vendor's own key number for this key: Yealink linekey.N
	// (or the key inside expansion module Module), Snom fkey idx (an
	// absolute index, expansion keys included), Fanvil Fkey N within its
	// Page. A phone whose vendor or model is unknown gets 1, 2, 3, ... (Snom
	// and Fanvil) or 2, 3, 4, ... (Yealink: key 1 is the account's line).
	Slot int
	// Module is the Yealink expansion module (1 to 3) holding the key; 0 is
	// the phone itself.
	Module int
	// Page and Section are the Fanvil key page (1 up) and its section name
	// ("Dsskey"); PageStart marks the first key of a page, where the
	// template writes the section header. Zero and empty for other vendors.
	Page      int
	Section   string
	PageStart bool
	// Pickup is the directed-pickup code the key sends to pick up the
	// monitored extension's ringing call (Yealink BLF line keys); empty
	// when the PBX has none.
	Pickup string
}

// VMKey is the transfer-to-voicemail key: pressed during a call it transfers
// the caller to the phone's own mailbox by dialling the voicemail feature
// code followed by the extension (*97<ext>, which the PBX takes as "leave a
// message in that box"). Render places it on the first key a Yealink, Snom
// or Fanvil phone has left after its BLF keys (see PlaceVMKey); Poly puts it
// on a softkey, which needs no slot here, and Grandstream has none.
type VMKey struct {
	Number string // the dialled string: the feature code and the extension
	Label  string
	URI    string // sip:<number>@<domain>

	// Slot, Module, Page, Section and PageStart place the key as BLFKey's
	// fields do. Zero for Poly.
	Slot      int
	Module    int
	Page      int
	Section   string
	PageStart bool
}

// ProvInfo is the phone's own provisioning URL and re-check settings.
type ProvInfo struct {
	URL   string // https://…/p/<current token>/; masked in previews
	CAURL string // the plain-HTTP CA certificate URL
	// CACertPEM is the CA certificate itself (one PEM block, trailing
	// newline trimmed; empty when none is configured), for vendors whose
	// CA setting takes the certificate, not a URL (Poly, Grandstream).
	CACertPEM string
	// CACertBase64 is CACertPEM without its BEGIN/END lines: the base64
	// text of the DER certificate, one line, for Snom's <certificate
	// type="base64"> element. Render fills it from CACertPEM.
	CACertBase64  string
	ResyncSeconds int // re-check interval, jitter from the MAC included
	// DSCPSIP and DSCPRTP are the DSCP values (0 to 63) phones mark their
	// SIP and RTP packets with (HELLO_DSCP_SIP, HELLO_DSCP_RTP; spec S-21).
	DSCPSIP int
	DSCPRTP int
	// RTCPXR turns on the phone's RTCP-XR reports to the SIP server (S-36).
	RTCPXR bool
}

// BookInfo is where a phone fetches its address book (spec P-4). The URLs
// carry the phone's token, so previews mask them like Prov.URL.
type BookInfo struct {
	// URL is the merged directory of every book the phone has.
	URL string
	// HostPath is URL without its https:// scheme, for settings that take
	// a server path (Grandstream P331).
	HostPath string
	// Sources are the separate directories for vendors that take several
	// (Yealink): always five, one per book of the phone's set in order
	// (the fifth also holds any further books), unused ones with an empty
	// Name and URL so a template can clear them.
	Sources []BookSource
	// RefreshSeconds and RefreshMinutes are HELLO_PROV_BOOK_REFRESH.
	RefreshSeconds int
	RefreshMinutes int
}

// BookSource is one separate directory of a phone.
type BookSource struct {
	Name string
	URL  string
}

// FirmwareInfo is the firmware pinned for the phone's model.
type FirmwareInfo struct {
	URL     string // the full URL of the pinned file
	Version string
	// Dir is the URL of the directory the file is in, without a trailing
	// slash. Grandstream takes a firmware server directory and appends the
	// model's own file name, so it needs this, not URL.
	Dir string
	// Via is the Grandstream "upgrade via" value for the scheme Dir is
	// served on, see UpgradeVia.
	Via int
	// Window is the upgrade window set on the pin, nil when the phone may
	// upgrade whenever it checks.
	Window *UpgradeWindow
}

// UpgradeWindow is the hour of the day (0 to 23, the phone's clock) a pinned
// firmware is fetched in. Only Grandstream templates act on it.
type UpgradeWindow struct{ Hour int }

// UpgradeVia is the Grandstream "upgrade via" value (P6767) for a URL:
// 2 for HTTPS, 1 for anything else (HTTP).
func UpgradeVia(u string) int {
	if strings.HasPrefix(strings.ToLower(u), "https://") {
		return 2
	}
	return 1
}

// noScheme is a URL without its scheme and trailing slashes:
// "https://h:8/p/x/" gives "h:8/p/x".
func noScheme(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	return strings.TrimRight(u, "/")
}

// TimeInfo is the phone's clock setup.
type TimeInfo struct {
	Zone string // HELLO_PROV_TIMEZONE
	NTP  string // HELLO_PROV_NTP
	TZ   TZInfo // Zone in each vendor's terms, see ZoneInfo
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

// SharingSecretAAD is the context of a phone group's sealed account sharing
// password (P22523).
func SharingSecretAAD(groupID int64) string {
	return "account-sharing:" + strconv.FormatInt(groupID, 10)
}

// PhoneLDAPAAD and PhoneLDAPPrevAAD are the contexts of a phone's sealed
// LDAP password and of the previous one kept after a rotation.
func PhoneLDAPAAD(phoneID int64) string { return "phone-ldap:" + strconv.FormatInt(phoneID, 10) }

// PhoneLDAPPrevAAD is the context of the previous LDAP password.
func PhoneLDAPPrevAAD(phoneID int64) string {
	return "phone-ldap-prev:" + strconv.FormatInt(phoneID, 10)
}

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

// AccountLabel is the account name a phone shows on its screen and line
// keys: the extension number followed by the extension's name in
// parentheses, or the bare number when the name is empty. Control
// characters (newlines would start a new key in the line-based vendor
// formats) become spaces; escaping for XML is the template's job.
func AccountLabel(number, name string) string {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, s)
		return strings.Join(strings.Fields(s), " ")
	}
	number, name = clean(number), clean(name)
	if name == "" || name == number {
		return number
	}
	if number == "" {
		return name
	}
	return number + " (" + name + ")"
}

// E911SyncMinutes is how often a WP8x6 asks Hello for its location again; the
// phone accepts 0 (never) or 30 to 1440.
const E911SyncMinutes = 60

// AlarmNumber is the number a WP8x6 dials for its alarm; Hello answers it
// (spec S-29).
const AlarmNumber = "*55"

// NewE911 builds a phone's E911 data from its provisioning URL (the phone's
// own URL with its token, ending in "/") and its emergency numbers (comma
// separated, as P8385 takes them). nil when the URL is not an https one.
func NewE911(provURL, numbers string) *E911Info {
	rest, ok := strings.CutPrefix(provURL, "https://")
	if !ok || rest == "" {
		return nil
	}
	return &E911Info{
		Server:          strings.TrimSuffix(rest, "/") + "/held",
		IntervalMinutes: E911SyncMinutes,
		Numbers:         strings.ReplaceAll(numbers, ",", ";"),
	}
}
