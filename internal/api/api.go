// Package api is hello-control's versioned management API (spec §24).
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/version"
)

//go:embed openapi.json
var openAPI []byte

// maxBody bounds every JSON request body.
const maxBody = 64 << 10

// liveTimeout bounds one read of the live state in Valkey.
const liveTimeout = 2 * time.Second

// Store is the persistence the API needs; *store.Store implements it.
type Store interface {
	auth.Lookup
	ConfigRevision(ctx context.Context) (int64, error)
	Audit(ctx context.Context, actor, action, resource, resourceID string) error

	UserByName(ctx context.Context, username string) (int64, string, error)
	CreateSession(ctx context.Context, userID int64, hash []byte, expires time.Time) error
	DeleteSession(ctx context.Context, hash []byte) error

	ListUsers(ctx context.Context) ([]store.User, error)
	SetUserRole(ctx context.Context, actor string, id int64, role auth.Role) (store.User, error)

	ListTokens(ctx context.Context, userID int64) ([]store.Token, error)
	CreateToken(ctx context.Context, actor string, userID int64, in store.NewToken, hash []byte) (store.Token, error)
	DeleteToken(ctx context.Context, actor string, userID, id int64) error

	ListExtensions(ctx context.Context) ([]store.Extension, error)
	GetExtension(ctx context.Context, id int64) (store.Extension, error)
	CreateExtension(ctx context.Context, actor, number, name, externalNumber string, check store.Check) (store.Extension, error)
	UpdateExtension(ctx context.Context, actor string, id int64, c store.ExtensionChange, check store.Check) (store.Extension, error)
	DeleteExtension(ctx context.Context, actor string, id int64, check store.Check) error

	GetVoicemailBox(ctx context.Context, extensionID int64) (store.VoicemailBox, error)
	ListVoicemailBoxes(ctx context.Context) ([]store.VoicemailBoxSummary, error)
	UpdateVoicemailBox(ctx context.Context, actor string, extensionID int64, c store.VoicemailBoxChange, check store.Check) (store.VoicemailBox, error)
	ListVoicemailMessages(ctx context.Context, boxID int64, unheardOnly bool) ([]store.VoicemailMessage, error)
	GetVoicemailMessage(ctx context.Context, id int64) (store.VoicemailMessage, error)
	UpdateVoicemailMessageHeard(ctx context.Context, actor string, id int64, heard bool) error
	DeleteVoicemailMessage(ctx context.Context, actor string, id int64) (string, error)

	ListRingGroups(ctx context.Context) ([]store.RingGroup, error)
	GetRingGroup(ctx context.Context, id int64) (store.RingGroup, error)
	CreateRingGroup(ctx context.Context, actor string, in store.RingGroupInput, check store.Check) (store.RingGroup, error)
	UpdateRingGroup(ctx context.Context, actor string, id int64, in store.RingGroupInput, check store.Check) (store.RingGroup, error)
	DeleteRingGroup(ctx context.Context, actor string, id int64, check store.Check) error

	ListFeatureCodes(ctx context.Context) ([]store.FeatureCode, error)
	PutFeatureCodes(ctx context.Context, actor string, codes []store.FeatureCode, check store.Check) error

	ListDevices(ctx context.Context) ([]store.Device, error)
	GetDevice(ctx context.Context, id int64) (store.Device, error)
	CreateDevice(ctx context.Context, actor string, in store.NewDevice) (store.Device, error)
	UpdateDevice(ctx context.Context, actor string, id int64, enabled *bool, extensionID *int64) (store.Device, error)
	RotateDeviceSecret(ctx context.Context, actor string, id int64, realm, secret string) (store.Device, error)
	DeleteDevice(ctx context.Context, actor string, id int64) error

	ListCDRs(ctx context.Context, f store.CDRFilter, before int64, limit int) ([]store.CDR, string, error)
	CountCDRs(ctx context.Context) (store.CDRCounts, error)
	CDRConcurrency(ctx context.Context, from, to time.Time, step time.Duration) ([]store.ConcurrencyPoint, error)
	GetCDR(ctx context.Context, id int64) (store.CDR, routing.Trace, error)

	ListRecordings(ctx context.Context, extension string, before int64, limit int) ([]store.Recording, string, error)
	GetRecording(ctx context.Context, id int64) (store.Recording, error)
	DeleteRecording(ctx context.Context, actor string, id int64) (string, error)

	ListAnnouncements(ctx context.Context) ([]store.Announcement, error)
	GetAnnouncement(ctx context.Context, id int64) (store.Announcement, error)
	AnnouncementByName(ctx context.Context, name string) (store.Announcement, bool, error)
	CreateAnnouncement(ctx context.Context, actor, name, object string, check store.Check) (store.Announcement, error)
	ReplaceAnnouncement(ctx context.Context, actor string, id int64) (store.Announcement, error)
	DeleteAnnouncement(ctx context.Context, actor string, id int64, check store.Check) (string, error)

	ListTrunks(ctx context.Context) ([]store.Trunk, error)
	GetTrunk(ctx context.Context, id int64) (store.Trunk, error)
	CreateTrunk(ctx context.Context, actor string, in store.TrunkInput, password string, check store.Check) (store.Trunk, error)
	UpdateTrunk(ctx context.Context, actor string, id int64, pw store.PasswordChange,
		apply func(*store.TrunkInput, bool) []routing.FieldError, check store.Check) (store.Trunk, error)
	DeleteTrunk(ctx context.Context, actor string, id int64, check store.Check) error

	ListOutboundRoutes(ctx context.Context) ([]store.OutboundRoute, error)
	GetOutboundRoute(ctx context.Context, id int64) (store.OutboundRoute, error)
	CreateOutboundRoute(ctx context.Context, actor string, o store.OutboundRoute, check store.Check) (store.OutboundRoute, error)
	UpdateOutboundRoute(ctx context.Context, actor string, id int64,
		apply func(*store.OutboundRoute) []routing.FieldError, check store.Check) (store.OutboundRoute, error)
	DeleteOutboundRoute(ctx context.Context, actor string, id int64, check store.Check) error

	ListInboundRoutes(ctx context.Context) ([]store.InboundRoute, error)
	GetInboundRoute(ctx context.Context, id int64) (store.InboundRoute, error)
	CreateInboundRoute(ctx context.Context, actor string, in store.InboundRoute, check store.Check) (store.InboundRoute, error)
	UpdateInboundRoute(ctx context.Context, actor string, id int64,
		apply func(*store.InboundRoute) []routing.FieldError, check store.Check) (store.InboundRoute, error)
	DeleteInboundRoute(ctx context.Context, actor string, id int64, check store.Check) error

	ReorderRoutes(ctx context.Context, actor, kind string, ids []int64, check store.Check) error
	RoutingConfig(ctx context.Context) (store.RoutingSnapshot, error)
}

