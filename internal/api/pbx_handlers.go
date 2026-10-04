package api

// Phase 4 PBX feature handlers (spec contract 7): extension call features,
// voicemail boxes/messages, ring groups, feature codes and presence.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// Phase 4 field validation, in the same shape as the Phase 2 route
// validation: stage 1 here, stage 2 (the whole-configuration compile) in the
// store transaction.

var (
	// featureCodeRe is the shape of a stored feature code. ## is the
	// attended-transfer prompt; the migration's CHECK constraint only
	// accepts '*' plus 2-4 digits, or '##'.
	featureCodeRe = regexp.MustCompile(`^(\*[0-9]{2,4}|##)$`)
	// forwardRe is a forwarding target: an extension or external number.
	forwardRe = regexp.MustCompile(`^(\+?[0-9*#]{2,32})?$`)
	emailRe   = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,255}\.[A-Za-z]{2,}$`)
)

// Group strategies and feature-code actions (migration 00004's CHECKs,
// widened by 00005 to accept the announcement destination and action).
var (
	strategies = []string{"ring-all", "sequential", "round-robin", "longest-idle", "weighted"}
	fcActions  = []string{"forward_always", "forward_busy", "forward_no_answer", "dnd_on", "dnd_off",
		"voicemail", "blind_transfer", "attended_transfer", "announcement"}
)

// voicemailLimits bound greeting uploads.
const (
	maxGreetingBytes = 5 << 20 // 5 MiB of PCM WAV
	maxUploadBytes   = 7 << 20 // total multipart body
)

// Extensions: call features (contract 7).

// validateForwardTarget checks a forwardAlways/Busy/NoAnswer value: empty is
// off, otherwise an extension or external number.
func validateForwardTarget(f *fieldErrs, path, v string) {
	if !forwardRe.MatchString(v) {
		f.add(path, "must be empty or 2-32 of 0-9 * # with an optional leading +")
	}
}

// Voicemail box.

func (s *server) getVoicemailBox(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := s.Store.GetVoicemailBox(r.Context(), id)
	if err != nil {
		s.configError(w, "voicemail box", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// putVoicemailBox is PUT /api/v1/extensions/{id}/voicemail: JSON with
// password/email, or multipart with the same fields plus greeting and
// unreachable WAV files.
func (s *server) putVoicemailBox(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var (
		change   store.VoicemailBoxChange
		greeting []byte
		unreach  []byte
	)
	switch ct := r.Header.Get("Content-Type"); {
	case strings.HasPrefix(ct, "multipart/"):
		// Streamed (MultipartReader, no spooling to memory or disk) and the
		// body is bounded first, so an oversized upload cannot exhaust it.
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := readVoicemailUpload(r, &change, &greeting, &unreach); err != nil {
			badRequest(w, err.Error())
			return
		}
	default:
		var in struct {
			Password *string `json:"password"`
			Email    *string `json:"email"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Password != nil && *in.Password != "" {
			change.Password = in.Password
		}
		change.Email = in.Email
	}
	var f fieldErrs
	if change.Password != nil && (len(*change.Password) < 4 || len(*change.Password) > 72) {
		f.add("password", "must be 4-72 characters")
	}
	if change.Email != nil && *change.Email != "" && !emailRe.MatchString(*change.Email) {
		f.add("email", "must be an email address or empty")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	// Greetings upload before the settings change, so a failed upload does
	// not leave the box pointing at nothing; the old object is removed only
	// after the row names the new one.
	uploaded := map[string]string{}
	for name, data := range map[string][]byte{"greeting": greeting, "unreachable": unreach} {
		if data == nil {
			continue
		}
		if s.Objects == nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
			return
		}
		object := fmt.Sprintf("box/%d/%s-%d.wav", id, name, time.Now().Unix())
		if err := s.Objects.Put(r.Context(), object, bytes.NewReader(data), int64(len(data))); err != nil {
			s.Log.Error("voicemail: upload greeting", "error", err)
			writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
			return
		}
		uploaded[name] = object
		if name == "greeting" {
			change.Greeting = &object
		} else {
			change.Unreachable = &object
		}
	}
	if change.Email == nil && change.Password == nil && change.Greeting == nil && change.Unreachable == nil {
		badRequest(w, "password, email, greeting or unreachable is required")
		return
	}
	// The previous greetings, so they can go once the row names the new ones.
	var replaced []string
	if prev, err := s.Store.GetVoicemailBox(r.Context(), id); err == nil {
		if change.Greeting != nil && prev.GreetingObject != "" {
			replaced = append(replaced, prev.GreetingObject)
		}
		if change.Unreachable != nil && prev.UnreachableObject != "" {
			replaced = append(replaced, prev.UnreachableObject)
		}
	}
	b, err := s.Store.UpdateVoicemailBox(r.Context(), actor(r).String(), id, change, s.check())
	if err != nil {
		s.configError(w, "voicemail box", err)
		return
	}
	// Best effort: a stale object left behind is harmless.
	if s.Objects != nil {
		for _, object := range replaced {
			if err := s.Objects.Remove(r.Context(), object); err != nil {
				s.Log.Warn("voicemail: remove old greeting", "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, b)
}

// isWAV checks the RIFF/WAVE container header; the caller decides about the
// payload inside it.
func isWAV(b []byte) bool {
	return len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE"
}

// readVoicemailUpload walks a multipart body and fills in the box change and
// the greeting uploads. Every field is bounded; unknown parts are skipped.
func readVoicemailUpload(r *http.Request, change *store.VoicemailBoxChange, greeting, unreach *[]byte) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return errors.New("invalid multipart body")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("invalid multipart body")
		}
		switch name := part.FormName(); name {
		case "password":
			b, err := io.ReadAll(io.LimitReader(part, 73))
			if err != nil {
				return errors.New("invalid password field")
			}
			if v := string(b); v != "" {
				change.Password = &v
			}
		case "email":
			b, err := io.ReadAll(io.LimitReader(part, 255))
			if err != nil {
				return errors.New("invalid email field")
			}
			if v := strings.TrimSpace(string(b)); v != "" {
				change.Email = &v
			}
		case "greeting", "unreachable":
			data, err := io.ReadAll(io.LimitReader(part, maxGreetingBytes+1))
			if err != nil {
				return errors.New("could not read " + name)
			}
			if len(data) > maxGreetingBytes {
				return fmt.Errorf("%s is larger than 5 MiB", name)
			}
			// A client-declared audio type is not trusted; the bytes decide.
			if !isWAV(data) {
				return fmt.Errorf("%s must be a PCM WAV file", name)
			}
			if name == "greeting" {
				*greeting = data
			} else {
				*unreach = data
			}
		}
		_ = part.Close()
	}
}

