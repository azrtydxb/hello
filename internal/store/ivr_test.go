package store

import (
	"context"
	"errors"
	"strings"
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
