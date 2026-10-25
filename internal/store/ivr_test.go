package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func ivrInput(name string, greeting int64) IVRMenuInput {
	return IVRMenuInput{
		Name: name, Enabled: true, GreetingID: greeting, DigitTimeout: 5, InterDigitTimeout: 3, MaxRetries: 3,
		Timeout: IVRDestination{Kind: IVRHangup}, Invalid: IVRDestination{Kind: IVRHangup}, Options: []IVROption{},
	}
}

// TestIVRMenus covers what the API relies on: options are stored and read in
// digit order, references must exist, timeouts that lead back through
// automatic exits are refused while option loops are allowed, a referenced
// menu can be neither deleted nor renamed, and the prompts it plays cannot be
// deleted. Runs against the real schema, so it needs HELLO_TEST_DATABASE_URL.
func TestIVRMenus(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO extensions (number, name) VALUES ('101', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	greet, err := s.CreateAnnouncement(ctx, "test", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	in := ivrInput("main", greet.ID)
	in.Number = "800"
	in.Options = []IVROption{{Digit: "2", Kind: IVRExtension, Target: "101"}, {Digit: "1", Kind: IVRAnnouncement, Target: "greet"}}
	main, err := s.CreateIVRMenu(ctx, "test", in, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if main.GreetingName != "greet" || len(main.Options) != 2 || main.Options[0].Digit != "1" {
		t.Fatalf("menu = %+v", main)
	}

	var ve *ValidationError
	bad := ivrInput("x", greet.ID)
	bad.Options = []IVROption{{Digit: "1", Kind: IVRExtension, Target: "999"}}
	if _, err := s.CreateIVRMenu(ctx, "test", bad, nil); !errors.As(err, &ve) {
		t.Fatalf("unknown extension: %v", err)
	}
	bad = ivrInput("x", greet.ID)
	bad.Number = "101"
	if _, err := s.CreateIVRMenu(ctx, "test", bad, nil); !errors.As(err, &ve) {
		t.Fatalf("number of an extension: %v", err)
	}
	if _, err := s.CreateIVRMenu(ctx, "test", ivrInput("x", 999999), nil); !errors.As(err, &ve) {
		t.Fatalf("unknown greeting: %v", err)
	}

	sub := ivrInput("sub", greet.ID)
	sub.Timeout = IVRDestination{Kind: IVRMenuDest, Target: "main"}
	subMenu, err := s.CreateIVRMenu(ctx, "test", sub, nil)
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	loop := in
	loop.Timeout = IVRDestination{Kind: IVRMenuDest, Target: "sub"}
	if _, err := s.UpdateIVRMenu(ctx, "test", main.ID, loop, nil); !errors.As(err, &ve) {
		t.Fatalf("automatic loop accepted: %v", err)
	}
	self := in
	self.Invalid = IVRDestination{Kind: IVRMenuDest, Target: "main"}
	if _, err := s.UpdateIVRMenu(ctx, "test", main.ID, self, nil); !errors.As(err, &ve) {
		t.Fatalf("self exit accepted: %v", err)
	}
	keys := in
	keys.Options = []IVROption{{Digit: "0", Kind: IVRMenuDest, Target: "main"}, {Digit: "9", Kind: IVRMenuDest, Target: "sub"}}
	if _, err := s.UpdateIVRMenu(ctx, "test", main.ID, keys, nil); err != nil {
		t.Fatalf("a loop through keys is allowed: %v", err)
	}

	var inUse *InUseError
	if err := s.DeleteIVRMenu(ctx, "test", subMenu.ID, nil); !errors.As(err, &inUse) {
		t.Fatalf("delete of a referenced menu: %v", err)
	}
	renamed := sub
	renamed.Name = "sub2"
	if _, err := s.UpdateIVRMenu(ctx, "test", subMenu.ID, renamed, nil); !errors.As(err, &inUse) {
		t.Fatalf("rename of a referenced menu: %v", err)
	}
	if _, err := s.DeleteAnnouncement(ctx, "test", greet.ID, nil); err == nil {
		t.Fatal("an announcement a menu plays was deleted")
	}
	if ok, err := s.IVRMenuNameExists(ctx, "sub"); err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	// Remove the key that points at sub, then both menus can go.
	keys.Options = nil
	if _, err := s.UpdateIVRMenu(ctx, "test", main.ID, keys, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIVRMenu(ctx, "test", subMenu.ID, nil); err != nil {
		t.Fatalf("delete sub: %v", err)
	}
	if err := s.DeleteIVRMenu(ctx, "test", main.ID, nil); err != nil {
		t.Fatalf("delete main: %v", err)
	}
	if err := s.DeleteIVRMenu(ctx, "test", main.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// TestIVRNamedObjectsAreProtected: what a menu's options and exits point at
// cannot be deleted or renumbered, and the error names the menu.
func TestIVRNamedObjectsAreProtected(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	var ext, rg, pg int64
	for q, dst := range map[string]*int64{
		`INSERT INTO extensions (number, name) VALUES ('101', 'Sales') RETURNING id`:         &ext,
		`INSERT INTO ring_groups (name, strategy) VALUES ('sales', 'ring-all') RETURNING id`: &rg,
		`INSERT INTO paging_groups (name, code) VALUES ('kitchen', '*60') RETURNING id`:      &pg,
	} {
		if err := s.db.QueryRowContext(ctx, q).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	greet, err := s.CreateAnnouncement(ctx, "t", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	bye, err := s.CreateAnnouncement(ctx, "t", "bye", "ann/bye.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	in := ivrInput("main", greet.ID)
	in.Timeout = IVRDestination{Kind: IVRAnnouncement, Target: "bye"}
	in.Options = []IVROption{
		{Digit: "1", Kind: IVRExtension, Target: "101"}, {Digit: "3", Kind: IVRRingGroup, Target: "sales"},
		{Digit: "4", Kind: IVRPaging, Target: "*60"},
	}
	menu, err := s.CreateIVRMenu(ctx, "t", in, nil)
	if err != nil {
		t.Fatal(err)
	}
	var inUse *InUseError
	refuse := func(what string, err error) {
		t.Helper()
		if !errors.As(err, &inUse) || !strings.Contains(inUse.Msg, "IVR menu main") {
			t.Errorf("%s: %v, want an in-use error naming the menu", what, err)
		}
	}
	number := "109"
	_, err = s.UpdateExtension(ctx, "t", ext, ExtensionChange{Number: &number}, nil)
	refuse("renumber extension", err)
	refuse("delete extension", s.DeleteExtension(ctx, "t", ext, nil))
	_, err = s.UpdateRingGroup(ctx, "t", rg, RingGroupInput{Name: "sales2", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	refuse("rename ring group", err)
	refuse("delete ring group", s.DeleteRingGroup(ctx, "t", rg, nil))
	_, err = s.UpdatePagingGroup(ctx, "t", pg, PagingGroupInput{Name: "kitchen", Code: "*61", Priority: PagingNormal, Enabled: true}, nil)
	refuse("change page code", err)
	refuse("delete paging group", s.DeletePagingGroup(ctx, "t", pg, nil))
	_, err = s.DeleteAnnouncement(ctx, "t", bye.ID, nil)
	refuse("delete announcement", err)

	in.Timeout, in.Options = IVRDestination{Kind: IVRHangup}, nil
	if _, err := s.UpdateIVRMenu(ctx, "t", menu.ID, in, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAnnouncement(ctx, "t", bye.ID, nil); err != nil {
		t.Fatalf("delete announcement after release: %v", err)
	}
	if err := s.DeletePagingGroup(ctx, "t", pg, nil); err != nil {
		t.Fatalf("delete paging group after release: %v", err)
	}
	if err := s.DeleteRingGroup(ctx, "t", rg, nil); err != nil {
		t.Fatalf("delete ring group after release: %v", err)
	}
	if err := s.DeleteExtension(ctx, "t", ext, nil); err != nil {
		t.Fatalf("delete extension after release: %v", err)
	}
}

// TestIVRReferencesCheckedInTransaction: a ring group failover and an "ivr"
// feature code must name an existing menu, checked by the store inside the
// configuration change (under the configuration lock), so a menu deleted at
// the same moment cannot leave a dangling reference. It fails if the store
// accepts a reference to a missing menu, or if a concurrent delete and
// create ever leave a group whose failover names a deleted menu.
func TestIVRReferencesCheckedInTransaction(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	greet, err := s.CreateAnnouncement(ctx, "t", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ve *ValidationError
	group := func(name, menu string) RingGroupInput {
		return RingGroupInput{Name: name, Strategy: "ring-all", RingTimeout: 30, FailureKind: "ivr", FailureTarget: menu}
	}
	if _, err := s.CreateRingGroup(ctx, "t", group("g0", "nope"), nil); !errors.As(err, &ve) ||
		ve.Fields[0].Path != "failureTarget" {
		t.Fatalf("group failing over to a missing menu: %v", err)
	}
	if err := s.PutFeatureCodes(ctx, "t", []FeatureCode{{Code: "*55", Action: "ivr", Argument: "nope"}}, nil); !errors.As(err, &ve) ||
		ve.Fields[0].Path != "items[0].argument" {
		t.Fatalf("feature code naming a missing menu: %v", err)
	}

	for i := range 20 {
		name := fmt.Sprintf("m%d", i)
		m, err := s.CreateIVRMenu(ctx, "t", ivrInput(name, greet.ID), nil)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var gErr, dErr error
		var g RingGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			g, gErr = s.CreateRingGroup(ctx, "t", group(fmt.Sprintf("g%d", i+1), name), nil)
		}()
		go func() { defer wg.Done(); dErr = s.DeleteIVRMenu(ctx, "t", m.ID, nil) }()
		wg.Wait()
		if gErr == nil && dErr == nil {
			t.Fatalf("round %d: group %s fails over to menu %s, which was deleted", i, g.Name, name)
		}
		if gErr == nil {
			if err := s.DeleteRingGroup(ctx, "t", g.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestIVRNumberNotShadowed: an IVR menu's number is matched before
// extensions and ring groups, so an extension or group must not take it. It
// fails if an extension can be created or renumbered onto a menu's number,
// or a ring group created or renamed to it.
func TestIVRNumberNotShadowed(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	greet, err := s.CreateAnnouncement(ctx, "t", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	in := ivrInput("main", greet.ID)
	in.Number = "800"
	if _, err := s.CreateIVRMenu(ctx, "t", in, nil); err != nil {
		t.Fatal(err)
	}
	var ve *ValidationError
	want := func(what string, err error, path string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Fields[0].Path != path || !strings.Contains(ve.Fields[0].Message, "IVR menu main") {
			t.Errorf("%s: %v, want a %s error naming the menu", what, err, path)
		}
	}
	_, err = s.CreateExtension(ctx, "t", "800", "Clash", "", nil)
	want("create extension", err, "number")
	e, err := s.CreateExtension(ctx, "t", "801", "Fine", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := "800"
	_, err = s.UpdateExtension(ctx, "t", e.ID, ExtensionChange{Number: &n}, nil)
	want("renumber extension", err, "number")
	_, err = s.CreateRingGroup(ctx, "t", RingGroupInput{Name: "800", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	want("create ring group", err, "name")
	g, err := s.CreateRingGroup(ctx, "t", RingGroupInput{Name: "sales", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateRingGroup(ctx, "t", g.ID, RingGroupInput{Name: "800", Strategy: "ring-all", RingTimeout: 30, FailureKind: "none"}, nil)
	want("rename ring group", err, "name")
	// The group keeps its own name through an unrelated change.
	if _, err := s.UpdateRingGroup(ctx, "t", g.ID, RingGroupInput{Name: "sales", Strategy: "sequential", RingTimeout: 30, FailureKind: "none"}, nil); err != nil {
		t.Fatalf("unrelated group change: %v", err)
	}
}
