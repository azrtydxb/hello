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
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/timecond"
	"github.com/azrtydxb/hello/internal/tts"
	"github.com/azrtydxb/hello/internal/ttscache"
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

	UserForLogin(ctx context.Context, username string) (store.Login, error)
	UpgradePasswordHash(ctx context.Context, id int64, oldHash, newHash string) error
	UserPasswordHash(ctx context.Context, id int64) (string, error)
	ChangeOwnPassword(ctx context.Context, id int64, newHash string, keep []byte) error
	UpdateProfile(ctx context.Context, actor string, id int64, c store.ProfileChange) (store.User, error)
	CreateSession(ctx context.Context, userID int64, hash []byte, expires time.Time) error
	DeleteSession(ctx context.Context, hash []byte) error

	ListUsers(ctx context.Context) ([]store.User, error)
	GetUser(ctx context.Context, id int64) (store.User, error)
	CreateAccount(ctx context.Context, actor string, in store.NewAccount) (store.User, error)
	UpdateAccount(ctx context.Context, actor string, id int64, c store.AccountChange) (store.User, error)
	DeleteUser(ctx context.Context, actor string, id int64) error
	UserExtensions(ctx context.Context, userID int64) ([]store.Extension, error)
	UserExtension(ctx context.Context, userID, id int64) (store.Extension, error)
	UserVoicemailMessage(ctx context.Context, userID, id int64) (store.VoicemailMessage, error)
	UserCDRs(ctx context.Context, userID, before int64, limit int) ([]store.CDR, string, error)
	SetUserRole(ctx context.Context, actor string, id int64, role auth.Role) (store.User, error)
	SetUserExtension(ctx context.Context, actor string, id int64, extension *int64) (store.User, error)

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

	ListPagingGroups(ctx context.Context) ([]store.PagingGroup, error)
	GetPagingGroup(ctx context.Context, id int64) (store.PagingGroup, error)
	CreatePagingGroup(ctx context.Context, actor string, in store.PagingGroupInput, check store.Check) (store.PagingGroup, error)
	UpdatePagingGroup(ctx context.Context, actor string, id int64, in store.PagingGroupInput, check store.Check) (store.PagingGroup, error)
	DeletePagingGroup(ctx context.Context, actor string, id int64, check store.Check) error

	ListIVRMenus(ctx context.Context) ([]store.IVRMenu, error)
	GetIVRMenu(ctx context.Context, id int64) (store.IVRMenu, error)
	CreateIVRMenu(ctx context.Context, actor string, in store.IVRMenuInput, check store.Check) (store.IVRMenu, error)
	UpdateIVRMenu(ctx context.Context, actor string, id int64, in store.IVRMenuInput, check store.Check) (store.IVRMenu, error)
	DeleteIVRMenu(ctx context.Context, actor string, id int64, check store.Check) error
	ListSchedules(ctx context.Context) ([]store.Schedule, error)
	GetSchedule(ctx context.Context, id int64) (store.Schedule, error)
	CreateSchedule(ctx context.Context, actor string, in store.ScheduleInput, check store.Check) (store.Schedule, error)
	UpdateSchedule(ctx context.Context, actor string, id int64, in store.ScheduleInput, check store.Check) (store.Schedule, error)
	DeleteSchedule(ctx context.Context, actor string, id int64, check store.Check) error
	ListCalendars(ctx context.Context) ([]store.Calendar, error)
	GetCalendar(ctx context.Context, id int64) (store.Calendar, error)
	CreateCalendar(ctx context.Context, actor string, in store.CalendarInput, check store.Check) (store.Calendar, error)
	UpdateCalendar(ctx context.Context, actor string, id int64, in store.CalendarInput, check store.Check) (store.Calendar, error)
	ImportCalendarEntries(ctx context.Context, actor string, id int64, entries []timecond.Entry, replace bool, check store.Check) (store.Calendar, error)
	DeleteCalendar(ctx context.Context, actor string, id int64, check store.Check) error
	ListTimeConditions(ctx context.Context) ([]store.TimeCondition, error)
	GetTimeCondition(ctx context.Context, id int64) (store.TimeCondition, error)
	CreateTimeCondition(ctx context.Context, actor string, in store.TimeConditionInput, check store.Check) (store.TimeCondition, error)
	UpdateTimeCondition(ctx context.Context, actor string, id int64, in store.TimeConditionInput, check store.Check) (store.TimeCondition, error)
	DeleteTimeCondition(ctx context.Context, actor string, id int64, check store.Check) error
	SetTimeConditionOverride(ctx context.Context, actor string, id int64, mode string) (store.TimeCondition, error)
	EvaluateTimeCondition(ctx context.Context, id int64, at time.Time, ignoreOverride bool) (store.TimeConditionEvaluation, error)
	ListFlowCanvases(ctx context.Context) ([]store.FlowCanvas, error)
	GetFlowCanvas(ctx context.Context, id int64) (store.FlowCanvas, []store.FlowCanvasNode, error)
	CreateFlowCanvas(ctx context.Context, actor, name, description string) (store.FlowCanvas, error)
	UpdateFlowCanvas(ctx context.Context, actor string, id int64, c store.FlowCanvasChange) (store.FlowCanvas, error)
	DeleteFlowCanvas(ctx context.Context, actor string, id int64) error
	PutFlowLayout(ctx context.Context, actor string, id int64, w store.FlowLayoutWrite) (store.FlowCanvas, []store.FlowCanvasNode, error)
	ListParkLots(ctx context.Context) ([]store.ParkLot, error)
	GetParkLot(ctx context.Context, id int64) (store.ParkLot, error)
	CreateParkLot(ctx context.Context, actor string, in store.ParkLotInput, check store.Check) (store.ParkLot, error)
	UpdateParkLot(ctx context.Context, actor string, id int64, in store.ParkLotInput, check store.Check) (store.ParkLot, error)
	DeleteParkLot(ctx context.Context, actor string, id int64, check store.Check) error

	ListPickupGroups(ctx context.Context) ([]store.PickupGroup, error)
	GetPickupGroup(ctx context.Context, id int64) (store.PickupGroup, error)
	CreatePickupGroup(ctx context.Context, actor string, in store.PickupGroupInput, check store.Check) (store.PickupGroup, error)
	UpdatePickupGroup(ctx context.Context, actor string, id int64, in store.PickupGroupInput, check store.Check) (store.PickupGroup, error)
	DeletePickupGroup(ctx context.Context, actor string, id int64, check store.Check) error
	ListMulticastChannels(ctx context.Context) ([]phonebook.Channel, error)
	GetMulticastChannel(ctx context.Context, id int64) (phonebook.Channel, error)
	CreateMulticastChannel(ctx context.Context, actor string, in phonebook.ChannelInput) (phonebook.Channel, error)
	UpdateMulticastChannel(ctx context.Context, actor string, id int64, in phonebook.ChannelInput) (phonebook.Channel, error)
	DeleteMulticastChannel(ctx context.Context, actor string, id int64) error

	ListQueues(ctx context.Context) ([]store.Queue, error)
	GetQueue(ctx context.Context, id int64) (store.Queue, error)
	CreateQueue(ctx context.Context, actor string, in store.QueueInput, check store.Check) (store.Queue, error)
	UpdateQueue(ctx context.Context, actor string, id int64, in store.QueueInput, check store.Check) (store.Queue, error)
	DeleteQueue(ctx context.Context, actor string, id int64, check store.Check) error
	QueueNameExists(ctx context.Context, name string) (bool, error)

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
	SaveAnnouncementPrompt(ctx context.Context, actor string, id int64, text, voice, model string) (store.Announcement, error)
	CreateGeneratedAnnouncement(ctx context.Context, actor, name, object, text, voice, model string, check store.Check) (store.Announcement, error)
	MarkAnnouncementGenerated(ctx context.Context, actor string, id int64, text, voice, model string) (store.Announcement, error)

	GetTTSSettings(ctx context.Context) (store.TTSSettings, error)
	PutTTSSettings(ctx context.Context, actor string, c store.TTSChange) (store.TTSSettings, error)
	ClearTTSKey(ctx context.Context, actor string) (store.TTSSettings, error)
	TTSKey(ctx context.Context) (string, error)

	ListMOHClasses(ctx context.Context) ([]store.MOHClass, error)
	GetMOHClass(ctx context.Context, id int64) (store.MOHClass, error)
	CreateMOHClass(ctx context.Context, actor string, in store.MOHClassInput) (store.MOHClass, error)
	UpdateMOHClass(ctx context.Context, actor string, id int64, in store.MOHClassInput) (store.MOHClass, error)
	DeleteMOHClass(ctx context.Context, actor string, id int64) error

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

