package store

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/azrtydxb/hello/internal/secret"
)

// TestDoorOpenStore fails if the door-open code is stored in clear, does not
// open under its own extension, is accepted on a user extension or in a form
// other than 1-8 characters of 0-9, * and #, survives the station becoming a
// user again, or can be read back through GetDoorOpen.
func TestDoorOpenStore(t *testing.T) {
	st := scratchStore(t)
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	st = st.WithSecretBox(box)
	ctx := context.Background()
	user := mustExtension(t, st, "100")
	door, err := st.CreateExtensionKind(ctx, "t", "900", "Front door", "", ExtensionDoor, DoorTargets{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const code = "8#42"
	got, err := st.SetDoorOpen(ctx, "t", door.ID, DoorOpenChange{Code: ptr(code), HangupAfterOpen: ptr(true)}, nil)
	if err != nil || !got.CodeSet || !got.HangupAfterOpen {
		t.Fatalf("set = %+v, %v", got, err)
	}
	var enc []byte
	if err := st.db.QueryRowContext(ctx, `SELECT code_enc FROM door_open WHERE extension_id = $1`, door.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || string(enc) == code || containsBytes(enc, code) {
		t.Fatal("the code is stored in clear")
	}
	if back, err := box.Open(enc, secret.DoorOpenAAD(door.ID)); err != nil || back != code {
		t.Fatalf("the code does not open under its extension: %q, %v", back, err)
	}
	if _, err := box.Open(enc, secret.DoorOpenAAD(door.ID+1)); err == nil {
		t.Error("the code opens under another extension")
	}
	if d, err := st.GetDoorOpen(ctx, door.ID); err != nil || !d.CodeSet || !d.HangupAfterOpen {
		t.Errorf("get = %+v, %v", d, err)
	}

	// Changing only the flag keeps the code.
	if d, err := st.SetDoorOpen(ctx, "t", door.ID, DoorOpenChange{HangupAfterOpen: ptr(false)}, nil); err != nil || !d.CodeSet || d.HangupAfterOpen {
		t.Errorf("flag only = %+v, %v", d, err)
	}
	for _, bad := range []string{"123456789", "12ab", "1 2", "+1"} {
		if _, err := st.SetDoorOpen(ctx, "t", door.ID, DoorOpenChange{Code: ptr(bad)}, nil); err == nil {
			t.Errorf("code %q accepted", bad)
		}
	}
	if _, err := st.SetDoorOpen(ctx, "t", user.ID, DoorOpenChange{Code: ptr(code)}, nil); err == nil {
		t.Error("a user extension took a door-open code")
	}
	var audited int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE (action || resource || resource_id || actor) LIKE '%' || $1 || '%'`, code).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 0 {
		t.Error("the audit log carries the code")
	}

	// An empty code clears it; a station that becomes a user loses its row.
	if d, err := st.SetDoorOpen(ctx, "t", door.ID, DoorOpenChange{Code: ptr("")}, nil); err != nil || d.CodeSet {
		t.Errorf("clear = %+v, %v", d, err)
	}
	if _, err := st.SetDoorOpen(ctx, "t", door.ID, DoorOpenChange{Code: ptr(code)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateExtension(ctx, "t", door.ID, ExtensionChange{Kind: ptr(ExtensionUser)}, nil); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM door_open WHERE extension_id = $1`, door.ID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a user extension keeps its door-open row (%d, %v)", rows, err)
	}
}

func containsBytes(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
