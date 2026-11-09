package api

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/azrtydxb/hello/internal/store"
)

// Follow-me (migration 00052, pbx-gaps S-5): one profile per extension, read
// and replaced through the extension (operators) or through /me (the user of
// the extension).

// followMeExternalRe is an external follow-me target: a dialable number, like
// the number of a trunk route (digits with an optional leading +).
var followMeExternalRe = regexp.MustCompile(`^\+?[0-9]{2,32}$`)

// followMeTargetBody is one target as sent.
type followMeTargetBody struct {
	Kind         string `json:"kind"`
	Target       string `json:"target"`
	DelaySeconds int    `json:"delaySeconds"`
}

// followMeBody is a profile as sent: a field left out keeps its stored value;
// targets, when sent, replace the whole list.
type followMeBody struct {
	Enabled         *bool                 `json:"enabled"`
	Strategy        *string               `json:"strategy"`
	RingOwnDevices  *bool                 `json:"ringOwnDevices"`
	TimeoutSeconds  *int                  `json:"timeoutSeconds"`
	ConfirmExternal *bool                 `json:"confirmExternal"`
	DNDBlocks       *bool                 `json:"dndBlocks"`
	Targets         *[]followMeTargetBody `json:"targets"`
}

// followMeInput merges the body into the stored profile and validates the
// result. own is the extension's number, which no target may be.
func (b followMeBody) input(cur store.FollowMe) (store.FollowMeInput, fieldErrs) {
	in := store.FollowMeInput{
		Enabled: cur.Enabled, Strategy: cur.Strategy, RingOwnDevices: cur.RingOwnDevices,
		TimeoutSeconds: cur.TimeoutSeconds, ConfirmExternal: cur.ConfirmExternal, DNDBlocks: cur.DNDBlocks,
		Targets: cur.Targets,
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.Strategy != nil {
		in.Strategy = *b.Strategy
	}
	if b.RingOwnDevices != nil {
		in.RingOwnDevices = *b.RingOwnDevices
	}
	if b.TimeoutSeconds != nil {
		in.TimeoutSeconds = *b.TimeoutSeconds
	}
	if b.ConfirmExternal != nil {
		in.ConfirmExternal = *b.ConfirmExternal
	}
	if b.DNDBlocks != nil {
		in.DNDBlocks = *b.DNDBlocks
	}
	if b.Targets != nil {
		in.Targets = make([]store.FollowMeTarget, 0, len(*b.Targets))
		for _, t := range *b.Targets {
			in.Targets = append(in.Targets, store.FollowMeTarget{Kind: t.Kind, Target: t.Target, DelaySeconds: t.DelaySeconds})
		}
	}
	var f fieldErrs
	if in.Strategy != store.FollowMeSimultaneous && in.Strategy != store.FollowMeSequential {
		f.add("strategy", "must be simultaneous or sequential")
	}
	if in.TimeoutSeconds < 5 || in.TimeoutSeconds > 120 {
		f.add("timeoutSeconds", "must be 5-120 seconds")
	}
	if len(in.Targets) > store.FollowMeMaxTargets {
		f.add("targets", "at most 5 targets")
	}
	if in.Enabled && len(in.Targets) == 0 {
		f.add("enabled", "a profile needs at least one target to be turned on")
	}
	return in, f
}

// checkTargets validates each target against the extension it belongs to.
func checkFollowMeTargets(f *fieldErrs, own string, targets []store.FollowMeTarget) {
	for i, t := range targets {
		path := fmt.Sprintf("targets[%d]", i)
		switch t.Kind {
		case store.FollowMeExternal:
			if !followMeExternalRe.MatchString(t.Target) {
				f.add(path+".target", "must be a number of 2-32 digits with an optional leading +")
			}
		case store.FollowMeExtension:
			if t.Target == "" || len(t.Target) > 32 {
				f.add(path+".target", "must be an extension number")
			}
		case store.FollowMeRingGroup:
			if t.Target == "" || len(t.Target) > 64 {
				f.add(path+".target", "must be the name of a ring group")
			}
		default:
			f.add(path+".kind", "must be external, extension or ring_group")
			continue
		}
		if t.Target == own && t.Kind != store.FollowMeRingGroup {
			f.add(path+".target", "a target cannot be the extension itself")
		}
		if t.DelaySeconds < 0 || t.DelaySeconds > 60 {
			f.add(path+".delaySeconds", "must be 0-60 seconds")
		}
	}
}

// getFollowMe is GET /api/v1/extensions/{id}/follow-me.
func (s *server) getFollowMe(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.writeFollowMe(w, r, id)
}

// putFollowMe is PUT /api/v1/extensions/{id}/follow-me.
func (s *server) putFollowMe(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.saveFollowMe(w, r, id)
}

// getMyFollowMe is GET /api/v1/me/extensions/{id}/follow-me.
func (s *server) getMyFollowMe(w http.ResponseWriter, r *http.Request) {
	e, ok := s.selfExtension(w, r)
	if !ok {
		return
	}
	s.writeFollowMe(w, r, e.ID)
}

// putMyFollowMe is PUT /api/v1/me/extensions/{id}/follow-me.
func (s *server) putMyFollowMe(w http.ResponseWriter, r *http.Request) {
	e, ok := s.selfExtension(w, r)
	if !ok {
		return
	}
	s.saveFollowMe(w, r, e.ID)
}

func (s *server) writeFollowMe(w http.ResponseWriter, r *http.Request, id int64) {
	v, err := s.Store.GetFollowMe(r.Context(), id)
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) saveFollowMe(w http.ResponseWriter, r *http.Request, id int64) {
	var b followMeBody
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil && b.Strategy == nil && b.RingOwnDevices == nil && b.TimeoutSeconds == nil &&
		b.ConfirmExternal == nil && b.DNDBlocks == nil && b.Targets == nil {
		badRequest(w, "a field to change is required")
		return
	}
	cur, err := s.Store.GetFollowMe(r.Context(), id)
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	in, f := b.input(cur)
	checkFollowMeTargets(&f, cur.Number, in.Targets)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.PutFollowMe(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