// Live is the shared live state; *livestate.Store implements it.
type Live interface {
	AllBindings(ctx context.Context) ([]livestate.Binding, error)
	Calls(ctx context.Context) ([]livestate.Call, error)
	DeviceStates(ctx context.Context) ([]livestate.DeviceState, error)
}

// Config wires the API to its dependencies.
type Config struct {
	Store Store
	Live  Live
	// SIPDomain is the realm device HA1 values are computed for.
	SIPDomain  string
	SessionTTL time.Duration
	Log        *slog.Logger
	// Router is the routing engine; nil means routing.Compile.
	Router Router
	// Trunks reads trunk live state for /trunks/status and the route
	// tester; nil behaves as Valkey unreachable.
	Trunks TrunkLive
	// Cluster is node membership and drain requests; Valkey reports Valkey
	// health. nil behaves as Valkey unreachable.
	Cluster ClusterStore
	Valkey  ValkeyStatus
	// Objects is the voicemail audio store; nil makes the audio routes and
	// greeting uploads answer 503 instead of touching MinIO.
	Objects Objects
	// Diagnostics reads REGISTER attempts and the failed-auth throttle for
	// the Diagnostics view; nil answers 503. AuthFailLimit is hello-sip's
	// HELLO_SIP_AUTH_FAIL_LIMIT (0 means its default, 10).
	Diagnostics   DiagnosticsLive
	AuthFailLimit int
	// EmailDelivery is whether voicemail-to-email can send (SMTP_HOST is
	// set); the voicemail box responses carry it so the console can say
	// email is not configured instead of showing messages stuck pending.
	EmailDelivery bool
	// ProvStore is the phone provisioning persistence; nil answers 503 on
	// the provisioning routes. Prov wires the rest of them.
	ProvStore ProvStore
	Prov      ProvConfig
	// AI is the OAuth authorization server (spec ai-external-access); nil
	// without HELLO_PUBLIC_URL, when the consent, grant and service-account
	// routes answer 404 and bearer tokens get no audience check.
	AI AIAccess
	// Findings is the AIOps findings store (spec ai-agent); nil is AI off.
	Findings AIFindings
}

