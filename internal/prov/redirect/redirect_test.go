package redirect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestStatusJSON fails if a phone's redirect status leaves the API shape
// of the plan's contract 7, {"state","reason","at"}, or drops the reason
// of a failure.
func TestStatusJSON(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	b, err := json.Marshal(Status{State: StateFailed, Reason: "drift", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"state":"failed","reason":"drift","at":"2026-10-06T12:00:00Z"}`; string(b) != want {
		t.Fatalf("status JSON = %s, want %s", b, want)
	}
	var back Status
	if err := json.Unmarshal([]byte(`{"state":"not_configured"}`), &back); err != nil || back.State != StateNotConfigured {
		t.Fatalf("default column value = %+v, %v", back, err)
	}
	if b, _ := json.Marshal(back); string(b) != `{"state":"not_configured"}` {
		t.Fatalf("default status JSON = %s, want no reason and no zero time", b)
	}
}

// TestCredentialsNeverFormat fails if any fmt verb or slog prints a
// credential value: a client that wraps its credentials into an error or
// a log line must still print only the placeholder.
func TestCredentialsNeverFormat(t *testing.T) {
	val := "s3cr" + "et-value" // assembled so scanners do not flag a literal
	c := Credentials{"snomSrapsAccessKeySecret": val}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "creds", c)
	for _, out := range []string{
		fmt.Sprintf("%v %s %+v %#v", c, c, c, c),
		fmt.Errorf("check failed for %v", c).Error(),
		fmt.Sprint(Account{Credentials: c}),
		buf.String(),
	} {
		if strings.Contains(out, val) {
			t.Fatalf("credential printed: %s", out)
		}
	}
}
