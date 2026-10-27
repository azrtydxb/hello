package api

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// IVR / auto-attendant menus (migration 00033). Reads are open to a viewer;
// changes need the write scope, like ring groups. A menu that pages (a paging
// destination) needs the admin role and scope, like the paging groups
// themselves: it lets anyone who reaches the menu page the group.

var ivrNumberRe = regexp.MustCompile(`^([0-9]{2,16})?$`)

// ivrDestBody is a destination as the API takes it.
type ivrDestBody struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// ivrBody is the create and replace body. Replace (PUT) takes the whole
// menu; create applies defaults to absent optional fields.
type ivrBody struct {
	Name              *string            `json:"name"`
	Number            *string            `json:"number"`
	Enabled           *bool              `json:"enabled"`
	GreetingID        *int64             `json:"greetingId"`
	InvalidPromptID   *int64             `json:"invalidPromptId"`
	TimeoutPromptID   *int64             `json:"timeoutPromptId"`
	DigitTimeout      *int               `json:"digitTimeout"`
	InterDigitTimeout *int               `json:"interDigitTimeout"`
	MaxRetries        *int               `json:"maxRetries"`
	AllowDirectDial   *bool              `json:"allowDirectDial"`
	Timeout           *ivrDestBody       `json:"timeout"`
	Invalid           *ivrDestBody       `json:"invalid"`
	Options           *[]store.IVROption `json:"options"`
}

