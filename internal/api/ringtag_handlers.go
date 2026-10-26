package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/ringtag"
	"github.com/azrtydxb/hello/internal/store"
)

// Ring tags and tag rules (migration 00043, spec phone-feature-parity S-39
// and S-40). Reads are open to a viewer; changes need the write scope, like
// ring groups.

type ringTagBody struct {
	Name         *string `json:"name"`
	BellcoreSlot *int    `json:"bellcoreSlot"`
	Description  *string `json:"description"`
}

func (b ringTagBody) input(base store.RingTagInput) (store.RingTagInput, fieldErrs) {
	if b.Name != nil {
		base.Name = *b.Name
	}
	if b.BellcoreSlot != nil {
		base.BellcoreSlot = *b.BellcoreSlot
	}
	if b.Description != nil {
		base.Description = *b.Description
	}
	var f fieldErrs
	if !ringtag.ValidName(base.Name) {
		f.add("name", "must be 1-32 of a-z 0-9 -")
	}
	if base.BellcoreSlot < 1 || base.BellcoreSlot > 5 {
		f.add("bellcoreSlot", "must be 1-5")
	}
	if !printable(base.Description, 200, true) {
		f.add("description", "must be at most 200 printable characters")
	}
	return base, f
}

func (s *server) listRingTags(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListRingTags(r.Context())
	if err != nil {
		s.internal(w, "list ring tags", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getRingTag(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetRingTag(r.Context(), r.PathValue("name"))
	if err != nil {
		s.configError(w, "ring tag", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createRingTag(w http.ResponseWriter, r *http.Request) {
	var b ringTagBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in, f := b.input(store.RingTagInput{BellcoreSlot: 1})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.CreateRingTag(r.Context(), actor(r).String(), in)
	if err != nil {
		s.configError(w, "ring tag", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) putRingTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var b ringTagBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name != nil && *b.Name != name {
		badRequest(w, "the name of a ring tag cannot change")
		return
	}
	cur, err := s.Store.GetRingTag(r.Context(), name)
	if err != nil {
		s.configError(w, "ring tag", err)
		return
	}
	in, f := b.input(store.RingTagInput{Name: name, BellcoreSlot: cur.BellcoreSlot, Description: cur.Description})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.UpdateRingTag(r.Context(), actor(r).String(), name, in)
	if err != nil {
		s.configError(w, "ring tag", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteRingTag(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteRingTag(r.Context(), actor(r).String(), r.PathValue("name")); err != nil {
		s.configError(w, "ring tag", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type ringRuleBody struct {
	Name  string        `json:"name"`
	Match ringtag.Match `json:"match"`
	Tag   string        `json:"tag"`
}

type ringRulesBody struct {
	Rules *[]ringRuleBody `json:"rules"`
}

func (s *server) listRingRules(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListRingRules(r.Context())
	if err != nil {
		s.internal(w, "list ring rules", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

// putRingRules replaces the whole ordered rule list.
func (s *server) putRingRules(w http.ResponseWriter, r *http.Request) {
	var b ringRulesBody
	if !decode(w, r, &b) {
		return
	}
	if b.Rules == nil {
		badRequest(w, "rules is required")
		return
	}
	var f fieldErrs
	if len(*b.Rules) > ringtag.MaxRules {
		f.add("rules", "must have at most %d rules", ringtag.MaxRules)
	}
	in := make([]store.RingRuleInput, 0, len(*b.Rules))
	for i, rb := range *b.Rules {
		p := "rules[" + strconv.Itoa(i) + "]"
		if !ringtag.ValidName(rb.Tag) {
			f.add(p+".tag", "must be a ring tag name")
		}
		if !printable(rb.Name, 64, true) {
			f.add(p+".name", "must be at most 64 printable characters")
		}
		if rb.Match.Empty() {
			f.add(p+".match", "must set at least one of callerPattern, trunk, did, callerKind or window")
		}
		for _, pr := range ringtag.ValidateMatch(p+".match", rb.Match) {
			f.add(pr.Path, "%s", pr.Msg)
		}
		in = append(in, store.RingRuleInput{Name: rb.Name, Match: rb.Match, Tag: rb.Tag})
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.ReplaceRingRules(r.Context(), actor(r).String(), in)
	if err != nil {
		s.configError(w, "ring rules", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

// ringChoice is what GET /api/v1/ring-rules/evaluate returns.
type ringChoice struct {
	Tag          string `json:"tag"`
	Why          string `json:"why"`
	BellcoreSlot int    `json:"bellcoreSlot"`
	AlertInfo    string `json:"alertInfo"`
}

// evaluateRingRules answers which tag a described call would ring with,
// using the saved tags and rules. The query names the call: kind, callerNumber,
// did, trunk, callerKind, fromTrunk, groupTag and at (RFC 3339).
func (s *server) evaluateRingRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := ringtag.Input{
		Kind: q.Get("kind"), CallerNumber: q.Get("callerNumber"), DID: q.Get("did"), Trunk: q.Get("trunk"),
		CallerKind: q.Get("callerKind"), GroupTag: q.Get("groupTag"), At: time.Now(),
	}
	var f fieldErrs
	switch in.Kind {
	case "", ringtag.KindPage, ringtag.KindIntercom, ringtag.KindAlarm, ringtag.KindDoor, ringtag.KindEmergencyCallback:
	default:
		f.add("kind", "must be page, intercom, alarm, door or emergency-callback")
	}
	switch in.CallerKind {
	case "", ringtag.CallerExtension, ringtag.CallerDoor, ringtag.CallerTrunk:
	default:
		f.add("callerKind", "must be extension, door or trunk")
	}
	if in.GroupTag != "" && !ringtag.ValidName(in.GroupTag) {
		f.add("groupTag", "must be a ring tag name")
	}
	if v := q.Get("fromTrunk"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			f.add("fromTrunk", "must be true or false")
		}
		in.FromTrunk = b
	}
	if in.CallerKind == "" {
		in.CallerKind = ringtag.CallerExtension
		if in.FromTrunk || in.Trunk != "" {
			in.CallerKind = ringtag.CallerTrunk
		}
	}
	if in.Trunk != "" {
		in.FromTrunk = true
	}
	if v := q.Get("at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			f.add("at", "must be an RFC 3339 time")
		}
		in.At = t
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	tags, err := s.Store.ListRingTags(r.Context())
	if err != nil {
		s.internal(w, "list ring tags", err)
		return
	}
	rules, err := s.Store.ListRingRules(r.Context())
	if err != nil {
		s.internal(w, "list ring rules", err)
		return
	}
	ts := make([]ringtag.Tag, len(tags))
	for i, t := range tags {
		ts[i] = t.Tag
	}
	tbl := ringtag.NewTable(ts, rules)
	ch := tbl.Choose(in)
	writeJSON(w, http.StatusOK, ringChoice{Tag: ch.Tag, Why: ch.Why, BellcoreSlot: tbl.Slot(ch.Tag),
		AlertInfo: tbl.Header(ch.Tag, "")})
}