// QueueLive is the part of the call queues' shared state the API reads and
// writes; queue.Store satisfies it.
type QueueLive interface {
	Waiting(ctx context.Context, queue string) ([]queue.Entry, error)
	Agent(ctx context.Context, ext string) (queue.AgentState, bool, error)
	Agents(ctx context.Context) (map[string]queue.AgentState, error)
	SetAgent(ctx context.Context, st queue.AgentState) error
	Stats(ctx context.Context, queue string) (map[string]queue.AgentStats, error)
	AvgHandle(ctx context.Context, queue string) (time.Duration, error)
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
	// Queues is the call queues shared state (waiting callers, agent login
	// and pause) in Valkey; nil answers 503 on the live queue routes.
	Queues QueueLive
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
	// PageTests carries paging-group test pages to hello-sip; nil answers
	// 503. PageTestWait bounds the wait for the result (0 means 20 s).
	PageTests    PageTester
	PageTestWait time.Duration
	// Conferences is the conference room persistence and ConferenceLive the
	// rooms' live state and control queue (spec S-34); nil answers 503.
	Conferences ConferenceStore
	// TwoFactor is the TOTP persistence and TwoFALogins its login counter;
	// nil TwoFactor answers 503 and refuses the login of a 2FA user.
	TwoFactor      TwoFactorStore
	TwoFALogins    *prometheus.CounterVec
	ConferenceLive ConferenceLive
	// Events is the call-event pipeline (stream, history, tickets); nil
	// answers 503 on the event routes.
	Events EventService
	// Parked is the live view of the occupied park slots; nil answers 503.
	Parked ParkedLive
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
	// DefaultTimeZone is the zone a new schedule gets when the request names
	// none: the phones time zone setting (HELLO_PROV_TIMEZONE).
	DefaultTimeZone string
	// SMTPEnv is the deployment's SMTP_* settings, used while no settings
	// are saved in the console; SMTPDeliver replaces the SMTP send in tests.
	SMTPEnv     mailer.Settings
	SMTPDeliver func(ctx context.Context, s mailer.Settings, to string, msg []byte) error
	// ProvStore is the phone provisioning persistence; nil answers 503 on
	// the provisioning routes. Prov wires the rest of them.
	ProvStore ProvStore
	Prov      ProvConfig
	// Phonebooks is the address-book persistence; nil answers 503 on the
	// phonebook and phone-group routes. Dialing is the numbering context the
	// lookup keys of stored numbers are built in.
	Phonebooks PhonebookStore
	Dialing    phonebook.Dialing
	// Blocklist is the call blocklist persistence; nil answers 503 on the
	// blocklist routes.
	Blocklist BlocklistStore
	// Messages is the SIP MESSAGE persistence; nil answers 503 on the
	// message routes.
	Messages MessageStore
	// AI is the OAuth authorization server (spec ai-external-access); nil
	// without HELLO_PUBLIC_URL, when the consent, grant and service-account
	// routes answer 404 and bearer tokens get no audience check.
	AI AIAccess
	// Assistant is the in-product assistant (spec ai-agent S-8); nil while
	// AI is off, when its routes answer 503 ai_disabled.
	Assistant AIAssistant
	// Findings is the AIOps findings store (spec ai-agent); nil is AI off.
	Findings AIFindings
	// Proposals serves the AI proposal routes (spec ai-agent); nil answers
	// 503 ai_disabled.
	Proposals ProposalService
	// TTS generates prompt audio through OpenRouter; nil means the real
	// client. Generation only happens on an operator's explicit request.
	TTS Synthesizer
	// PromptLanguage is the language of the spoken voicemail prompts
	// (HELLO_VOICEMAIL_LANGUAGE; hello-control always sets it): the default
	// greeting a preview speaks and the prompts pre-generated when the TTS
	// settings are saved. Empty is English for a preview and turns the
	// pre-generation off.
	PromptLanguage string
	// AIAgent is the in-product AI agent (spec ai-agent); nil or off makes
	// every AI agent operation but getAIStatus answer 503 ai_disabled.
	AIAgent *ai.Service

	// speech is the voicemail voice: prompts synthesized once per model,
	// voice and text, kept in the voicemail bucket under tts/ (the same
	// cache hello-sip reads), so a preview here warms the call. prewarmMu
	// serializes the background pre-generation of the static prompts. Both
	// are set by Handler.
	speech    *ttscache.Cache
	prewarmMu *sync.Mutex
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
	if c.TTS == nil {
		c.TTS = &tts.Client{}
	}
	c.speech, c.prewarmMu = newSpeechCache(c), &sync.Mutex{}
	s := &server{c}
	opts := auth.Options{Cookies: true}
	if c.AI != nil {
		opts.Resource, _ = c.AI.Resources()
		opts.MetadataURL = c.AI.MetadataURL(opts.Resource)
	}
	authed := auth.Middleware(c.Store, opts, c.Log)
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		h := s.aiGuard(rt.Pattern, rt.H)
		if !rt.Public {
			guarded := auth.Require(rt.Role, rt.Scope)(passwordGate(rt.Pattern, h))
			h = authed(guarded)
			if rt.Pattern == "/api/v1/events/stream" {
				// A client that cannot send an Authorization header
				// redeems a single-use ticket instead.
				h = s.withEventTicket(h, guarded)
			}
		}
		mux.Handle(rt.Method+" "+rt.Pattern, h)
	}
	// Reject cross-origin browser requests that change state (CSRF); a
	// cookie's SameSite=Strict does not cover same-site sibling origins.
	// Clients without Sec-Fetch-Site/Origin headers (curl, SDKs) pass.
	return wrapForTest(http.NewCrossOriginProtection().Handler(mux))
}

// passwordGate refuses every route but the few a user needs to choose their
// new password while an admin-set one is still in force, and, while 2FA is
// required of them and not enrolled, to enrol it (spec pbx-gaps S-12).
func passwordGate(pattern string, next http.Handler) http.Handler {
	switch pattern {
	case "/api/v1/auth/me", "/api/v1/auth/logout", "/api/v1/auth/password":
		return next
	}
	enrol := strings.HasPrefix(pattern, "/api/v1/me/2fa")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := actor(r)
		if a.MustChangePassword {
			writeError(w, http.StatusForbidden, "password_change_required", "choose a new password first")
			return
		}
		if a.MustEnrollMFA && !enrol {
			writeError(w, http.StatusForbidden, "two_factor_enrolment_required", "set up two-factor authentication first")
			return
		}
		next.ServeHTTP(w, r)
	})
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
