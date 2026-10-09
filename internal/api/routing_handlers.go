package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// nullable is a PATCH field that distinguishes "absent" (Set false) from
// "null" (Set, Value nil).
type nullable[T any] struct {
	Set   bool
	Value *T
}

func (n *nullable[T]) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		n.Value = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

// writeFields answers a validation failure with every failing field.
func writeFields(w http.ResponseWriter, fields []routing.FieldError) {
	msg := "invalid configuration"
	if len(fields) > 0 {
		msg += ": " + fields[0].Path + ": " + fields[0].Message
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
		"code": "bad_request", "message": msg, "fields": fields,
	}})
}

// configError answers a failed configuration change.
func (s *server) configError(w http.ResponseWriter, what string, err error) {
	var v *store.ValidationError
	var used *store.InUseError
	switch {
	case errors.As(err, &v):
		writeFields(w, v.Fields)
	case errors.As(err, &used):
		writeError(w, http.StatusConflict, "conflict", used.Msg)
	default:
		s.storeError(w, what, err)
	}
}

// check is the whole-configuration validation (stage 2): the configuration
// as it would be after the change must compile. The voice SIP address is
// deployment configuration, not a database row, so it rides in here (spec
// S-17: without it, a route to an agent is a validation error).
func (s *server) check() store.Check {
	return func(cfg routing.Config) []routing.FieldError {
		cfg.VoiceSIPAddress = s.VoiceSIPAddress
		_, errs := s.Router.Compile(cfg)
		return errs
	}
}

// Trunks.

type trunkBody struct {
	Name            *string                `json:"name"`
	Mode            *string                `json:"mode"`
	Username        *string                `json:"username"`
	Password        *string                `json:"password"`
	Realm           *string                `json:"realm"`
	FromDomain      *string                `json:"fromDomain"`
	RegisterExpires *int                   `json:"registerExpires"`
	OptionsInterval *int                   `json:"optionsInterval"`
	SourceCIDRs     *[]string              `json:"sourceCidrs"`
	MaxCalls        *int                   `json:"maxCalls"`
	DefaultCallerID *string                `json:"defaultCallerId"`
	Enabled         *bool                  `json:"enabled"`
	Destinations    *[]routing.Destination `json:"destinations"`
}

func (b trunkBody) empty() bool { return b == trunkBody{} }

func (b trunkBody) applyTo(in *store.TrunkInput) {
	setIf(&in.Name, b.Name)
	setIf(&in.Mode, b.Mode)
	setIf(&in.Username, b.Username)
	setIf(&in.Realm, b.Realm)
	setIf(&in.FromDomain, b.FromDomain)
	setIf(&in.RegisterExpires, b.RegisterExpires)
	setIf(&in.OptionsInterval, b.OptionsInterval)
	setIf(&in.SourceCIDRs, b.SourceCIDRs)
	setIf(&in.MaxCalls, b.MaxCalls)
	setIf(&in.DefaultCallerID, b.DefaultCallerID)
	setIf(&in.Enabled, b.Enabled)
	setIf(&in.Destinations, b.Destinations)
}

// validPassword checks a new trunk password without echoing it.
func validPassword(f *fieldErrs, pw *string) {
	if pw != nil && !printable(*pw, 256, true) {
		f.add("password", "must be at most 256 printable characters")
	}
}

func (s *server) listTrunks(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListTrunks(r.Context())
	if err != nil {
		s.internal(w, "list trunks", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ts))
}

