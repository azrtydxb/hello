package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// Server conference rooms (phone-feature-parity spec S-34). Reads are open to
// a viewer; creating, changing and deleting a room is admin only because a
// room carries PINs; controlling a live room (mute, kick, lock) is an
// operator's job. The PINs are write-only: only whether each is set is ever
// returned.

// ConferenceStore is the room persistence; *store.Store satisfies it. Nil
// answers 503 on the conference routes.
type ConferenceStore interface {
	ListConferences(ctx context.Context) ([]store.Conference, error)
	GetConference(ctx context.Context, id int64) (store.Conference, error)
	CreateConference(ctx context.Context, actor string, in store.ConferenceInput, userPIN, leaderPIN store.PINChange, check store.Check) (store.Conference, error)
	UpdateConference(ctx context.Context, actor string, id int64, in store.ConferenceInput, userPIN, leaderPIN store.PINChange, check store.Check) (store.Conference, error)
	DeleteConference(ctx context.Context, actor string, id int64, check store.Check) error
}

// ConferenceLive reads the rooms' live state and queues control commands for
// the node that mixes a room; *LazyValkey satisfies it. Nil leaves the rooms
// without a live view and refuses control.
type ConferenceLive interface {
	ConfStates(ctx context.Context, numbers []string) (map[string]livestate.ConfRoomState, error)
	EnqueueConfCommand(ctx context.Context, node string, c livestate.ConfCommand) error
}

