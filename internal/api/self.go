package api

// Extension self-service (role user): the /me routes. Identity comes from the
// authenticated actor and nothing else. No route takes a user id, and an
// extension or message id in a path is resolved only through the caller's
// user_extensions rows, so an id that belongs to someone else answers 404
// exactly like one that does not exist: guessing ids reveals nothing and
// reaches nothing. A credential that is not a user (a service account) is
// refused. Everything these routes return is a slim view that leaves out
// SIP credentials, devices, trunks and system configuration.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// selfUser returns the caller's user id, answering 403 for a credential that
// is not a user.
func (s *server) selfUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	a := actor(r)
	if a.Kind == auth.KindService || a.UserID <= 0 {
		writeError(w, http.StatusForbidden, "no_user", "this belongs to a user; this credential has none")
		return 0, false
	}
	return a.UserID, true
}

// selfExtension resolves the {id} path value to one of the caller's own
// extensions; anything else is 404.
func (s *server) selfExtension(w http.ResponseWriter, r *http.Request) (store.Extension, bool) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return store.Extension{}, false
	}
	id, ok := pathID(w, r)
	if !ok {
		return store.Extension{}, false
	}
	e, err := s.Store.UserExtension(r.Context(), uid, id)
	if err != nil {
		s.storeError(w, "extension", err)
		return store.Extension{}, false
	}
	return e, true
}

// selfMessage resolves the {id} path value to a message in one of the
// caller's own boxes; anything else is 404.
func (s *server) selfMessage(w http.ResponseWriter, r *http.Request) (store.VoicemailMessage, bool) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return store.VoicemailMessage{}, false
	}
	id, ok := pathID(w, r)
	if !ok {
		return store.VoicemailMessage{}, false
	}
	m, err := s.Store.UserVoicemailMessage(r.Context(), uid, id)
	if err != nil {
		s.storeError(w, "voicemail message", err)
		return store.VoicemailMessage{}, false
	}
	return m, true
}

// Profile.

func (s *server) updateProfile(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	var in struct {
		DisplayName    *string    `json:"displayName"`
		Email          *string    `json:"email"`
		VoicemailEmail *vmPrefsIn `json:"voicemailEmail"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.DisplayName == nil && in.Email == nil && in.VoicemailEmail == nil {
		badRequest(w, "a field to change is required")
		return
	}
	var (
		f fieldErrs
		c store.ProfileChange
	)
	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if name != "" {
			var ok bool
			if name, ok = validName(name); !ok {
				f.add("displayName", "must be 1-100 printable characters")
			}
		}
		c.DisplayName = &name
	}
	if in.Email != nil {
		validEmail(&f, "email", *in.Email)
		c.Email = in.Email
	}
	in.VoicemailEmail.validate(&f, "voicemailEmail")
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	if in.VoicemailEmail != nil {
		cur, err := s.Store.GetUser(r.Context(), uid)
		if err != nil {
			s.storeError(w, "user", err)
			return
		}
		p := in.VoicemailEmail.merge(cur.VoicemailEmail)
		c.Voicemail = &p
	}
	if _, err := s.Store.UpdateProfile(r.Context(), actor(r).String(), uid, c); err != nil {
		s.storeError(w, "user", err)
		return
	}
	s.me(w, r)
}

// Extensions.

// selfExtensionView is an extension as its own user sees it: the label and
// the call features they may change, nothing else.
type selfExtensionView struct {
	ID               int64  `json:"id"`
	Number           string `json:"number"`
	Name             string `json:"name"`
	DND              bool   `json:"dnd"`
	ForwardAlways    string `json:"forwardAlways"`
	ForwardBusy      string `json:"forwardBusy"`
	ForwardNoAnswer  string `json:"forwardNoAnswer"`
	VoicemailEnabled bool   `json:"voicemailEnabled"`
}

func selfView(e store.Extension) selfExtensionView {
	return selfExtensionView{ID: e.ID, Number: e.Number, Name: e.Name, DND: e.DND, ForwardAlways: e.ForwardAlways,
		ForwardBusy: e.ForwardBusy, ForwardNoAnswer: e.ForwardNoAnswer, VoicemailEnabled: e.VoicemailEnabled}
}

func (s *server) listMyExtensions(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	es, err := s.Store.UserExtensions(r.Context(), uid)
	if err != nil {
		s.internal(w, "list own extensions", err)
		return
	}
	out := make([]selfExtensionView, len(es))
	for i, e := range es {
		out[i] = selfView(e)
	}
	writeJSON(w, http.StatusOK, items(out))
}

// updateMyExtension changes the label and the DND and forwarding of one of
// the caller's extensions. Forwarding to an external number follows the
// outbound routes the administrator configured, as a call from the extension
// would.
func (s *server) updateMyExtension(w http.ResponseWriter, r *http.Request) {
	e, ok := s.selfExtension(w, r)
	if !ok {
		return
	}
	var in struct {
		Name            *string `json:"name"`
		DND             *bool   `json:"dnd"`
		ForwardAlways   *string `json:"forwardAlways"`
		ForwardBusy     *string `json:"forwardBusy"`
		ForwardNoAnswer *string `json:"forwardNoAnswer"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil && in.DND == nil && in.ForwardAlways == nil && in.ForwardBusy == nil && in.ForwardNoAnswer == nil {
		badRequest(w, "a field to change is required")
		return
	}
	if f := validateExtension(nil, in.Name, nil); len(f) > 0 {
		writeFields(w, f)
		return
	}
	var f fieldErrs
	for path, v := range map[string]*string{
		"forwardAlways": in.ForwardAlways, "forwardBusy": in.ForwardBusy, "forwardNoAnswer": in.ForwardNoAnswer,
	} {
		if v != nil {
			validateForwardTarget(&f, path, *v)
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	upd, err := s.Store.UpdateExtension(r.Context(), actor(r).String(), e.ID, store.ExtensionChange{
		Name: in.Name, DND: in.DND, ForwardAlways: in.ForwardAlways, ForwardBusy: in.ForwardBusy, ForwardNoAnswer: in.ForwardNoAnswer,
	}, s.check())
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusOK, selfView(upd))
}

// Voicemail.

// selfBoxView is a voicemail box as its owner sees it.
type selfBoxView struct {
	ExtensionID    int64 `json:"extensionId"`
	BoxID          int64 `json:"boxId"`
	HasPIN         bool  `json:"hasPin"`
	HasGreeting    bool  `json:"hasGreeting"`
	HasUnreachable bool  `json:"hasUnreachableGreeting"`
}

func selfBox(e store.Extension, b store.VoicemailBox, found bool) selfBoxView {
	v := selfBoxView{ExtensionID: e.ID}
	if found {
		v.BoxID, v.HasPIN = b.ID, b.HasPassword
		v.HasGreeting, v.HasUnreachable = b.GreetingObject != "", b.UnreachableObject != ""
	}
	return v
}

func (s *server) getMyVoicemail(w http.ResponseWriter, r *http.Request) {
	e, ok := s.selfExtension(w, r)
	if !ok {
		return
	}
	b, err := s.Store.GetVoicemailBox(r.Context(), e.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, "own voicemail box", err)
		return
	}
	writeJSON(w, http.StatusOK, selfBox(e, b, err == nil))
}

// putMyVoicemail changes the PIN and uploads greetings of one's own box.
func (s *server) putMyVoicemail(w http.ResponseWriter, r *http.Request) {
	e, ok := s.selfExtension(w, r)
	if !ok {
		return
	}
	s.saveVoicemailBox(w, r, e.ID, true)
}

func (s *server) listMyMessages(w http.ResponseWriter, r *http.Request) {
	e, ok := s.selfExtension(w, r)
	if !ok {
		return
	}
	unheard := false
	if v := r.URL.Query().Get("unheard"); v != "" {
		var err error
		if unheard, err = strconv.ParseBool(v); err != nil {
			badRequest(w, "unheard must be true or false")
			return
		}
	}
	b, err := s.Store.GetVoicemailBox(r.Context(), e.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, items([]store.VoicemailMessage{}))
		return
	}
	if err != nil {
		s.internal(w, "own voicemail box", err)
		return
	}
	ms, err := s.Store.ListVoicemailMessages(r.Context(), b.ID, unheard)
	if err != nil {
		s.internal(w, "list own voicemail messages", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ms))
}

