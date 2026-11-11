package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/azrtydxb/hello/internal/location"
	"github.com/azrtydxb/hello/internal/store"
)

// Locations, their assignments, the E911 switch and alarm destination of a
// phone group, and the emergency settings (migration 00078, spec
// phone-feature-parity S-28 and S-29). Reads are open to a viewer; changes to
// locations and assignments need the write scope, and the phone group's
// alarm destination and the emergency settings the admin scope, as the
// changes to phone groups do.

// locationBody is the writable shape of a location. An update replaces the
// location, so absent fields are cleared; the kind cannot change.
type locationBody struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	ParentID    *int64   `json:"parentId"`
	Country     string   `json:"country"`
	State       string   `json:"state"`
	City        string   `json:"city"`
	Street      string   `json:"street"`
	HouseNumber string   `json:"houseNumber"`
	PostalCode  string   `json:"postalCode"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	RadiusM     *float64 `json:"radiusM"`
}

func (b locationBody) node() location.Node {
	return location.Node{
		Kind: b.Kind, Name: b.Name, ParentID: b.ParentID, Country: b.Country, State: b.State, City: b.City,
		Street: b.Street, HouseNumber: b.HouseNumber, PostalCode: b.PostalCode,
		Latitude: b.Latitude, Longitude: b.Longitude, RadiusM: b.RadiusM,
	}
}

func (s *server) listLocations(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListLocations(r.Context())
	if err != nil {
		s.internal(w, "list locations", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	v, err := s.Store.GetLocation(r.Context(), id)
	if err != nil {
		s.configError(w, "location", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createLocation(w http.ResponseWriter, r *http.Request) {
	var b locationBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.Store.CreateLocation(r.Context(), actor(r).String(), b.node())
	if err != nil {
		s.configError(w, "location", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) putLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var b locationBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.Store.UpdateLocation(r.Context(), actor(r).String(), id, b.node())
	if err != nil {
		s.configError(w, "location", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if err := s.Store.DeleteLocation(r.Context(), actor(r).String(), id); err != nil {
		s.configError(w, "location", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listLocationAssignments(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListLocationAssignments(r.Context())
	if err != nil {
		s.internal(w, "list location assignments", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

// requiredID is a JSON number or null that must be present: UnmarshalJSON runs
// only when the key is, so a body without it is told apart from a null.
type requiredID struct {
	set bool
	v   *int64
}

func (r *requiredID) UnmarshalJSON(b []byte) error {
	r.set = true
	if string(b) == "null" {
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	r.v = &n
	return nil
}

type assignmentBody struct {
	// LocationID is the location to assign, or null to unassign.
	LocationID requiredID `json:"locationId"`
}

// putLocationAssignment assigns a phone group, extension or phone to a
// location, or to none.
func (s *server) putLocationAssignment(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != store.AssignGroup && kind != store.AssignExtension && kind != store.AssignPhone {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var b assignmentBody
	if !decode(w, r, &b) {
		return
	}
	if !b.LocationID.set {
		var f fieldErrs
		f.add("locationId", "is required: a location id, or null to unassign")
		writeFields(w, f)
		return
	}
	if err := s.Store.SetLocationAssignment(r.Context(), actor(r).String(), kind, id, b.LocationID.v); err != nil {
		s.configError(w, kind, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// phoneLocationView is the location a phone resolves to and where it comes
// from; Source is empty and Place null when none is assigned.
type phoneLocationView struct {
	Source     string          `json:"source"`
	LocationID *int64          `json:"locationId"`
	Place      *location.Place `json:"place"`
	E911       bool            `json:"e911"`
}

func (s *server) getPhoneLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	pl, err := s.Store.PhoneLocation(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNoLocation):
		writeJSON(w, http.StatusOK, phoneLocationView{E911: pl.E911})
		return
	case err != nil:
		s.storeError(w, "phone", err)
		return
	}
	lid := pl.LocationID
	writeJSON(w, http.StatusOK, phoneLocationView{Source: pl.Source, LocationID: &lid, Place: &pl.Place, E911: pl.E911})
}

// phoneEmergencyBody is the E911 switch and alarm destination of a phone
// group; an update replaces both.
type phoneEmergencyBody struct {
	E911Enabled *bool   `json:"e911Enabled"`
	AlarmTarget *string `json:"alarmTarget"`
}

func (s *server) getPhoneGroupEmergency(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	v, err := s.Store.GetPhoneGroupEmergency(r.Context(), id)
	if err != nil {
		s.configError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// dialTargetRe is the form of an extension number or ring group name.
var dialTargetRe = regexp.MustCompile(`^[0-9A-Za-z._-]{1,64}$`)

func (s *server) putPhoneGroupEmergency(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var b phoneEmergencyBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	if b.E911Enabled == nil {
		f.add("e911Enabled", "is required")
	}
	switch {
	case b.AlarmTarget == nil:
		f.add("alarmTarget", "is required: an extension or ring group number, or empty")
	case *b.AlarmTarget != "" && !dialTargetRe.MatchString(*b.AlarmTarget):
		f.add("alarmTarget", "must be an extension or ring group number, or empty")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.SetPhoneGroupEmergency(r.Context(), actor(r).String(), id, store.PhoneEmergency{E911Enabled: *b.E911Enabled, AlarmTarget: *b.AlarmTarget})
	if err != nil {
		s.configError(w, "phone group", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type emergencySettingsBody struct {
	NotifyGroup *string `json:"notifyGroup"`
}

func (s *server) getEmergencySettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetEmergencySettings(r.Context())
	if err != nil {
		s.internal(w, "read emergency settings", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) putEmergencySettings(w http.ResponseWriter, r *http.Request) {
	var b emergencySettingsBody
	if !decode(w, r, &b) {
		return
	}
	if b.NotifyGroup == nil || (*b.NotifyGroup != "" && !dialTargetRe.MatchString(*b.NotifyGroup)) {
		var f fieldErrs
		f.add("notifyGroup", "is required: a ring group number, or empty")
		writeFields(w, f)
		return
	}
	v, err := s.Store.PutEmergencySettings(r.Context(), actor(r).String(), store.EmergencySettings{NotifyGroup: *b.NotifyGroup})
	if err != nil {
		s.configError(w, "emergency settings", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
