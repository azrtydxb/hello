package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// Call park lots and call pickup groups (phone-feature-parity spec S-6,
// S-50). Reads are open to a viewer; changes need the operator role, like
// ring groups: a park lot or pickup group only decides who can answer which
// call, it adds no new reach to a phone.

// ParkedLive is the live view of the occupied park slots; *LazyValkey
// implements it.
type ParkedLive interface {
	ParkList(ctx context.Context) ([]livestate.ParkedCall, error)
}

// parkLotBody is the create and replace body of a park lot.
type parkLotBody struct {
	Name          *string `json:"name"`
	SlotStart     *int    `json:"slotStart"`
	SlotEnd       *int    `json:"slotEnd"`
	ReturnSecs    *int    `json:"returnSecs"`
	VoicemailSecs *int    `json:"voicemailSecs"`
	Enabled       *bool   `json:"enabled"`
}

// toInput applies the body over the defaults of spec default 3: return to
// the parker after 120 s, voicemail after 60 s more, enabled.
func (b parkLotBody) toInput() store.ParkLotInput {
	in := store.ParkLotInput{ReturnSecs: 120, VoicemailSecs: 60, Enabled: true}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.SlotStart != nil {
		in.SlotStart = *b.SlotStart
	}
	if b.SlotEnd != nil {
		in.SlotEnd = *b.SlotEnd
	}
	if b.ReturnSecs != nil {
		in.ReturnSecs = *b.ReturnSecs
	}
	if b.VoicemailSecs != nil {
		in.VoicemailSecs = *b.VoicemailSecs
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	return in
}

func validateParkLot(in *store.ParkLotInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if in.SlotStart < 1 || in.SlotStart > 99999 {
		f.add("slotStart", "must be 1-99999")
	}
	if in.SlotEnd < 1 || in.SlotEnd > 99999 {
		f.add("slotEnd", "must be 1-99999")
	}
	if in.SlotEnd < in.SlotStart || in.SlotEnd-in.SlotStart >= store.MaxParkLotSlots {
		f.add("slotEnd", "must be at least slotStart and cover at most %d slots", store.MaxParkLotSlots)
	}
	if in.ReturnSecs < 10 || in.ReturnSecs > 3600 {
		f.add("returnSecs", "must be 10-3600")
	}
	if in.VoicemailSecs < 10 || in.VoicemailSecs > 3600 {
		f.add("voicemailSecs", "must be 10-3600")
	}
	return f
}

func (s *server) listParkLots(w http.ResponseWriter, r *http.Request) {
	ls, err := s.Store.ListParkLots(r.Context())
	if err != nil {
		s.internal(w, "list park lots", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ls))
}

func (s *server) getParkLot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := s.Store.GetParkLot(r.Context(), id)
	if err != nil {
		s.configError(w, "park lot", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *server) createParkLot(w http.ResponseWriter, r *http.Request) {
	var b parkLotBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.SlotStart == nil || b.SlotEnd == nil {
		badRequest(w, "name, slotStart and slotEnd are required")
		return
	}
	in := b.toInput()
	if f := validateParkLot(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	l, err := s.Store.CreateParkLot(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "park lot", err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *server) putParkLot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b parkLotBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.SlotStart == nil || b.SlotEnd == nil {
		badRequest(w, "name, slotStart and slotEnd are required")
		return
	}
	in := b.toInput()
	if f := validateParkLot(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	l, err := s.Store.UpdateParkLot(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "park lot", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *server) deleteParkLot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteParkLot(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "park lot", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parkedList is the GET /api/v1/parked-calls response.
type parkedList struct {
	Items []livestate.ParkedCall `json:"items"`
}

// listParkedCalls shows the calls parked right now, from every node.
func (s *server) listParkedCalls(w http.ResponseWriter, r *http.Request) {
	if s.Parked == nil {
		s.liveDown(w, "list parked calls", ErrValkeyConnecting)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	ps, err := s.Parked.ParkList(ctx)
	if err != nil {
		s.liveDown(w, "list parked calls", err)
		return
	}
	if ps == nil {
		ps = []livestate.ParkedCall{}
	}
	writeJSON(w, http.StatusOK, parkedList{Items: ps})
}

// pickupBody is the create and replace body of a pickup group.
type pickupBody struct {
	Name    *string  `json:"name"`
	Members *[]int64 `json:"members"`
}

func (b pickupBody) toInput() store.PickupGroupInput {
	var in store.PickupGroupInput
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Members != nil {
		in.Members = *b.Members
	}
	return in
}

func validatePickupGroup(in *store.PickupGroupInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if len(in.Members) == 0 || len(in.Members) > store.MaxPickupMembers {
		f.add("members", "must have 1-%d members", store.MaxPickupMembers)
	}
	seen := map[int64]bool{}
	for i, id := range in.Members {
		if id <= 0 || seen[id] {
			f.add(fmt.Sprintf("members[%d]", i), "must be a distinct extension id")
		}
		seen[id] = true
	}
	return f
}

func (s *server) listPickupGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.Store.ListPickupGroups(r.Context())
	if err != nil {
		s.internal(w, "list pickup groups", err)
		return
	}
	writeJSON(w, http.StatusOK, items(gs))
}

func (s *server) getPickupGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.Store.GetPickupGroup(r.Context(), id)
	if err != nil {
		s.configError(w, "pickup group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) createPickupGroup(w http.ResponseWriter, r *http.Request) {
	var b pickupBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Members == nil {
		badRequest(w, "name and members are required")
		return
	}
	in := b.toInput()
	if f := validatePickupGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.CreatePickupGroup(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "pickup group", err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *server) putPickupGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b pickupBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Members == nil {
		badRequest(w, "name and members are required")
		return
	}
	in := b.toInput()
	if f := validatePickupGroup(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	g, err := s.Store.UpdatePickupGroup(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "pickup group", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deletePickupGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeletePickupGroup(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "pickup group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
