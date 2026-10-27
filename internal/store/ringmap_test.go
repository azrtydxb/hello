package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestRingMapRenderInputs fails if the render data does not carry the ring
// map (the vendor's default for a tag with no choice, the global choice, a
// phone group's choice over the global one, and nothing from another
// vendor's choice), if a ring map change leaves the phone's sync hash as it
// was (so the automatic resync would not push it), if a tag the map uses can
// be deleted, or if the map accepts a tone its vendor does not have.
func TestRingMapRenderInputs(t *testing.T) {
	st, _, ext := provStore(t)
	ctx := context.Background()
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Warehouse"})
	if err != nil {
		t.Fatal(err)
	}
	gid := g.ID
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000009", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	tone := func(tag string) (string, []string) {
		t.Helper()
		d, _, err := st.RenderInputs(ctx, c.Phone.ID)
		if err != nil && !errors.Is(err, prov.ErrNoTemplate) {
			t.Fatal(err)
		}
		var order []string
		got := ""
		for _, r := range d.Ring.Rules {
			order = append(order, r.Tag)
			if r.Tag == tag {
				got = r.Tone
			}
		}
		return got, order
	}

	if got, order := tone("doorbell"); got != "4" || order[0] != "emergency-callback" {
		t.Fatalf("default doorbell tone = %q (rules %v), want 4 (the ringer of Bellcore slot 4)", got, order)
	}
	h0, err := st.PhoneSyncHash(ctx, c.Phone.ID)
	if err != nil {
		t.Fatal(err)
	}
	// First sight of the phone stores the hash and queues nothing.
	if queued, err := st.AutoResync(ctx, c.Phone.ID, "", h0); err != nil || queued {
		t.Fatalf("first auto resync queued = %v, %v; want the hash stored and no push", queued, err)
	}

	if _, err := st.ReplaceRingMap(ctx, "test", nil, []RingMapEntry{
		{Tag: "doorbell", Vendor: "yealink", Tone: "7"},
		{Tag: "doorbell", Vendor: "poly", Tone: "ringer9"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := tone("doorbell"); got != "7" {
		t.Fatalf("global doorbell tone = %q, want 7 (and not Poly's)", got)
	}
	h1, _ := st.PhoneSyncHash(ctx, c.Phone.ID)
	if h1 == h0 {
		t.Fatal("a global ring map change left the sync hash as it was")
	}
	if queued, err := st.AutoResync(ctx, c.Phone.ID, h0, h1); err != nil || !queued {
		t.Fatalf("auto resync queued = %v, %v; want a check-sync for the changed ring map", queued, err)
	}

	gmap, err := st.ReplaceRingMap(ctx, "test", &gid, []RingMapEntry{{Tag: "doorbell", Vendor: "yealink", Tone: "bellcore3"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(gmap.Groups) != 1 || gmap.Groups[0].GroupID != gid || len(gmap.Global) != 2 {
		t.Fatalf("map = %+v", gmap)
	}
	if got, _ := tone("doorbell"); got != "11" {
		t.Fatalf("group doorbell tone = %q, want 11 (the group's choice wins)", got)
	}
	if h2, _ := st.PhoneSyncHash(ctx, c.Phone.ID); h2 == h1 {
		t.Fatal("a group override left the sync hash as it was")
	}

	// A tag the map uses cannot be deleted.
	if _, err := st.CreateRingTag(ctx, "test", RingTagInput{Name: "vip", BellcoreSlot: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceRingMap(ctx, "test", nil, []RingMapEntry{{Tag: "vip", Vendor: "snom", Tone: "Ringer4"}}); err != nil {
		t.Fatal(err)
	}
	var inUse *InUseError
	if err := st.DeleteRingTag(ctx, "test", "vip"); !errors.As(err, &inUse) {
		t.Fatalf("deleting a tag the ring map uses = %v, want a conflict", err)
	}

	// Refusals: unknown tag, vendor, tone and group, and a repeated entry.
	for name, in := range map[string][]RingMapEntry{
		"tag":    {{Tag: "ghost", Vendor: "yealink", Tone: "1"}},
		"vendor": {{Tag: "vip", Vendor: "nokia", Tone: "1"}},
		"tone":   {{Tag: "vip", Vendor: "yealink", Tone: "ringer1"}},
		"twice":  {{Tag: "vip", Vendor: "snom", Tone: "Ringer1"}, {Tag: "vip", Vendor: "snom", Tone: "Ringer2"}},
	} {
		var v *ValidationError
		if _, err := st.ReplaceRingMap(ctx, "test", nil, in); !errors.As(err, &v) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	if _, err := st.ReplaceRingMap(ctx, "test", ptr(int64(999999)), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown group: err = %v, want not found", err)
	}

	// An empty list removes a group's overrides; deleting the group removes them too.
	if m, err := st.ReplaceRingMap(ctx, "test", &gid, nil); err != nil || len(m.Groups) != 0 {
		t.Fatalf("clearing the group: %+v, %v", m, err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestRingtoneStore fails if a custom tone does not reach the render data of
// a Yealink phone (the file in Ring.Custom and the rule's tone), if a tone
// the ring map uses can be deleted, if a deleted tone leaves a phone
// referencing it, if the tone's name can be taken twice, or if a tone is
// looked up by name wrongly for the provisioning handler.
func TestRingtoneStore(t *testing.T) {
	st, _, ext := provStore(t)
	ctx := context.Background()
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000010", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tone, err := st.CreateRingtone(ctx, "test", "chime", prov.ToneInfo{SampleRate: 8000, Duration: time.Second}, 16044, sum)
	if err != nil || tone.Name != "chime" || tone.DurationMS != 1000 || tone.UsedBy != 0 {
		t.Fatalf("create = %+v, %v", tone, err)
	}
	var inUse *InUseError
	if _, err := st.CreateRingtone(ctx, "test", "chime", prov.ToneInfo{SampleRate: 8000, Duration: time.Second}, 1, sum); !errors.As(err, &inUse) {
		t.Fatalf("a taken name = %v, want a conflict", err)
	}
	got, err := st.RingtoneByName(ctx, "chime")
	if err != nil || got.ObjectKey != "chime/"+sum+".wav" || got.SHA256 != sum {
		t.Fatalf("by name = %+v, %v", got, err)
	}
	if _, err := st.RingtoneByName(ctx, "ghost"); !errors.Is(err, prov.ErrNotFound) {
		t.Fatalf("unknown name = %v, want ErrNotFound", err)
	}

	for name, e := range map[string]RingMapEntry{
		"missing": {Tag: "doorbell", Vendor: "yealink", Tone: "custom:ghost"},
		"poly":    {Tag: "doorbell", Vendor: "poly", Tone: "custom:chime"},
	} {
		var v *ValidationError
		if _, err := st.ReplaceRingMap(ctx, "test", nil, []RingMapEntry{e}); !errors.As(err, &v) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	m, err := st.ReplaceRingMap(ctx, "test", nil, []RingMapEntry{{Tag: "doorbell", Vendor: "yealink", Tone: "custom:chime"}})
	if err != nil || len(m.Global) != 1 || m.Global[0].Tone != "custom:chime" {
		t.Fatalf("map = %+v, %v", m, err)
	}
	d, _, err := st.RenderInputs(ctx, c.Phone.ID)
	if err != nil && !errors.Is(err, prov.ErrNoTemplate) {
		t.Fatal(err)
	}
	if d.Ring.Custom != "chime" {
		t.Fatalf("Ring.Custom = %q, want chime", d.Ring.Custom)
	}
	if i := slices.IndexFunc(d.Ring.Rules, func(r prov.RingRule) bool { return r.Tag == "doorbell" }); i < 0 || d.Ring.Rules[i].Tone != "chime.wav" {
		t.Fatalf("rules = %+v", d.Ring.Rules)
	}

	if _, err := st.DeleteRingtone(ctx, "test", tone.ID); !errors.As(err, &inUse) {
		t.Fatalf("deleting a tone in use = %v, want a conflict", err)
	}
	if l, _ := st.ListRingtones(ctx); len(l) != 1 || l[0].UsedBy != 1 {
		t.Fatalf("list = %+v", l)
	}
	if _, err := st.ReplaceRingMap(ctx, "test", nil, nil); err != nil {
		t.Fatal(err)
	}
	if key, err := st.DeleteRingtone(ctx, "test", tone.ID); err != nil || key != got.ObjectKey {
		t.Fatalf("delete = %q, %v", key, err)
	}
	if _, err := st.DeleteRingtone(ctx, "test", tone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want not found", err)
	}
}
