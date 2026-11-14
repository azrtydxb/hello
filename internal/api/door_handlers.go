package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// Door-open (spec phone-feature-parity S-47). A door station's code is a
// write-only secret: it is set here, sealed by the store, and only hello-sip
// ever reads it back. The web call view opens the door of the user's own
// live call through POST /calls/{id}/door-open, which asks the hello-sip node
// holding the call to send the code to the door.

// DoorControl carries a door-open request to the hello-sip node that holds a
// call, and reads the node's answer; *LazyValkey implements it.
type DoorControl interface {
	ParkSend(ctx context.Context, node string, cmd livestate.ParkCommand) error
	ParkGetReply(ctx context.Context, id string) (livestate.ParkReply, bool, error)
}

const (
	// doorOpenWait bounds the wait for the node's answer; a node answers
	// within one park poll.
	doorOpenWait = 5 * time.Second
	// doorOpenPoll is how often the answer is looked for.
	doorOpenPoll = 50 * time.Millisecond
)

// doorOpenBody is the PUT body: both fields are optional, one is required.
// The code is write-only: "" clears it.
type doorOpenBody struct {
	Code            *string `json:"code"`
	HangupAfterOpen *bool   `json:"hangupAfterOpen"`
}

// doorStation resolves the {id} path value to a door station; a missing
// extension is 404 and an ordinary user extension 409.
func (s *server) doorStation(w http.ResponseWriter, r *http.Request) (store.Extension, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return store.Extension{}, false
	}
	e, err := s.Store.GetExtension(r.Context(), id)
	if err != nil {
		s.storeError(w, "extension", err)
		return store.Extension{}, false
	}
	if e.Kind != store.ExtensionDoor {
		writeError(w, http.StatusConflict, "conflict", "only a door station has a door-open code")
		return store.Extension{}, false
	}
	return e, true
}

