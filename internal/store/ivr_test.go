package store

import (
	"context"
	"errors"
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