func (s *server) getTrunk(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := s.Store.GetTrunk(r.Context(), id)
	if err != nil {
		s.storeError(w, "trunk", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) createTrunk(w http.ResponseWriter, r *http.Request) {
	var b trunkBody
	if !decode(w, r, &b) {
		return
	}
	in := store.TrunkInput{RegisterExpires: 3600, OptionsInterval: 30, Enabled: true, SourceCIDRs: []string{}}
	b.applyTo(&in)
	password := ""
	setIf(&password, b.Password)
	f := validateTrunk(&in, password != "")
	validPassword(&f, b.Password)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	t, err := s.Store.CreateTrunk(r.Context(), actor(r).String(), in, password, s.check())
	if err != nil {
		s.configError(w, "trunk", err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) updateTrunk(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b trunkBody
	if !decode(w, r, &b) {
		return
	}
	if b.empty() {
		badRequest(w, "no fields to change")
		return
	}
	var f fieldErrs
	validPassword(&f, b.Password)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	// password omitted keeps it, "" clears it, a value replaces it.
	pw := store.PasswordChange{Set: b.Password != nil}
	setIf(&pw.Value, b.Password)
	t, err := s.Store.UpdateTrunk(r.Context(), actor(r).String(), id, pw, func(in *store.TrunkInput, has bool) []routing.FieldError {
		b.applyTo(in)
		return validateTrunk(in, has)
	}, s.check())
	if err != nil {
		s.configError(w, "trunk", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTrunk(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteTrunk(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "trunk", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type namedTrunkStatus struct {
	livestate.TrunkStatus
	Name string `json:"name"`
}

func (s *server) trunkStatus(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListTrunks(r.Context())
	if err != nil {
		s.internal(w, "list trunks", err)
		return
	}
	if s.Trunks == nil {
		s.liveDown(w, "trunk status", errors.New("no trunk live state configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	out := make([]namedTrunkStatus, 0, len(ts))
	for _, t := range ts {
		st, err := s.Trunks.TrunkStatus(ctx, t.ID)
		if err != nil {
			s.liveDown(w, "trunk status", err)
			return
		}
		sort.Slice(st.Destinations, func(i, j int) bool { return st.Destinations[i].Destination < st.Destinations[j].Destination })
		out = append(out, namedTrunkStatus{TrunkStatus: st, Name: t.Name})
	}
	writeJSON(w, http.StatusOK, items(out))
}

// Outbound routes.

type outboundBody struct {
	Name              *string                    `json:"name"`
	MatchKind         *string                    `json:"matchKind"`
	Match             *string                    `json:"match"`
	SourceExtensions  *[]string                  `json:"sourceExtensions"`
	Schedule          nullable[routing.Schedule] `json:"schedule"`
	NumberTransform   *routing.Transform         `json:"numberTransform"`
	CallerIDTransform *routing.Transform         `json:"callerIdTransform"`
	Trunks            *[]int64                   `json:"trunks"`
	FailoverCodes     *[]int                     `json:"failoverCodes"`
	Emergency         *bool                      `json:"emergency"`
	Enabled           *bool                      `json:"enabled"`
}

func (b outboundBody) empty() bool {
	return b.Name == nil && b.MatchKind == nil && b.Match == nil && b.SourceExtensions == nil && !b.Schedule.Set &&
		b.NumberTransform == nil && b.CallerIDTransform == nil && b.Trunks == nil && b.FailoverCodes == nil &&
		b.Emergency == nil && b.Enabled == nil
}

func (b outboundBody) applyTo(o *store.OutboundRoute) {
	setIf(&o.Name, b.Name)
	setIf(&o.MatchKind, b.MatchKind)
	setIf(&o.Match, b.Match)
	setIf(&o.SourceExtensions, b.SourceExtensions)
	if b.Schedule.Set {
		o.Schedule = b.Schedule.Value
	}
	setIf(&o.NumberTransform, b.NumberTransform)
	setIf(&o.CallerIDTransform, b.CallerIDTransform)
	setIf(&o.Trunks, b.Trunks)
	setIf(&o.FailoverCodes, b.FailoverCodes)
	setIf(&o.Emergency, b.Emergency)
	setIf(&o.Enabled, b.Enabled)
}

func (s *server) listOutbound(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Store.ListOutboundRoutes(r.Context())
	if err != nil {
		s.internal(w, "list outbound routes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(rs))
}

func (s *server) getOutbound(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := s.Store.GetOutboundRoute(r.Context(), id)
	if err != nil {
		s.storeError(w, "outbound route", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *server) createOutbound(w http.ResponseWriter, r *http.Request) {
	var b outboundBody
	if !decode(w, r, &b) {
		return
	}
	o := store.OutboundRoute{Enabled: true, SourceExtensions: []string{}, FailoverCodes: slices.Clone(store.DefaultFailoverCodes)}
	b.applyTo(&o)
	if f := validateOutbound(&o); len(f) > 0 {
		writeFields(w, f)
		return
	}
	out, err := s.Store.CreateOutboundRoute(r.Context(), actor(r).String(), o, s.check())
	if err != nil {
		s.configError(w, "outbound route", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) updateOutbound(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b outboundBody
	if !decode(w, r, &b) {
		return
	}
	if b.empty() {
		badRequest(w, "no fields to change")
		return
	}
	out, err := s.Store.UpdateOutboundRoute(r.Context(), actor(r).String(), id, func(o *store.OutboundRoute) []routing.FieldError {
		b.applyTo(o)
		return validateOutbound(o)
	}, s.check())
	if err != nil {
		s.configError(w, "outbound route", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) deleteOutbound(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteOutboundRoute(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "outbound route", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Inbound routes.

type inboundBody struct {
	Name              *string                    `json:"name"`
	DIDKind           *string                    `json:"didKind"`
	DID               *string                    `json:"did"`
	TrunkID           nullable[int64]            `json:"trunkId"`
	SIPDomain         *string                    `json:"sipDomain"`
	HeaderName        *string                    `json:"headerName"`
	HeaderRegex       *string                    `json:"headerRegex"`
	Schedule          nullable[routing.Schedule] `json:"schedule"`
	CallerIDTransform *routing.Transform         `json:"callerIdTransform"`
	DestinationKind   *string                    `json:"destinationKind"`
	Destination       *string                    `json:"destination"`
	Enabled           *bool                      `json:"enabled"`
}

func (b inboundBody) empty() bool {
	return b.Name == nil && b.DIDKind == nil && b.DID == nil && !b.TrunkID.Set && b.SIPDomain == nil &&
		b.HeaderName == nil && b.HeaderRegex == nil && !b.Schedule.Set && b.CallerIDTransform == nil &&
		b.DestinationKind == nil && b.Destination == nil && b.Enabled == nil
}

func (b inboundBody) applyTo(in *store.InboundRoute) {
	setIf(&in.Name, b.Name)
	setIf(&in.DIDKind, b.DIDKind)
	setIf(&in.DID, b.DID)
	if b.TrunkID.Set {
		in.TrunkID = b.TrunkID.Value
	}
	setIf(&in.SIPDomain, b.SIPDomain)
	setIf(&in.HeaderName, b.HeaderName)
	setIf(&in.HeaderRegex, b.HeaderRegex)
	if b.Schedule.Set {
		in.Schedule = b.Schedule.Value
	}
	setIf(&in.CallerIDTransform, b.CallerIDTransform)
	setIf(&in.DestinationKind, b.DestinationKind)
	setIf(&in.Destination, b.Destination)
	setIf(&in.Enabled, b.Enabled)
}

func (s *server) listInbound(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Store.ListInboundRoutes(r.Context())
	if err != nil {
		s.internal(w, "list inbound routes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(rs))
}

func (s *server) getInbound(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in, err := s.Store.GetInboundRoute(r.Context(), id)
	if err != nil {
		s.storeError(w, "inbound route", err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *server) createInbound(w http.ResponseWriter, r *http.Request) {
	var b inboundBody
	if !decode(w, r, &b) {
		return
	}
	in := store.InboundRoute{DIDKind: "any", Enabled: true}
	b.applyTo(&in)
	if f := validateInbound(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	out, err := s.Store.CreateInboundRoute(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "inbound route", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) updateInbound(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b inboundBody
	if !decode(w, r, &b) {
		return
	}
	if b.empty() {
		badRequest(w, "no fields to change")
		return
	}
	out, err := s.Store.UpdateInboundRoute(r.Context(), actor(r).String(), id, func(in *store.InboundRoute) []routing.FieldError {
		b.applyTo(in)
		return validateInbound(in)
	}, s.check())
	if err != nil {
		s.configError(w, "inbound route", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteInboundRoute(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "inbound route", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorder handles PUT /api/v1/routes/{kind}/order.
func (s *server) reorder(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			IDs []int64 `json:"ids"`
		}
		if !decode(w, r, &b) {
			return
		}
		if b.IDs == nil || len(b.IDs) > 10000 {
			writeFields(w, []routing.FieldError{{Path: "ids", Message: "must list every route's id (at most 10000)"}})
			return
		}
		if err := s.Store.ReorderRoutes(r.Context(), actor(r).String(), kind, b.IDs, s.check()); err != nil {
			s.configError(w, kind+" routes", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