// getDoorOpen shows whether a door-open code is set and the hang-up flag; the
// code itself is never returned.
func (s *server) getDoorOpen(w http.ResponseWriter, r *http.Request) {
	e, ok := s.doorStation(w, r)
	if !ok {
		return
	}
	d, err := s.Store.GetDoorOpen(r.Context(), e.ID)
	if err != nil {
		s.storeError(w, "door-open settings", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// putDoorOpen sets or clears the door-open code and the hang-up flag. The
// request body is not echoed, the audit row never holds the code.
func (s *server) putDoorOpen(w http.ResponseWriter, r *http.Request) {
	e, ok := s.doorStation(w, r)
	if !ok {
		return
	}
	var b doorOpenBody
	if !decode(w, r, &b) {
		return
	}
	if b.Code == nil && b.HangupAfterOpen == nil {
		badRequest(w, "send code, hangupAfterOpen or both")
		return
	}
	var f fieldErrs
	if b.Code != nil && *b.Code != "" && !store.ValidDoorCode(*b.Code) {
		f.add("code", "must be 1-%d characters of 0-9, * and #", store.MaxDoorCodeLen)
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	d, err := s.Store.SetDoorOpen(r.Context(), actor(r).String(), e.ID,
		store.DoorOpenChange{Code: b.Code, HangupAfterOpen: b.HangupAfterOpen}, s.check())
	if err != nil {
		s.configError(w, "door-open settings", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// answeredDoorCall reports whether a live call is a connected door call that
// one of exts answered (or, for a user who dialled the station, started).
func answeredDoorCall(c livestate.Call, exts []string) bool {
	if c.Door == "" || c.State != "connected" {
		return false
	}
	if c.Door == c.From {
		return c.Callee != "" && slices.Contains(exts, c.Callee)
	}
	return slices.Contains(exts, c.From)
}

// userNumbers lists the numbers of the signed-in user's extensions.
func (s *server) userNumbers(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return nil, false
	}
	exts, err := s.Store.UserExtensions(r.Context(), uid)
	if err != nil {
		s.internal(w, "list own extensions", err)
		return nil, false
	}
	if len(exts) == 0 {
		writeError(w, http.StatusForbidden, "no_extension", "your account is not linked to an extension; ask an administrator to link it")
		return nil, false
	}
	nums := make([]string, len(exts))
	for i, e := range exts {
		nums[i] = e.Number
	}
	return nums, true
}

// meDoorCall is one entry of GET /me/door-calls.
type meDoorCall struct {
	ID        string `json:"id"`
	Door      string `json:"door"`
	StartedAt string `json:"startedAt"`
}

// listMyDoorCalls lists the door calls the signed-in user answered and that
// are still up, the ones the web view can open the door of.
func (s *server) listMyDoorCalls(w http.ResponseWriter, r *http.Request) {
	nums, ok := s.userNumbers(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	cs, err := s.Live.Calls(ctx)
	if err != nil {
		s.liveDown(w, "list own door calls", err)
		return
	}
	out := []meDoorCall{}
	for _, c := range cs {
		if answeredDoorCall(c, nums) {
			out = append(out, meDoorCall{ID: c.ID, Door: c.Door, StartedAt: c.StartedAt.UTC().Format(time.RFC3339)})
		}
	}
	writeJSON(w, http.StatusOK, items(out))
}

// doorOpenCall is POST /calls/{id}/door-open: the user who answered a door
// call asks the node holding it to send the door-open code. 403 for anyone
// else, 404 for no such call, 409 when the call is no answered door call or
// the station has no code, 502 when the node could not do it, 504 when no node
// answered in time.
func (s *server) doorOpenCall(w http.ResponseWriter, r *http.Request) {
	nums, ok := s.userNumbers(w, r)
	if !ok {
		return
	}
	if s.DoorCalls == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "opening a door needs Valkey and a running hello-sip node")
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	cs, err := s.Live.Calls(ctx)
	cancel()
	if err != nil {
		s.liveDown(w, "find the door call", err)
		return
	}
	var call *livestate.Call
	for i := range cs {
		if cs[i].ID == id {
			call = &cs[i]
		}
	}
	switch {
	case call == nil:
		writeError(w, http.StatusNotFound, "not_found", "no such call")
		return
	case call.Door == "" || call.State != "connected":
		writeError(w, http.StatusConflict, "conflict", "the call is not an answered door call")
		return
	case !answeredDoorCall(*call, nums):
		writeError(w, http.StatusForbidden, "not_your_call", "only the user who answered the door call may open the door")
		return
	}
	// The extension that asks is the one on the call; the node checks it
	// again against the live dialog.
	ext := call.Callee
	if call.Door != call.From {
		ext = call.From
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.internal(w, "door open command id", err)
		return
	}
	cmd := livestate.ParkCommand{ID: hex.EncodeToString(raw[:]), Op: livestate.ParkOpDoorOpen, From: "api",
		Correlation: call.ID, Retriever: ext}
	qctx, qcancel := context.WithTimeout(r.Context(), liveTimeout)
	err = s.DoorCalls.ParkSend(qctx, call.Node, cmd)
	qcancel()
	if err != nil {
		s.liveDown(w, "queue the door open command", err)
		return
	}
	rep, found := s.awaitDoorReply(r.Context(), cmd.ID)
	if !found {
		writeError(w, http.StatusGatewayTimeout, "timeout", "the hello-sip node holding the call did not answer in time")
		return
	}
	if err := s.Store.Audit(r.Context(), actor(r).String(), "door_open", "call", call.ID); err != nil {
		s.Log.Warn("audit door open", "error", err)
	}
	switch {
	case rep.OK:
		writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
	case rep.Code == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "not_found", "the call has ended")
	case rep.Code == http.StatusForbidden:
		writeError(w, http.StatusForbidden, "not_your_call", "only the user who answered the door call may open the door")
	case rep.Code == http.StatusConflict:
		writeError(w, http.StatusConflict, "conflict", rep.Reason)
	default:
		writeError(w, http.StatusBadGateway, "upstream", "the hello-sip node could not open the door")
	}
}

// awaitDoorReply polls for the node's answer to command id.
func (s *server) awaitDoorReply(ctx context.Context, id string) (livestate.ParkReply, bool) {
	wait := s.DoorOpenWait
	if wait <= 0 {
		wait = doorOpenWait
	}
	tick := time.NewTicker(doorOpenPoll)
	defer tick.Stop()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return livestate.ParkReply{}, false
		case <-deadline.C:
			return livestate.ParkReply{}, false
		case <-tick.C:
		}
		rctx, cancel := context.WithTimeout(ctx, liveTimeout)
		rep, ok, err := s.DoorCalls.ParkGetReply(rctx, id)
		cancel()
		if err != nil {
			s.Log.Warn("read door open reply", "error", err)
			continue
		}
		if ok {
			return rep, true
		}
	}
}