func (s *server) markMyMessageHeard(w http.ResponseWriter, r *http.Request) {
	m, ok := s.selfMessage(w, r)
	if !ok {
		return
	}
	s.setHeard(w, r, m.ID)
}

func (s *server) deleteMyMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.selfMessage(w, r)
	if !ok {
		return
	}
	s.removeMessage(w, r, m.ID)
}

// myMessageAudio plays a message, or with ?download=1 saves it.
func (s *server) myMessageAudio(w http.ResponseWriter, r *http.Request) {
	m, ok := s.selfMessage(w, r)
	if !ok {
		return
	}
	download := ""
	if r.URL.Query().Get("download") == "1" {
		download = "voicemail-" + strconv.FormatInt(m.ID, 10) + ".wav"
	}
	s.playMessage(w, r, m.ID, download)
}

// Call history.

// selfCall is one call of the caller's own history: no routes, trunks or
// signalling detail.
type selfCall struct {
	ID          int64  `json:"id"`
	StartTime   string `json:"startTime"`
	DurationMs  int64  `json:"durationMs"`
	Direction   string `json:"direction"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Answered    bool   `json:"answered"`
}

func (s *server) listMyCalls(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	limit, before, ok := pageParams(w, r)
	if !ok {
		return
	}
	cs, next, err := s.Store.UserCDRs(r.Context(), uid, before, limit)
	if err != nil {
		s.internal(w, "list own calls", err)
		return
	}
	out := make([]selfCall, len(cs))
	for i, c := range cs {
		out[i] = selfCall{ID: c.ID, StartTime: c.StartTime.UTC().Format("2006-01-02T15:04:05Z07:00"), DurationMs: c.DurationMs,
			Direction: c.Direction, Kind: c.Kind, Source: c.Source, Destination: c.Destination, Answered: c.AnswerTime != nil}
	}
	var n int64
	if next != "" {
		n, _ = strconv.ParseInt(next, 10, 64)
	}
	writePage(w, out, n)
}