type server struct{ Config }

// Handler returns the /api/v1 routes. Every route except version, openapi
// and login requires a session cookie or bearer token.
func Handler(c Config) http.Handler {
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Router == nil {
		c.Router = engineRouter{}
	}
	s := &server{c}
	opts := auth.Options{Cookies: true}
	if c.AI != nil {
		opts.Resource, _ = c.AI.Resources()
		opts.MetadataURL = c.AI.MetadataURL(opts.Resource)
	}
	authed := auth.Middleware(c.Store, opts, c.Log)
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		h := http.Handler(rt.H)
		if !rt.Public {
			h = authed(auth.Require(rt.Role, rt.Scope)(h))
		}
		mux.Handle(rt.Method+" "+rt.Pattern, h)
	}
	// Reject cross-origin browser requests that change state (CSRF); a
	// cookie's SameSite=Strict does not cover same-site sibling origins.
	// Clients without Sec-Fetch-Site/Origin headers (curl, SDKs) pass.
	return wrapForTest(http.NewCrossOriginProtection().Handler(mux))
}

// wrapForTest is the identity; the package tests replace it from TestMain
// with the OpenAPI conformance validator (spec ai-external-access S-2).
var wrapForTest = func(h http.Handler) http.Handler { return h }

func (s *server) version(w http.ResponseWriter, r *http.Request) {
	rev, err := s.Store.ConfigRevision(r.Context())
	if err != nil {
		s.internal(w, "read config revision", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        version.Version,
		"commit":         version.Commit,
		"configRevision": rev,
	})
}

type list[T any] struct {
	Items []T `json:"items"`
}

func items[T any](v []T) list[T] {
	if v == nil {
		v = []T{}
	}
	return list[T]{Items: v}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var writeError = auth.WriteError

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "bad_request", msg)
}

func (s *server) internal(w http.ResponseWriter, what string, err error) {
	s.Log.Error(what, "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

// storeError answers a store failure: 404, 409 or 500.
func (s *server) storeError(w http.ResponseWriter, what string, err error) {
	var used *store.InUseError
	switch {
	case errors.As(err, &used):
		writeError(w, http.StatusConflict, "conflict", used.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", what+": already exists")
	default:
		s.internal(w, what, err)
	}
}

// decode reads one JSON object into v, rejecting unknown fields, trailing
// data and bodies over maxBody. It answers 400 itself and reports false.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			badRequest(w, "request body too large")
		} else {
			badRequest(w, "invalid JSON body: "+jsonProblem(err))
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body: trailing data")
		return false
	}
	return true
}

// jsonProblem describes a decode error without echoing request values.
func jsonProblem(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return "syntax error at offset " + strconv.FormatInt(syn.Offset, 10)
	case errors.As(err, &typ):
		return "wrong type for field " + strconv.Quote(typ.Field)
	case errors.Is(err, io.EOF):
		return "empty body"
	}
	if msg := err.Error(); len(msg) < 200 {
		return msg // e.g. json: unknown field "x"
	}
	return "malformed"
}

// pathID parses the {id} path value; anything but a positive integer is 404.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return 0, false
	}
	return id, true
}

func actor(r *http.Request) auth.Actor {
	a, _ := auth.ActorFrom(r.Context())
	return a
}
