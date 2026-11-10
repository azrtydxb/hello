package store

import (
	"context"
	"errors"
	"testing"

	"github.com/azrtydxb/hello/internal/routing"
)

func blockInput(pattern string) BlocklistInput {
	return BlocklistInput{Pattern: pattern, Match: routing.BlockExact, Action: routing.ActionBlock, Enabled: true}
}

// TestBlocklistStore fails if an entry cannot be created, read, replaced and
// deleted, a repeated pattern in one scope is accepted, an unknown trunk or
// announcement is accepted, a deleted trunk leaves its entries behind, an
// import does not add new rows and update repeated ones, or the list does
// not filter and page.
func TestBlocklistStore(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	var ve *ValidationError

	e, err := s.CreateBlocklistEntry(ctx, "t", blockInput("+41225550000"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Match != "exact" || e.Action != "block" || !e.Enabled || e.TrunkID != nil {
		t.Fatalf("created = %+v", e)
	}
	if _, err := s.CreateBlocklistEntry(ctx, "t", blockInput("+41225550000")); !errors.As(err, &ve) || ve.Fields[0].Path != "pattern" {
		t.Fatalf("duplicate = %v, want a pattern error", err)
	}
	bad := blockInput("+41225550001")
	bad.TrunkID = new(int64)
	*bad.TrunkID = 99999
	if _, err := s.CreateBlocklistEntry(ctx, "t", bad); !errors.As(err, &ve) || ve.Fields[0].Path != "trunkId" {
		t.Fatalf("unknown trunk = %v, want a trunkId error", err)
	}
	bad = blockInput("+41225550001")
	bad.AnnouncementID = new(int64)
	*bad.AnnouncementID = 99999
	if _, err := s.CreateBlocklistEntry(ctx, "t", bad); !errors.As(err, &ve) || ve.Fields[0].Path != "announcementId" {
		t.Fatalf("unknown announcement = %v, want an announcementId error", err)
	}

	tr, err := s.CreateTrunk(ctx, "t", TrunkInput{Name: "carrier", Mode: "ip", RegisterExpires: 3600, OptionsInterval: 30, Enabled: true,
		SourceCIDRs: []string{"203.0.113.0/24"}, InternalDialing: Dialing{Mode: "off"}, Destinations: []routing.Destination{{Host: "203.0.113.10", Port: 5060, Priority: 1, Weight: 1}},
		CallerPolicy: routing.CallerPolicy{RejectAnonymous: true, AllowedCountryCodes: []string{"41", "49"}, RejectCode: 486}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.RejectAnonymous || len(tr.AllowedCountryCodes) != 2 || tr.RejectCode != 486 || tr.UnknownCallerAction != "block" {
		t.Fatalf("trunk policy = %+v", tr.CallerPolicy)
	}
	scoped := blockInput("+41225550000")
	scoped.TrunkID = &tr.ID
	scoped.Reason = "only here"
	e2, err := s.CreateBlocklistEntry(ctx, "t", scoped)
	if err != nil {
		t.Fatalf("the same number on a trunk: %v", err)
	}
	if e2.TrunkName != "carrier" || e2.Reason != "only here" {
		t.Fatalf("scoped = %+v", e2)
	}

	got, err := s.UpdateBlocklistEntry(ctx, "t", e.ID, func(cur BlocklistInput) (BlocklistInput, error) {
		cur.Action, cur.Match, cur.Pattern = routing.ActionAllow, routing.BlockPrefix, "+4122*"
		return cur, nil
	})
	if err != nil || got.Action != "allow" || got.Pattern != "+4122*" {
		t.Fatalf("update = %+v, %v", got, err)
	}

	res, err := s.ImportBlocklist(ctx, "t", []BlocklistInput{blockInput("+33123456789"), func() BlocklistInput {
		in := blockInput("+4122*")
		in.Match, in.Action, in.Reason = routing.BlockPrefix, routing.ActionBlock, "flipped"
		return in
	}()})
	if err != nil || res.Created != 1 || res.Updated != 1 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	if cur, _ := s.GetBlocklistEntry(ctx, e.ID); cur.Action != "block" || cur.Reason != "flipped" {
		t.Fatalf("imported update = %+v", cur)
	}

	all, next, total, err := s.ListBlocklist(ctx, BlocklistFilter{Limit: 2})
	if err != nil || len(all) != 2 || next == "" || total != 3 {
		t.Fatalf("page = %d entries, next %q, total %d, %v", len(all), next, total, err)
	}
	if only, _, n, _ := s.ListBlocklist(ctx, BlocklistFilter{Scope: "global", Q: "33"}); n != 1 || len(only) != 1 {
		t.Fatalf("filter = %d entries (total %d)", len(only), n)
	}

	if err := s.DeleteBlocklistEntry(ctx, "t", e.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBlocklistEntry(ctx, "t", e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want not found", err)
	}
	if err := s.DeleteTrunk(ctx, "t", tr.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBlocklistEntry(ctx, e2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entry of a deleted trunk survived: %v", err)
	}
}