var (
	confNumberRe = regexp.MustCompile(`^[0-9]{2,10}$`)
	confPINRe    = regexp.MustCompile(`^[0-9]{4,10}$`)
	confAnnRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// conferenceBody is the create and replace body. A PIN absent from a replace
// is kept; an empty string clears it.
type conferenceBody struct {
	Name              *string `json:"name"`
	Number            *string `json:"number"`
	UserPIN           *string `json:"userPin"`
	LeaderPIN         *string `json:"leaderPin"`
	MaxParticipants   *int    `json:"maxParticipants"`
	WaitForLeader     *bool   `json:"waitForLeader"`
	MuteOnEntry       *bool   `json:"muteOnEntry"`
	EntryExit         *string `json:"entryExit"`
	EntryAnnouncement *string `json:"entryAnnouncement"`
	ExitAnnouncement  *string `json:"exitAnnouncement"`
	Record            *bool   `json:"record"`
	Enabled           *bool   `json:"enabled"`
}

// toInput applies the body over the defaults: ten seats, tones, no waiting,
// no recording, enabled.
func (b conferenceBody) toInput() store.ConferenceInput {
	in := store.ConferenceInput{
		MaxParticipants: store.DefaultConferenceCap, EntryExit: store.ConferenceSoundTone, Enabled: true,
	}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Number != nil {
		in.Number = *b.Number
	}
	if b.MaxParticipants != nil {
		in.MaxParticipants = *b.MaxParticipants
	}
	if b.WaitForLeader != nil {
		in.WaitForLeader = *b.WaitForLeader
	}
	if b.MuteOnEntry != nil {
		in.MuteOnEntry = *b.MuteOnEntry
	}
	if b.EntryExit != nil {
		in.EntryExit = *b.EntryExit
	}
	if b.EntryAnnouncement != nil {
		in.EntryAnnouncement = *b.EntryAnnouncement
	}
	if b.ExitAnnouncement != nil {
		in.ExitAnnouncement = *b.ExitAnnouncement
	}
	if b.Record != nil {
		in.Record = *b.Record
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	return in
}

func pinChange(p *string) store.PINChange {
	if p == nil {
		return store.PINChange{}
	}
	return store.PINChange{Set: true, Value: *p}
}

func validRoomName(s string) bool {
	if s == "" || s != strings.TrimSpace(s) || len([]rune(s)) > 64 {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// validateConference checks a full input (stage 1) and the PINs being set.
func validateConference(in *store.ConferenceInput, user, leader store.PINChange) fieldErrs {
	var f fieldErrs
	if !validRoomName(in.Name) {
		f.add("name", "must be 1-64 printable characters without surrounding space")
	}
	if !confNumberRe.MatchString(in.Number) {
		f.add("number", "must be 2-10 digits")
	}
	if in.MaxParticipants < store.MinConferenceCap || in.MaxParticipants > store.MaxConferenceCap {
		f.add("maxParticipants", "must be %d to %d", store.MinConferenceCap, store.MaxConferenceCap)
	}
	switch in.EntryExit {
	case store.ConferenceSoundNone, store.ConferenceSoundTone:
	case store.ConferenceSoundAnnouncement:
		if in.EntryAnnouncement == "" && in.ExitAnnouncement == "" {
			f.add("entryAnnouncement", "name an announcement, or use tones")
		}
	default:
		f.add("entryExit", `must be "none", "tone" or "announcement"`)
	}
	for _, a := range []struct{ path, v string }{{"entryAnnouncement", in.EntryAnnouncement}, {"exitAnnouncement", in.ExitAnnouncement}} {
		if a.v != "" && !confAnnRe.MatchString(a.v) {
			f.add(a.path, "must be an announcement name (1-64 of A-Z a-z 0-9 . _ -)")
		}
	}
	for _, p := range []struct {
		path string
		ch   store.PINChange
	}{{"userPin", user}, {"leaderPin", leader}} {
		if p.ch.Set && p.ch.Value != "" && !confPINRe.MatchString(p.ch.Value) {
			f.add(p.path, "must be 4-10 digits, or empty to clear it")
		}
	}
	if user.Set && leader.Set && user.Value != "" && user.Value == leader.Value {
		f.add("leaderPin", "must differ from the user PIN")
	}
	return f
}

// conferenceView is a room with its live state, when it has one.
type conferenceView struct {
	store.Conference
	Live *livestate.ConfRoomState `json:"live"`
}

func (s *server) conferencesDisabled(w http.ResponseWriter) bool {
	if s.Conferences == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "conference rooms are not configured")
		return true
	}
	return false
}

// liveStates reads the live state of the given rooms; an unreachable Valkey
// leaves the list without live data rather than failing it.
func (s *server) liveStates(ctx context.Context, rooms []store.Conference) map[string]livestate.ConfRoomState {
	if s.ConferenceLive == nil || len(rooms) == 0 {
		return nil
	}
	numbers := make([]string, len(rooms))
	for i, r := range rooms {
		numbers[i] = r.Number
	}
	lctx, cancel := context.WithTimeout(ctx, liveTimeout)
	defer cancel()
	st, err := s.ConferenceLive.ConfStates(lctx, numbers)
	if err != nil {
		s.Log.Warn("conference live state unavailable", "error", err)
		return nil
	}
	return st
}

func (s *server) view(ctx context.Context, rooms []store.Conference) []conferenceView {
	live := s.liveStates(ctx, rooms)
	out := make([]conferenceView, len(rooms))
	for i, r := range rooms {
		out[i] = conferenceView{Conference: r}
		if st, ok := live[r.Number]; ok {
			st := st
			out[i].Live = &st
		}
	}
	return out
}

func (s *server) listConferences(w http.ResponseWriter, r *http.Request) {
	if s.conferencesDisabled(w) {
		return
	}
	rooms, err := s.Conferences.ListConferences(r.Context())
	if err != nil {
		s.internal(w, "list conferences", err)
		return
	}
	writeJSON(w, http.StatusOK, items(s.view(r.Context(), rooms)))
}

func (s *server) getConference(w http.ResponseWriter, r *http.Request) {
	if s.conferencesDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Conferences.GetConference(r.Context(), id)
	if err != nil {
		s.configError(w, "conference room", err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), []store.Conference{c})[0])
}

func (s *server) createConference(w http.ResponseWriter, r *http.Request) {
	if s.conferencesDisabled(w) {
		return
	}
	var b conferenceBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Number == nil {
		badRequest(w, "name and number are required")
		return
	}
	in, user, leader := b.toInput(), pinChange(b.UserPIN), pinChange(b.LeaderPIN)
	if f := validateConference(&in, user, leader); len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := s.Conferences.CreateConference(r.Context(), actor(r).String(), in, user, leader, s.check())
	if err != nil {
		s.configError(w, "conference room", err)
		return
	}
	writeJSON(w, http.StatusCreated, conferenceView{Conference: c})
}

// putConference replaces a room as a whole; a PIN that is absent is kept.
func (s *server) putConference(w http.ResponseWriter, r *http.Request) {
	if s.conferencesDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b conferenceBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Number == nil {
		badRequest(w, "name and number are required")
		return
	}
	in, user, leader := b.toInput(), pinChange(b.UserPIN), pinChange(b.LeaderPIN)
	if f := validateConference(&in, user, leader); len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := s.Conferences.UpdateConference(r.Context(), actor(r).String(), id, in, user, leader, s.check())
	if err != nil {
		s.configError(w, "conference room", err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), []store.Conference{c})[0])
}

