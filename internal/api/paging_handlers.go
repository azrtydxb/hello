package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// Server paging groups (phonebooks-ptt spec P-12, P-18). Reads are open to a
// viewer; every change is admin only (spec P-19), because a paging group can
// turn a phone into a listening device.

var pageCodeRe = regexp.MustCompile(`^\*[0-9]{2,4}$`)

// pagingBody is the create and replace body. Replace (PUT) takes the whole
// group; create applies defaults to absent optional fields.
type pagingBody struct {
	Name     *string  `json:"name"`
	Code     *string  `json:"code"`
	Priority *string  `json:"priority"`
	Enabled  *bool    `json:"enabled"`
	Members  *[]int64 `json:"members"`
	Callers  *[]int64 `json:"callers"`
	// MulticastChannelID is a phone group's multicast channel to page too.
	MulticastChannelID *int64 `json:"multicastChannelId"`
}

// toInput applies the body over defaults: priority normal, enabled, no
// caller restriction.
func (b pagingBody) toInput() store.PagingGroupInput {
	in := store.PagingGroupInput{Priority: store.PagingNormal, Enabled: true}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Code != nil {
		in.Code = *b.Code
	}
	if b.Priority != nil {
		in.Priority = *b.Priority
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.Members != nil {
		in.Members = *b.Members
	}
	if b.Callers != nil {
		in.Callers = *b.Callers
	}
	in.MulticastChannelID = b.MulticastChannelID
	return in
}

// validatePagingGroup checks a full input (stage 1).
func validatePagingGroup(in *store.PagingGroupInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if !pageCodeRe.MatchString(in.Code) {
		f.add("code", "must be '*' plus 2-4 digits")
	}
	if in.Priority != store.PagingNormal && in.Priority != store.PagingEmergency {
		f.add("priority", `must be "normal" or "emergency"`)
	}
	switch {
	case len(in.Members) > store.MaxPagingMembers:
		f.add("members", "must have at most %d members", store.MaxPagingMembers)
	case len(in.Members) == 0 && in.MulticastChannelID == nil:
		f.add("members", "must have 1-%d members, or a multicast channel to page", store.MaxPagingMembers)
	}
	if in.MulticastChannelID != nil && *in.MulticastChannelID <= 0 {
		f.add("multicastChannelId", "must be a multicast channel id")
	}
	checkIDs := func(path string, ids []int64) {
		seen := map[int64]bool{}
		for i, id := range ids {
			if id <= 0 || seen[id] {
				f.add(fmt.Sprintf("%s[%d]", path, i), "must be a distinct extension id")
			}
			seen[id] = true
		}
	}
	checkIDs("members", in.Members)
	if len(in.Callers) > 1000 {
		f.add("callers", "must have at most 1000 extensions")
	}
	checkIDs("callers", in.Callers)
	return f
}

func (s *server) listPagingGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Store.ListPagingGroups(r.Context())
	if err != nil {
		s.internal(w, "list paging groups", err)
		return
	}
	writeJSON(w, http.StatusOK, items(gs))
}

func (s *server) getPagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.Store.GetPagingGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) createPagingGroup(w http.ResponseWriter, r *http.Request) {
	var b pagingBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Code == nil || b.Members == nil {
		badRequest(w, "name, code and members are required")
		return
	}
	in := b.toInput()
	if f := validatePagingGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.CreatePagingGroup(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// putPagingGroup replaces a paging group as a whole.
func (s *server) putPagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b pagingBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Code == nil || b.Members == nil {
		badRequest(w, "name, code and members are required")
		return
	}
	in := b.toInput()
	if f := validatePagingGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.UpdatePagingGroup(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deletePagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeletePagingGroup(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "paging group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PageTester carries a paging-group test to a hello-sip node, which alone
// can originate the INVITE; *LazyValkey implements it.
type PageTester interface {
	EnqueuePageTest(ctx context.Context, r livestate.PageTestRequest) error
	GetPageTestResult(ctx context.Context, id string) (livestate.PageTestResult, bool, error)
}

const (
	// pageTestWait bounds how long the test request waits for a node to
	// page and report: the answer timeout plus the tone and the poll.
	pageTestWait = 20 * time.Second
	// pageTestPoll is how often the result is looked for.
	pageTestPoll = 250 * time.Millisecond
)

type pagingTestBody struct {
	// Extension is the member to page; absent means the group's first
	// member.
	Extension *string `json:"extension"`
}

// testPagingGroup is POST /api/v1/paging-groups/{id}/test: it pages one
// member of the group like a real page, with the vendor's auto-answer
// headers, plays a short tone and reports each contact's outcome and the
// headers sent (spec P-18).
func (s *server) testPagingGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b pagingTestBody
	if !decode(w, r, &b) {
		return
	}
	g, err := s.Store.GetPagingGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "paging group", err)
		return
	}
	if !g.Enabled {
		writeError(w, http.StatusConflict, "conflict", "the paging group is disabled; enable it to test it")
		return
	}
	if len(g.Members) == 0 {
		writeError(w, http.StatusConflict, "conflict", "the paging group has no members")
		return
	}
	ext := g.Members[0].Number
	if b.Extension != nil {
		ext = *b.Extension
		member := false
		for _, m := range g.Members {
			member = member || m.Number == ext
		}
		if !member {
			writeFields(w, fieldErrsOf("extension", "must be a member of the paging group"))
			return
		}
	}
	if s.PageTests == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "paging tests need Valkey and a running hello-sip node")
		return
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.internal(w, "page test id", err)
		return
	}
	req := livestate.PageTestRequest{ID: hex.EncodeToString(raw[:]), GroupCode: g.Code, Extension: ext, Actor: actor(r).String()}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	err = s.PageTests.EnqueuePageTest(ctx, req)
	cancel()
	if err != nil {
		s.Log.Warn("queue page test", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state is unavailable")
		return
	}
	if err := s.Store.Audit(r.Context(), req.Actor, "test", "paging_group", fmt.Sprint(id)); err != nil {
		s.Log.Warn("audit page test", "error", err)
	}
	tick := time.NewTicker(pageTestPoll)
	defer tick.Stop()
	deadline := time.NewTimer(s.pageTestWait())
	defer deadline.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeError(w, http.StatusGatewayTimeout, "timeout", "no hello-sip node reported the test page in time")
			return
		case <-tick.C:
		}
		rctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
		res, found, err := s.PageTests.GetPageTestResult(rctx, req.ID)
		cancel()
		if err != nil {
			s.Log.Warn("read page test result", "error", err)
			continue
		}
		if found {
			writeJSON(w, http.StatusOK, res)
			return
		}
	}
}

func (s *server) pageTestWait() time.Duration {
	if s.PageTestWait > 0 {
		return s.PageTestWait
	}
	return pageTestWait
}

func fieldErrsOf(path, msg string) fieldErrs {
	var f fieldErrs
	f.add(path, "%s", msg)
	return f
}
