package routing

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestTrunkLogValueRedactsPassword fails if logging a Trunk (directly or
// by pointer) shows its password.
func TestTrunkLogValueRedactsPassword(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	tr := Trunk{ID: 3, Name: "carrier", Username: "acct", Password: "hunter2-trunk"}
	log.Info("x", "trunk", tr, "ptr", &tr)
	if strings.Contains(buf.String(), "hunter2-trunk") || !strings.Contains(buf.String(), `"password":"configured"`) {
		t.Fatalf("log = %s", buf.String())
	}
}
