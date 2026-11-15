package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestWebRTCStatusDatabase fails if, against PostgreSQL, the WebRTC status does
// not count an enrolled web device or the candidate types of the reports its
// client filed (a relayed call counted as relayed).
func TestWebRTCStatusDatabase(t *testing.T) {
	e := newEnv(t, noLive{})
	admin := e.login()
	alice, aliceID := e.asRole("anna", auth.RoleUser)
	ext := admin.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(aliceID, 10), map[string]any{"extensionId": ext["id"]})
	made := alice.must(http.StatusCreated, "POST", "/api/v1/me/devices", map[string]any{"label": "Chrome"}).json(t)
	id := made["deviceId"]
	for i, pair := range [][2]string{{"host", "host"}, {"relay", "host"}, {"srflx", "prflx"}} {
		alice.must(http.StatusNoContent, "POST", "/api/v1/me/calls/call-"+strconv.Itoa(i)+"/quality", map[string]any{
			"deviceId": id, "codec": "opus", "lossPct": 0, "jitterMs": 1, "rttMs": 20,
			"candidateLocal": pair[0], "candidateRemote": pair[1], "durationMs": 1000,
		})
	}
	got := admin.must(http.StatusOK, "GET", "/api/v1/webrtc/status", nil).json(t)
	if e := got["clients"].(map[string]any)["enrolled"].(map[string]any); e["web"] != float64(1) {
		t.Fatalf("enrolled = %v, want one web device", e)
	}
	c := got["candidates"].(map[string]any)
	local, remote := c["local"].(map[string]any), c["remote"].(map[string]any)
	if c["reports"] != float64(3) || c["relayed"] != float64(1) || local["relay"] != float64(1) || local["srflx"] != float64(1) ||
		remote["host"] != float64(2) || remote["prflx"] != float64(1) || local["unknown"] != float64(0) {
		t.Fatalf("candidates = %v", c)
	}
}
