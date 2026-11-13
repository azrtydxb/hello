package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/location"
	"github.com/azrtydxb/hello/internal/prov"
)

func iptr(v int64) *int64 { return &v }

// TestLocationsAndEmergencyProvisioning fails if a phone's location is not the
// most specific of its own, its extension's and its group's, if the HELD
// location does not follow it, if a WP836 of a group with E911 on lacks the
// E911 data (or one with it off has it), if a group's alarm destination does
// not reach the render data or leaves the phone's sync hash as it was, if a
// phone that is not a WP8x6 gets either, if an alarm destination that does not
// exist is accepted, or if a location in use can be deleted.
func TestLocationsAndEmergencyProvisioning(t *testing.T) {
	st, _, ext := provStore(t)
	ctx := context.Background()
	hq, err := st.CreateLocation(ctx, "test", location.Node{Kind: location.KindSite, Name: "HQ", Country: "NL", City: "Amsterdam", Street: "Damrak"})
	if err != nil {
		t.Fatal(err)
	}
	floor, err := st.CreateLocation(ctx, "test", location.Node{Kind: location.KindFloor, Name: "3", ParentID: &hq.ID})
	if err != nil {
		t.Fatal(err)
	}
	room, err := st.CreateLocation(ctx, "test", location.Node{Kind: location.KindRoom, Name: "Lab", ParentID: &floor.ID, Latitude: fptr(52.3), Longitude: fptr(4.9)})
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Warehouse"})
	if err != nil {
		t.Fatal(err)
	}
	gid := g.ID
	wp, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "c074ad000001", Vendor: prov.Grandstream, Model: "WP836",
		ExtensionID: ext, Enabled: true, Realm: "hello.test", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	pid := wp.Phone.ID
	e := func(who string) PhoneLocation {
		t.Helper()
		pl, err := st.PhoneLocation(ctx, pid)
		if err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		return pl
	}

	if _, err := st.PhoneLocation(ctx, pid); !errors.Is(err, ErrNoLocation) {
		t.Fatalf("a phone with no location = %v, want ErrNoLocation", err)
	}
	if _, err := st.PhoneLocation(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing phone = %v, want ErrNotFound", err)
	}
	if err := st.SetLocationAssignment(ctx, "test", AssignGroup, gid, &hq.ID); err != nil {
		t.Fatal(err)
	}
	if pl := e("group"); pl.Source != "group" || pl.Place.City != "Amsterdam" || pl.Place.Floor != "" {
		t.Fatalf("group location = %+v", pl)
	}
	if err := st.SetLocationAssignment(ctx, "test", AssignExtension, ext, &floor.ID); err != nil {
		t.Fatal(err)
	}
	if pl := e("extension"); pl.Source != "extension" || pl.Place.Floor != "3" || pl.Place.Path != "HQ / 3" {
		t.Fatalf("extension location = %+v", pl)
	}
	if err := st.SetLocationAssignment(ctx, "test", AssignPhone, pid, &room.ID); err != nil {
		t.Fatal(err)
	}
	pl := e("phone")
	if pl.Source != "phone" || pl.Place.Room != "Lab" || pl.Place.Street != "Damrak" || pl.Place.Latitude == nil || *pl.Place.Latitude != 52.3 {
		t.Fatalf("phone location = %+v", pl)
	}
	held, err := st.HELDLocation(ctx, pid)
	if err != nil || held.Place.Room != "Lab" || held.Extension == "" {
		t.Fatalf("HELD location = %+v, %v", held, err)
	}
	if err := st.SetLocationAssignment(ctx, "test", AssignPhone, pid, iptr(999999)); err == nil {
		t.Error("an unknown location was assigned")
	}
	if err := st.SetLocationAssignment(ctx, "test", AssignPhone, 999999, &hq.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("assigning a missing phone = %v, want ErrNotFound", err)
	}
	if err := st.SetLocationAssignment(ctx, "test", AssignPhone, pid, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.HELDLocation(ctx, pid); err != nil || e("extension again").Source != "extension" {
		t.Fatalf("after unassigning the phone: %v", err)
	}

	// E911 and the alarm destination in the render data.
	render := func() prov.RenderData {
		t.Helper()
		d, _, err := st.RenderInputs(ctx, pid)
		if err != nil && !errors.Is(err, prov.ErrNoTemplate) {
			t.Fatal(err)
		}
		return d
	}
	if d := render(); d.E911 != nil || d.AlarmNumber != "" {
		t.Fatalf("a group with nothing set has E911 %+v alarm %q", d.E911, d.AlarmNumber)
	}
	h0, err := st.PhoneSyncHash(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPhoneGroupEmergency(ctx, "test", gid, PhoneEmergency{E911Enabled: true, AlarmTarget: "999"}); err == nil {
		t.Fatal("an alarm destination that does not exist was accepted")
	}
	if _, err := st.SetPhoneGroupEmergency(ctx, "test", gid, PhoneEmergency{E911Enabled: true}); err != nil {
		t.Fatal(err)
	}
	d := render()
	if d.E911 == nil || !strings.HasSuffix(d.E911.Server, "/held") || !strings.Contains(d.E911.Server, "/p/") || strings.HasPrefix(d.E911.Server, "https://") || d.AlarmNumber != "" {
		t.Fatalf("E911 %+v alarm %q", d.E911, d.AlarmNumber)
	}
	h1, _ := st.PhoneSyncHash(ctx, pid)
	if h1 == h0 {
		t.Error("switching E911 on left the sync hash as it was: the phone would never be told")
	}
	var n string
	if err := st.db.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1`, ext).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPhoneGroupEmergency(ctx, "test", gid, PhoneEmergency{AlarmTarget: n}); err != nil {
		t.Fatal(err)
	}
	if d := render(); d.E911 != nil || d.AlarmNumber != "*55" {
		t.Fatalf("E911 off, alarm on: E911 %+v alarm %q", d.E911, d.AlarmNumber)
	}
	if h2, _ := st.PhoneSyncHash(ctx, pid); h2 == h1 {
		t.Error("an alarm destination left the sync hash as it was")
	}
	em, err := st.GetPhoneGroupEmergency(ctx, gid)
	if err != nil || em.AlarmTarget != n || em.E911Enabled {
		t.Fatalf("group emergency = %+v, %v", em, err)
	}

	// A phone that is not a WP8x6 gets neither.
	yl, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000042", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	if d, _, err := st.RenderInputs(ctx, yl.Phone.ID); err != nil && !errors.Is(err, prov.ErrNoTemplate) {
		t.Fatal(err)
	} else if d.E911 != nil || d.AlarmNumber != "" {
		t.Fatalf("a Yealink got E911 %+v alarm %q", d.E911, d.AlarmNumber)
	}

	// The notify group must be a ring group.
	if _, err := st.PutEmergencySettings(ctx, "test", EmergencySettings{NotifyGroup: "999"}); err == nil {
		t.Error("a notify group that does not exist was accepted")
	}
	if s, err := st.GetEmergencySettings(ctx); err != nil || s.NotifyGroup != "" {
		t.Fatalf("settings = %+v, %v", s, err)
	}

	// In use: nothing is deleted from under an assignment.
	if err := st.SetLocationAssignment(ctx, "test", AssignPhone, pid, &room.ID); err != nil {
		t.Fatal(err)
	}
	for _, l := range []Location{room, floor, hq} {
		var in *InUseError
		if err := st.DeleteLocation(ctx, "test", l.ID); !errors.As(err, &in) {
			t.Errorf("deleting %s in use = %v, want InUseError", l.Name, err)
		}
	}
}
