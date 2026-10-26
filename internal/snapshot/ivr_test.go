package snapshot

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestLoadIVRMenus fails if Load drops a menu, its options, its prompt names
// or its destinations, or cannot find a menu by its number.
func TestLoadIVRMenus(t *testing.T) {
	cfg := scratch(t)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var greet, bad int64
	for name, id := range map[string]*int64{"greet": &greet, "bad": &bad} {
		if err := conn.QueryRow(ctx, "INSERT INTO announcements (name, minio_object) VALUES ($1, $2) RETURNING id", name, "ann/"+name+".wav").Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	var menu int64
	if err := conn.QueryRow(ctx, `INSERT INTO ivr_menus (name, number, greeting_id, invalid_prompt_id, digit_timeout, inter_digit_timeout,
		max_retries, allow_direct_dial, timeout_kind, timeout_target, invalid_kind, invalid_target)
		VALUES ('main', '800', $1, $2, 7, 2, 4, true, 'extension', '100', 'announcement', 'bad') RETURNING id`, greet, bad).Scan(&menu); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO ivr_menu_options (menu_id, digit, dest_kind, dest_target) VALUES ($1, '1', 'extension', '100'), ($1, '#', 'ivr', 'main')`, menu); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, conn, domain)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := snap.IVRMenuByNumber("800")
	if !ok || m.Name != "main" || m.Greeting != "greet" || m.InvalidPrompt != "bad" || m.TimeoutPrompt != "" ||
		m.DigitTimeout != 7 || m.InterDigitTimeout != 2 || m.Retries != 4 || !m.AllowDirectDial || !m.Enabled {
		t.Fatalf("menu = %+v", m)
	}
	if m.Timeout != (IVRDest{Kind: IVRExtension, Target: "100"}) || m.Invalid != (IVRDest{Kind: IVRAnnouncement, Target: "bad"}) {
		t.Fatalf("exits = %+v / %+v", m.Timeout, m.Invalid)
	}
	if len(m.Options) != 2 || m.Options["#"] != (IVRDest{Kind: IVRMenuKind, Target: "main"}) {
		t.Fatalf("options = %v", m.Options)
	}
	if _, ok := snap.IVRMenu("main"); !ok {
		t.Fatal("menu by name not found")
	}
}

// TestExtensionPrefix: exact and longer matches over extension numbers.
func TestExtensionPrefix(t *testing.T) {
	s := New(1, domain, nil).WithExtensions("100", "1000", "200")
	for digits, want := range map[string][2]bool{
		"1": {false, true}, "100": {true, true}, "1000": {true, false}, "2": {false, true}, "200": {true, false},
		"3": {false, false}, "": {false, false},
	} {
		if exact, longer := s.ExtensionPrefix(digits); exact != want[0] || longer != want[1] {
			t.Errorf("ExtensionPrefix(%q) = %v, %v; want %v", digits, exact, longer, want)
		}
	}
}