// Voicemail messages.

func (s *server) listVoicemailMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	box, err := strconv.ParseInt(q.Get("box"), 10, 64)
	if err != nil || box <= 0 {
		badRequest(w, "box must be a voicemail box id")
		return
	}
	unheard := false
	if v := q.Get("unheard"); v != "" {
		if unheard, err = strconv.ParseBool(v); err != nil {
			badRequest(w, "unheard must be true or false")
			return
		}
	}
	ms, err := s.Store.ListVoicemailMessages(r.Context(), box, unheard)
	if err != nil {
		s.internal(w, "list voicemail messages", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ms))
}

func (s *server) markMessageHeard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	heard := true
	if r.ContentLength != 0 {
		var in struct {
			Heard *bool `json:"heard"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Heard != nil {
			heard = *in.Heard
		}
	}
	if err := s.Store.UpdateVoicemailMessageHeard(r.Context(), actor(r).String(), id, heard); err != nil {
		s.configError(w, "voicemail message", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "heard": heard})
}

// deleteMessage removes the row and then the MinIO object. The row wins:
// if the object removal fails it is logged, because a second delete attempt
// cannot find the row again, and an orphaned object harms nothing.
func (s *server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	object, err := s.Store.DeleteVoicemailMessage(r.Context(), actor(r).String(), id)
	if err != nil {
		s.configError(w, "voicemail message", err)
		return
	}
	if s.Objects != nil && object != "" {
		if err := s.Objects.Remove(r.Context(), object); err != nil {
			s.Log.Warn("voicemail: remove message audio", "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// messageAudio is GET /api/v1/voicemail/messages/{id}/audio: a 302 to a
// presigned GET URL (15 minutes, spec contract 6). The message must exist
// before anything is presigned.
func (s *server) messageAudio(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.Store.GetVoicemailMessage(r.Context(), id)
	if err != nil {
		s.configError(w, "voicemail message", err)
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	url, err := s.Objects.Presign(r.Context(), m.Object)
	if err != nil {
		s.Log.Error("voicemail: presign message audio", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage is unavailable; try again shortly")
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// Ring groups.

func (s *server) listRingGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Store.ListRingGroups(r.Context())
	if err != nil {
		s.internal(w, "list ring groups", err)
		return
	}
	writeJSON(w, http.StatusOK, items(gs))
}

func (s *server) getRingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.Store.GetRingGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "ring group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// ringGroupBody is the create/PATCH body. PATCH fields are pointers; create
// reads the same shape with defaults applied.
type ringGroupBody struct {
	Name          *string              `json:"name"`
	Strategy      *string              `json:"strategy"`
	Hunt          *bool                `json:"hunt"`
	RingTimeout   *int                 `json:"ringTimeout"`
	MemberDelay   *int                 `json:"memberDelay"`
	IgnoreDND     *bool                `json:"ignoreDnd"`
	FailureKind   *string              `json:"failureKind"`
	FailureTarget *string              `json:"failureTarget"`
	Members       *[]ringGroupMemberIn `json:"members"`
}

type ringGroupMemberIn struct {
	ExtensionID int64 `json:"extensionId"`
	Position    int   `json:"position"`
	Weight      *int  `json:"weight"`
	Delay       *int  `json:"delay"`
}

func (b ringGroupBody) empty() bool { return b == ringGroupBody{} }

// toInput folds a body into a full input on top of base (the stored group
// for PATCH, the defaults for create).
func (b ringGroupBody) toInput(base store.RingGroupInput) (store.RingGroupInput, bool) {
	if b.Name != nil {
		base.Name = *b.Name
	}
	if b.Strategy != nil {
		base.Strategy = *b.Strategy
	}
	if b.Hunt != nil {
		base.Hunt = *b.Hunt
	}
	if b.RingTimeout != nil {
		base.RingTimeout = *b.RingTimeout
	}
	if b.MemberDelay != nil {
		base.MemberDelay = *b.MemberDelay
	}
	if b.IgnoreDND != nil {
		base.IgnoreDND = *b.IgnoreDND
	}
	if b.FailureKind != nil {
		base.FailureKind = *b.FailureKind
	}
	if b.FailureTarget != nil {
		base.FailureTarget = *b.FailureTarget
	}
	if b.Members != nil {
		base.Members = make([]store.RingGroupMember, 0, len(*b.Members))
		for _, m := range *b.Members {
			w := 1
			if m.Weight != nil {
				w = *m.Weight
			}
			d := 0
			if m.Delay != nil {
				d = *m.Delay
			}
			base.Members = append(base.Members, store.RingGroupMember{
				ExtensionID: m.ExtensionID, Position: m.Position, Weight: w, Delay: d})
		}
	}
	return base, true
}

// validateRingGroup checks a full input (stage 1). Stage 2 is the store's
// whole-configuration check like every other configuration change.
func validateRingGroup(in *store.RingGroupInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if !slices.Contains(strategies, in.Strategy) {
		f.add("strategy", `must be one of ring-all, sequential, round-robin, longest-idle, weighted`)
	}
	if in.RingTimeout < 5 || in.RingTimeout > 300 {
		f.add("ringTimeout", "must be 5-300 seconds")
	}
	if in.MemberDelay < 0 || in.MemberDelay > 60 {
		f.add("memberDelay", "must be 0-60 seconds")
	}
	switch in.FailureKind {
	case "none":
		if in.FailureTarget != "" {
			f.add("failureTarget", `must be empty when failureKind is "none"`)
		}
	case "voicemail":
		if !numberRe.MatchString(in.FailureTarget) {
			f.add("failureTarget", "must be the extension number of a voicemail box")
		}
	case "external":
		if !externalNumRe.MatchString(in.FailureTarget) {
			f.add("failureTarget", "must be 2-32 of 0-9 * # with an optional leading +")
		}
	case "announcement":
		if !usernameRe.MatchString(in.FailureTarget) {
			f.add("failureTarget", "must be an announcement name (1-64 of A-Z a-z 0-9 . _ -)")
		}
	default:
		f.add("failureKind", `must be "none", "voicemail", "external" or "announcement"`)
	}
	if len(in.Members) == 0 || len(in.Members) > 50 {
		f.add("members", "must have 1-50 members")
		return f
	}
	seenExt := map[int64]bool{}
	seenPos := map[int]bool{}
	for i, m := range in.Members {
		p := fmt.Sprintf("members[%d]", i)
		if m.ExtensionID <= 0 || seenExt[m.ExtensionID] {
			f.add(p+".extensionId", "must be a distinct extension id")
		}
		seenExt[m.ExtensionID] = true
		if m.Position < 1 || m.Position > 50 || seenPos[m.Position] {
			f.add(p+".position", "must be a distinct position of 1-50")
		}
		seenPos[m.Position] = true
		// The schema keeps weight positive (a weighted member with weight 0
		// is expressed as a low weight, not zero).
		if m.Weight < 1 || m.Weight > 65535 {
			f.add(p+".weight", "must be 1-65535")
		}
		if m.Delay < 0 || m.Delay > 300 {
			f.add(p+".delay", "must be 0-300 seconds")
		}
	}
	return f
}

// defaultsRingGroup is what POST accepts as absent.
func defaultsRingGroup() store.RingGroupInput {
	return store.RingGroupInput{
		Strategy: "ring-all", Hunt: false, RingTimeout: 30, MemberDelay: 5,
		IgnoreDND: false, FailureKind: "none", FailureTarget: "",
	}
}

func (s *server) createRingGroup(w http.ResponseWriter, r *http.Request) {
	var b ringGroupBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Members == nil {
		badRequest(w, "name and members are required")
		return
	}
	in, _ := b.toInput(defaultsRingGroup())
	if f := validateRingGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.CreateRingGroup(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "ring group", err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *server) updateRingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b ringGroupBody
	if !decode(w, r, &b) {
		return
	}
	if b.empty() {
		badRequest(w, "at least one field is required")
		return
	}
	current, err := s.Store.GetRingGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "ring group", err)
		return
	}
	base := store.RingGroupInput{
		Name: current.Name, Strategy: current.Strategy, Hunt: current.Hunt,
		RingTimeout: current.RingTimeout, MemberDelay: current.MemberDelay, IgnoreDND: current.IgnoreDND,
		FailureKind: current.FailureKind, FailureTarget: current.FailureTarget, Members: current.Members,
	}
	in, _ := b.toInput(base)
	if f := validateRingGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.UpdateRingGroup(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "ring group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deleteRingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteRingGroup(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "ring group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Feature codes.

func (s *server) listFeatureCodes(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListFeatureCodes(r.Context())
	if err != nil {
		s.internal(w, "list feature codes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

func (s *server) putFeatureCodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []FeatureCodeIn `json:"items"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	seen := map[string]bool{}
	for i, c := range in.Items {
		p := fmt.Sprintf("items[%d]", i)
		if !featureCodeRe.MatchString(c.Code) {
			f.add(p+".code", `must be '*' plus 2-4 digits, or '##'`)
		}
		if !slices.Contains(fcActions, c.Action) {
			f.add(p+".action", "must be a feature-code action")
		}
		if len(c.Argument) > 64 || !printable(c.Argument, 64, false) {
			f.add(p+".argument", "must be at most 64 printable characters without spaces")
		}
		if seen[c.Code] {
			f.add(p+".code", "must be a distinct code")
		}
		seen[c.Code] = true
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	codes := make([]store.FeatureCode, len(in.Items))
	for i, c := range in.Items {
		codes[i] = store.FeatureCode{Code: c.Code, Action: c.Action, Argument: c.Argument}
	}
	if err := s.Store.PutFeatureCodes(r.Context(), actor(r).String(), codes, s.check()); err != nil {
		s.configError(w, "feature code", err)
		return
	}
	cs, err := s.Store.ListFeatureCodes(r.Context())
	if err != nil {
		s.internal(w, "list feature codes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

// FeatureCodeIn is one PUT /api/v1/feature-codes item.
type FeatureCodeIn struct {
	Code     string `json:"code"`
	Action   string `json:"action"`
	Argument string `json:"argument"`
}

// Presence.

// presenceList is the GET /api/v1/presence response.
type presenceList struct {
	Items []livestate.DeviceState `json:"items"`
}

func (s *server) presence(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	states, err := s.Live.DeviceStates(ctx)
	if err != nil {
		s.liveDown(w, "list presence", err)
		return
	}
	writeJSON(w, http.StatusOK, presenceList{Items: states})
}
