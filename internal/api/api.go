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

	ListTokens(ctx context.Context, userID int64) ([]store.Token, error)
	CreateToken(ctx context.Context, actor string, userID int64, name string, hash []byte) (store.Token, error)
	DeleteToken(ctx context.Context, actor string, userID, id int64) error

	ListExtensions(ctx context.Context) ([]store.Extension, error)
	GetExtension(ctx context.Context, id int64) (store.Extension, error)
	CreateExtension(ctx context.Context, actor, number, name, externalNumber string, check store.Check) (store.Extension, error)
	UpdateExtension(ctx context.Context, actor string, id int64, c store.ExtensionChange, check store.Check) (store.Extension, error)
	DeleteExtension(ctx context.Context, actor string, id int64, check store.Check) error

	GetVoicemailBox(ctx context.Context, extensionID int64) (store.VoicemailBox, error)
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

	ListCDRs(ctx context.Context, before int64, limit int) ([]store.CDR, string, error)
	GetCDR(ctx context.Context, id int64) (store.CDR, routing.Trace, error)

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
	authed := auth.Middleware(c.Store, c.Log)
	mux := http.NewServeMux()
	public := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, h) }
	private := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, authed(h)) }

	public("GET /api/v1/version", s.version)
	public("GET /api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAPI)
	})
	public("POST /api/v1/auth/login", s.login)
	private("POST /api/v1/auth/logout", s.logout)
	private("GET /api/v1/auth/me", s.me)

	private("GET /api/v1/tokens", s.listTokens)
	private("POST /api/v1/tokens", s.createToken)
	private("DELETE /api/v1/tokens/{id}", s.deleteToken)

	private("GET /api/v1/extensions", s.listExtensions)
	private("POST /api/v1/extensions", s.createExtension)
	private("GET /api/v1/extensions/{id}", s.getExtension)
	private("PATCH /api/v1/extensions/{id}", s.updateExtension)
	private("DELETE /api/v1/extensions/{id}", s.deleteExtension)

	private("GET /api/v1/devices", s.listDevices)
	private("POST /api/v1/devices", s.createDevice)
	private("GET /api/v1/devices/{id}", s.getDevice)
	private("PATCH /api/v1/devices/{id}", s.updateDevice)
	private("DELETE /api/v1/devices/{id}", s.deleteDevice)
	private("POST /api/v1/devices/{id}/rotate-secret", s.rotateSecret)

	private("GET /api/v1/extensions/{id}/voicemail", s.getVoicemailBox)
	private("PUT /api/v1/extensions/{id}/voicemail", s.putVoicemailBox)

	private("GET /api/v1/voicemail/messages", s.listVoicemailMessages)
	private("POST /api/v1/voicemail/messages/{id}/heard", s.markMessageHeard)
	private("DELETE /api/v1/voicemail/messages/{id}", s.deleteMessage)
	private("GET /api/v1/voicemail/messages/{id}/audio", s.messageAudio)

	private("GET /api/v1/ring-groups", s.listRingGroups)
	private("POST /api/v1/ring-groups", s.createRingGroup)
	private("GET /api/v1/ring-groups/{id}", s.getRingGroup)
	private("PATCH /api/v1/ring-groups/{id}", s.updateRingGroup)
	private("DELETE /api/v1/ring-groups/{id}", s.deleteRingGroup)

	private("GET /api/v1/feature-codes", s.listFeatureCodes)
	private("PUT /api/v1/feature-codes", s.putFeatureCodes)

	private("GET /api/v1/presence", s.presence)

	private("GET /api/v1/registrations", s.registrations)
	private("GET /api/v1/calls", s.calls)
	private("GET /api/v1/cdrs", s.cdrs)
	private("GET /api/v1/cdrs/{id}", s.getCDR)

	private("GET /api/v1/trunks", s.listTrunks)
	private("POST /api/v1/trunks", s.createTrunk)
	private("GET /api/v1/trunks/status", s.trunkStatus)
	private("GET /api/v1/trunks/{id}", s.getTrunk)
	private("PATCH /api/v1/trunks/{id}", s.updateTrunk)
	private("DELETE /api/v1/trunks/{id}", s.deleteTrunk)

	private("GET /api/v1/routes/outbound", s.listOutbound)
	private("POST /api/v1/routes/outbound", s.createOutbound)
	private("PUT /api/v1/routes/outbound/order", s.reorder(store.Outbound))
	private("GET /api/v1/routes/outbound/{id}", s.getOutbound)
	private("PATCH /api/v1/routes/outbound/{id}", s.updateOutbound)
	private("DELETE /api/v1/routes/outbound/{id}", s.deleteOutbound)

	private("GET /api/v1/routes/inbound", s.listInbound)
	private("POST /api/v1/routes/inbound", s.createInbound)
	private("PUT /api/v1/routes/inbound/order", s.reorder(store.Inbound))
	private("GET /api/v1/routes/inbound/{id}", s.getInbound)
	private("PATCH /api/v1/routes/inbound/{id}", s.updateInbound)
	private("DELETE /api/v1/routes/inbound/{id}", s.deleteInbound)

	private("POST /api/v1/routing/test", s.routingTest)

	private("GET /api/v1/cluster", s.clusterOverview)
	private("GET /api/v1/cluster/nodes", s.clusterNodes)
	private("POST /api/v1/cluster/nodes/{id}/drain", s.requestDrain)
	private("DELETE /api/v1/cluster/nodes/{id}/drain", s.cancelDrain)
	// Reject cross-origin browser requests that change state (CSRF); a
	// cookie's SameSite=Strict does not cover same-site sibling origins.
	// Clients without Sec-Fetch-Site/Origin headers (curl, SDKs) pass.
	return http.NewCrossOriginProtection().Handler(mux)
}

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
	switch {
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