// toInput applies the body over defaults: enabled, a 5 second first-digit
// timeout, 3 seconds between digits, 3 retries, no direct dialling and a
// hang-up on timeout or an invalid choice.
func (b ivrBody) toInput() store.IVRMenuInput {
	in := store.IVRMenuInput{
		Enabled: true, DigitTimeout: 5, InterDigitTimeout: 3, MaxRetries: 3,
		Timeout: store.IVRDestination{Kind: store.IVRHangup}, Invalid: store.IVRDestination{Kind: store.IVRHangup},
		Options: []store.IVROption{},
	}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Number != nil {
		in.Number = *b.Number
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.GreetingID != nil {
		in.GreetingID = *b.GreetingID
	}
	in.InvalidPromptID, in.TimeoutPromptID = b.InvalidPromptID, b.TimeoutPromptID
	if b.DigitTimeout != nil {
		in.DigitTimeout = *b.DigitTimeout
	}
	if b.InterDigitTimeout != nil {
		in.InterDigitTimeout = *b.InterDigitTimeout
	}
	if b.MaxRetries != nil {
		in.MaxRetries = *b.MaxRetries
	}
	if b.AllowDirectDial != nil {
		in.AllowDirectDial = *b.AllowDirectDial
	}
	if b.Timeout != nil {
		in.Timeout = store.IVRDestination{Kind: b.Timeout.Kind, Target: b.Timeout.Target}
	}
	if b.Invalid != nil {
		in.Invalid = store.IVRDestination{Kind: b.Invalid.Kind, Target: b.Invalid.Target}
	}
	if b.Options != nil {
		in.Options = *b.Options
	}
	return in
}

// validateIVRDestination checks a destination's kind and the shape of its
// target (stage 1); the store checks that the target exists.
func validateIVRDestination(f *fieldErrs, path, kind, target string) {
	if !slices.Contains(store.IVRDestinationKinds, kind) {
		f.add(path+".kind", "must be one of %v", store.IVRDestinationKinds)
		return
	}
	var ok bool
	var want string
	switch kind {
	case store.IVRHangup:
		ok, want = target == "", `empty when kind is "hangup"`
	case store.IVRDirectory:
		ok, want = target == "", `empty when kind is "directory"`
	case store.IVRExtension, store.IVRVoicemail:
		ok, want = numberRe.MatchString(target), "an extension number (2-10 digits)"
	case store.IVRRingGroup, store.IVRAnnouncement, store.IVRMenuDest, store.IVRTimeCondition:
		ok, want = usernameRe.MatchString(target), "a name of 1-64 of A-Z a-z 0-9 . _ -"
	case store.IVRPaging:
		ok, want = pageCodeRe.MatchString(target), "a page code: '*' plus 2-4 digits"
	case store.IVRQueueDest:
		ok, want = usernameRe.MatchString(target), "a queue name of 1-64 of A-Z a-z 0-9 . _ -"
	case store.IVRExternal:
		ok, want = externalNumRe.MatchString(target), "2-32 of 0-9 * # with an optional leading +"
	}
	if !ok {
		f.add(path+".target", "must be %s", want)
	}
}

// validateIVRMenu checks a full input (stage 1).
func validateIVRMenu(in *store.IVRMenuInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if !ivrNumberRe.MatchString(in.Number) {
		f.add("number", "must be empty or 2-16 digits")
	}
	if in.GreetingID <= 0 {
		f.add("greetingId", "must name the greeting announcement")
	}
	for path, id := range map[string]*int64{"invalidPromptId": in.InvalidPromptID, "timeoutPromptId": in.TimeoutPromptID} {
		if id != nil && *id <= 0 {
			f.add(path, "must be an announcement id or null")
		}
	}
	if in.DigitTimeout < 1 || in.DigitTimeout > 60 {
		f.add("digitTimeout", "must be 1-60 seconds")
	}
	if in.InterDigitTimeout < 1 || in.InterDigitTimeout > 30 {
		f.add("interDigitTimeout", "must be 1-30 seconds")
	}
	if in.MaxRetries < 0 || in.MaxRetries > 10 {
		f.add("maxRetries", "must be 0-10")
	}
	validateIVRDestination(&f, "timeout", in.Timeout.Kind, in.Timeout.Target)
	validateIVRDestination(&f, "invalid", in.Invalid.Kind, in.Invalid.Target)
	if len(in.Options) > store.MaxIVROptions {
		f.add("options", "must have at most %d options", store.MaxIVROptions)
	}
	seen := map[string]bool{}
	for i, o := range in.Options {
		p := fmt.Sprintf("options[%d]", i)
		switch {
		case !store.ValidIVRDigit(o.Digit):
			f.add(p+".digit", "must be one of 0-9, * or #")
		case seen[o.Digit]:
			f.add(p+".digit", "digit %s is used twice", o.Digit)
		}
		seen[o.Digit] = true
		validateIVRDestination(&f, p, o.Kind, o.Target)
	}
	return f
}

// pagingAllowed answers 403 and reports false when the menu has a paging
// destination and the actor may not manage paging.
func pagingAllowed(w http.ResponseWriter, r *http.Request, in store.IVRMenuInput) bool {
	pages := in.Timeout.Kind == store.IVRPaging || in.Invalid.Kind == store.IVRPaging ||
		slices.ContainsFunc(in.Options, func(o store.IVROption) bool { return o.Kind == store.IVRPaging })
	if !pages {
		return true
	}
	a := actor(r)
	if !a.Role.AtLeast(auth.RoleAdmin) {
		writeError(w, http.StatusForbidden, "forbidden_role", "a paging destination needs the admin role")
		return false
	}
	if !a.Scopes.Has(auth.ScopeAdmin) {
		writeError(w, http.StatusForbidden, "insufficient_scope", "a paging destination needs the admin scope")
		return false
	}
	return true
}

func (s *server) listIVRMenus(w http.ResponseWriter, r *http.Request) {
	ms, err := s.Store.ListIVRMenus(r.Context())
	if err != nil {
		s.internal(w, "list ivr menus", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ms))
}

func (s *server) getIVRMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.Store.GetIVRMenu(r.Context(), id)
	if err != nil {
		s.configError(w, "ivr menu", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) createIVRMenu(w http.ResponseWriter, r *http.Request) {
	var b ivrBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.GreetingID == nil {
		badRequest(w, "name and greetingId are required")
		return
	}
	in := b.toInput()
	if f := validateIVRMenu(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	if !pagingAllowed(w, r, in) {
		return
	}
	m, err := s.Store.CreateIVRMenu(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "ivr menu", err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// putIVRMenu replaces a menu as a whole.
func (s *server) putIVRMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b ivrBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.GreetingID == nil {
		badRequest(w, "name and greetingId are required")
		return
	}
	in := b.toInput()
	if f := validateIVRMenu(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	if !pagingAllowed(w, r, in) {
		return
	}
	m, err := s.Store.UpdateIVRMenu(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "ivr menu", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) deleteIVRMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteIVRMenu(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "ivr menu", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
