package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/secret"
)

// TestDoorCameraStore fails if the camera password is stored in clear, opens
// under another extension, is lost when a change leaves it out, is kept when
// a change clears it, or if a user extension, an rtsp:// address, an address
// with credentials or an unknown kind is accepted (spec S-49).
func TestDoorCameraStore(t *testing.T) {
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
	const pw = "cam-s3cret-pw"
	in := DoorCameraInput{Kind: CameraMJPEG, URL: "http://192.168.10.40/video.mjpg", Username: "hello", Password: ptr(pw)}
	got, err := st.SetDoorCamera(ctx, "t", door.ID, in)
	if err != nil || !got.Configured || !got.PasswordSet || got.URL != in.URL || got.Username != "hello" {
		t.Fatalf("set = %+v, %v", got, err)
	}
	var enc []byte
	if err := st.db.QueryRowContext(ctx, `SELECT password_enc FROM door_cameras WHERE extension_id = $1`, door.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || strings.Contains(string(enc), pw) {
		t.Fatal("the password is stored in clear")
	}
	if back, err := box.Open(enc, secret.DoorCameraAAD(door.ID)); err != nil || back != pw {
		t.Fatalf("the password does not open under its extension: %v", err)
	}
	if _, err := box.Open(enc, secret.DoorCameraAAD(door.ID+1)); err == nil {
		t.Error("the password opens under another extension")
	}

	// A change without a password keeps it; an empty one clears it.
	in.Password, in.Kind = nil, CameraSnapshot
	if c, err := st.SetDoorCamera(ctx, "t", door.ID, in); err != nil || !c.PasswordSet || c.Kind != CameraSnapshot {
		t.Fatalf("change without a password = %+v, %v", c, err)
	}
	if a, ok, err := st.DoorCameraForView(ctx, door.ID); err != nil || !ok || a.Password != pw {
		t.Fatalf("for view = %+v, %v, %v; want the password kept", a, ok, err)
	}
	in.Password = ptr("")
	if c, err := st.SetDoorCamera(ctx, "t", door.ID, in); err != nil || c.PasswordSet {
		t.Fatalf("clear = %+v, %v", c, err)
	}

	refused := func(what string, in DoorCameraInput, id int64, path string) {
		t.Helper()
		_, err := st.SetDoorCamera(ctx, "t", id, in)
		var ve *ValidationError
		if !errors.As(err, &ve) || len(ve.Fields) == 0 || ve.Fields[0].Path != path {
			t.Errorf("%s: err = %v, want a validation error on %s", what, err, path)
		}
	}
	ok := DoorCameraInput{Kind: CameraSnapshot, URL: "http://192.168.10.40/snap.jpg"}
	refused("user extension", ok, user.ID, "kind")
	refused("rtsp", DoorCameraInput{Kind: CameraMJPEG, URL: "rtsp://192.168.10.40/stream1"}, door.ID, "url")
	refused("credentials in the URL", DoorCameraInput{Kind: CameraMJPEG, URL: "http://u:p@192.168.10.40/x"}, door.ID, "url")
	refused("no host", DoorCameraInput{Kind: CameraMJPEG, URL: "http:///x"}, door.ID, "url")
	refused("ftp", DoorCameraInput{Kind: CameraMJPEG, URL: "ftp://192.168.10.40/x"}, door.ID, "url")
	refused("kind", DoorCameraInput{Kind: "rtsp", URL: ok.URL}, door.ID, "kind")
	refused("colon in the user", DoorCameraInput{Kind: CameraSnapshot, URL: ok.URL, Username: "a:b"}, door.ID, "username")

	if err := st.DeleteDoorCamera(ctx, "t", door.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDoorCamera(ctx, "t", door.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v, want not found", err)
	}
	var leaked int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE (actor || action || resource || resource_id) LIKE '%' || $1 || '%'`, pw).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("the audit log carries the password (%d, %v)", leaked, err)
	}
}
