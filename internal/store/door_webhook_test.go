package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/secret"
)

// TestDoorWebhookStore fails if a webhook secret is stored in clear, opens
// under another webhook's id, survives a rotation, or is returned by a read;
// if a ring action can name a non-Door group or a paging group, a chime
// action can go without one, a missing announcement is accepted, or the
// audit log carries the secret (spec S-48).
func TestDoorWebhookStore(t *testing.T) {
	st := scratchStore(t)
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	st = st.WithSecretBox(box)
	ctx := context.Background()
	member := mustExtension(t, st, "200")
	door, err := st.CreateRingGroup(ctx, "t", RingGroupInput{Name: "800", Strategy: "ring-all", RingTimeout: 30, IsDoor: true,
		Members: []RingGroupMember{{ExtensionID: member.ID, Position: 1, Weight: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := st.CreateRingGroup(ctx, "t", RingGroupInput{Name: "810", Strategy: "ring-all", RingTimeout: 30,
		Members: []RingGroupMember{{ExtensionID: member.ID, Position: 1, Weight: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := st.CreatePagingGroup(ctx, "t", PagingGroupInput{Name: "Hall", Code: "*60", Priority: "normal", Enabled: true,
		Members: []int64{member.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := st.CreateAnnouncement(ctx, "t", "chime", "ann/chime.wav", nil)
	if err != nil {
		t.Fatal(err)
	}

	refused := func(what string, err error, path string) {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) || len(ve.Fields) == 0 || ve.Fields[0].Path != path {
			t.Fatalf("%s: err = %v, want a validation error on %s", what, err, path)
		}
	}
	base := DoorWebhookInput{Name: "Gate bell", Action: DoorActionRing, RingGroupID: &door.ID, RateLimit: DefaultDoorRateLimit, Enabled: true}

	hook, sec, err := st.CreateDoorWebhook(ctx, "t", base)
	if err != nil {
		t.Fatal(err)
	}
	if len(sec) != 64 || hook.RingGroup != "800" {
		t.Fatalf("created %+v with a %d-character secret", hook, len(sec))
	}
	var enc []byte
	if err := st.db.QueryRowContext(ctx, `SELECT secret_enc FROM door_webhooks WHERE id = $1`, hook.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), sec) {
		t.Fatal("the secret is stored in clear")
	}
	if back, err := box.Open(enc, secret.DoorWebhookAAD(hook.ID)); err != nil || back != sec {
		t.Fatalf("the secret does not open under its webhook: %v", err)
	}
	if _, err := box.Open(enc, secret.DoorWebhookAAD(hook.ID+1)); err == nil {
		t.Error("the secret opens under another webhook")
	}
	got, ok, err := st.DoorWebhookForTrigger(ctx, hook.ID)
	if err != nil || !ok || got.Secret != sec || got.RingGroup != "800" || got.Action != DoorActionRing {
		t.Fatalf("for trigger = %+v, %v, %v", got, ok, err)
	}
	if _, ok, _ := st.DoorWebhookForTrigger(ctx, hook.ID+99); ok {
		t.Error("an unknown webhook was found")
	}

	// Rotation replaces the secret and the old one stops opening the value.
	next, err := st.RotateDoorWebhookSecret(ctx, "t", hook.ID)
	if err != nil || next == sec {
		t.Fatalf("rotate = %q, %v", next, err)
	}
	if got, _, _ := st.DoorWebhookForTrigger(ctx, hook.ID); got.Secret != next {
		t.Error("the rotated secret is not the stored one")
	}
	if _, err := st.RotateDoorWebhookSecret(ctx, "t", hook.ID+99); !errors.Is(err, ErrNotFound) {
		t.Errorf("rotate of a missing webhook: %v, want not found", err)
	}

	// Validation.
	for _, c := range []struct {
		what, path string
		edit       func(*DoorWebhookInput)
	}{
		{"non-Door group", "ringGroupId", func(in *DoorWebhookInput) { in.RingGroupID = &plain.ID }},
		{"missing group", "ringGroupId", func(in *DoorWebhookInput) { id := int64(999); in.RingGroupID = &id }},
		{"paging group on a ring action", "pagingGroupId", func(in *DoorWebhookInput) { in.PagingGroupID = &page.ID }},
		{"chime without a paging group", "pagingGroupId", func(in *DoorWebhookInput) { in.Action, in.RingGroupID = DoorActionChime, nil }},
		{"ring group on a chime", "ringGroupId", func(in *DoorWebhookInput) { in.Action, in.PagingGroupID = DoorActionChime, &page.ID }},
		{"missing announcement", "announcementId", func(in *DoorWebhookInput) { id := int64(999); in.AnnouncementID = &id }},
		{"bad action", "action", func(in *DoorWebhookInput) { in.Action = "open" }},
		{"bad name", "name", func(in *DoorWebhookInput) { in.Name = "a/b" }},
		{"rate zero", "rateLimit", func(in *DoorWebhookInput) { in.RateLimit = 0 }},
	} {
		in := base
		in.Name = "Other"
		c.edit(&in)
		_, _, err := st.CreateDoorWebhook(ctx, "t", in)
		refused(c.what, err, c.path)
	}
	if _, _, err := st.CreateDoorWebhook(ctx, "t", base); !errors.Is(err, ErrConflict) {
		t.Errorf("a duplicate name: %v, want a conflict", err)
	}

	chime := DoorWebhookInput{Name: "Hall chime", Action: DoorActionChime, PagingGroupID: &page.ID, AnnouncementID: &ann.ID, RateLimit: 5, Enabled: true}
	ch, _, err := st.CreateDoorWebhook(ctx, "t", chime)
	if err != nil || ch.PagingGroup != "Hall" || ch.Announcement != "chime" {
		t.Fatalf("chime = %+v, %v", ch, err)
	}
	if h, _, _ := st.DoorWebhookForTrigger(ctx, ch.ID); h.PagingCode != "*60" || h.Announcement != "chime" {
		t.Errorf("chime for trigger = %+v", h)
	}
	chime.Enabled = false
	if up, err := st.UpdateDoorWebhook(ctx, "t", ch.ID, chime); err != nil || up.Enabled {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if h, ok, _ := st.DoorWebhookForTrigger(ctx, ch.ID); !ok || h.Enabled {
		t.Errorf("a disabled webhook = %+v, %v", h, ok)
	}

	// A group a webhook rings cannot be deleted from under it.
	if err := st.DeleteRingGroup(ctx, "t", door.ID, nil); err == nil {
		t.Error("a Door group a webhook rings was deleted")
	}
	if err := st.NoteDoorWebhookTriggered(ctx, hook.ID); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.GetDoorWebhook(ctx, hook.ID); h.LastTriggeredAt == nil {
		t.Error("the trigger time was not stamped")
	}

	if err := st.DeleteDoorWebhook(ctx, "t", hook.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDoorWebhook(ctx, "t", hook.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v, want not found", err)
	}
	var leaked int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE (actor || action || resource || resource_id) LIKE '%' || $1 || '%'`, sec).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("the audit log carries the secret (%d, %v)", leaked, err)
	}
}