func (s *server) deleteConference(w http.ResponseWriter, r *http.Request) {
	if s.conferencesDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Conferences.DeleteConference(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "conference room", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type conferenceControlBody struct {
	Action      string `json:"action"`
	Participant string `json:"participant"`
}

// Control actions an operator may request of a live room.
var conferenceActions = []string{"mute", "unmute", "kick", "lock", "unlock", "mute_all", "unmute_all", "end"}

// controlConference is POST /api/v1/conferences/{id}/control: it queues a
// control command for the node that mixes the room. The room's owner applies
// it within about half a second; the live view shows the effect.
func (s *server) controlConference(w http.ResponseWriter, r *http.Request) {
	if s.conferencesDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b conferenceControlBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	if !slices.Contains(conferenceActions, b.Action) {
		f.add("action", "must be one of %s", strings.Join(conferenceActions, ", "))
	}
	needsTarget := b.Action == "mute" || b.Action == "unmute" || b.Action == "kick"
	if needsTarget && b.Participant == "" {
		f.add("participant", "is required for %s", b.Action)
	}
	if !needsTarget && b.Participant != "" {
		f.add("participant", "applies to mute, unmute and kick only")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	room, err := s.Conferences.GetConference(r.Context(), id)
	if err != nil {
		s.configError(w, "conference room", err)
		return
	}
	if s.ConferenceLive == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live conference control needs Valkey and a running hello-sip node")
		return
	}
	lctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	states, err := s.ConferenceLive.ConfStates(lctx, []string{room.Number})
	if err != nil {
		s.Log.Warn("conference live state unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state is unavailable")
		return
	}
	st, live := states[room.Number]
	if !live {
		writeError(w, http.StatusConflict, "conflict", "the room is empty")
		return
	}
	if needsTarget && !slices.ContainsFunc(st.Participants, func(p livestate.ConfParticipant) bool { return p.ID == b.Participant }) {
		writeError(w, http.StatusConflict, "conflict", "that participant is no longer in the room")
		return
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.internal(w, "conference command id", err)
		return
	}
	who := actor(r).String()
	cmd := livestate.ConfCommand{
		ID: hex.EncodeToString(raw[:]), Room: room.Number, Action: b.Action, Participant: b.Participant,
		Actor: who, At: time.Now(),
	}
	if err := s.ConferenceLive.EnqueueConfCommand(lctx, st.Node, cmd); err != nil {
		s.Log.Warn("queue conference command", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state is unavailable")
		return
	}
	// The audit event is written here, where the actor made the request; the
	// node that applies the command does not repeat it.
	what := "conference." + b.Action
	if b.Participant != "" {
		what += " " + b.Participant
	}
	if err := s.Store.Audit(r.Context(), who, what, "conference_room", strconv.FormatInt(room.ID, 10)); err != nil {
		s.Log.Warn("audit conference control", "error", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "node": st.Node, "commandId": cmd.ID})
}
