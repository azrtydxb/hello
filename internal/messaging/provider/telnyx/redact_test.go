package telnyx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// TestRedactUnsupported fails if the Telnyx adapter claims to delete a
// message (Telnyx has no such operation) or misreads the messaging
// profile's own redaction setting.
func TestRedactUnsupported(t *testing.T) {
	enabled := `true`
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/messaging_profiles/"+testProf {
			_, _ = io.WriteString(w, `{"data":{"id":"`+testProf+`","redaction_enabled":`+enabled+`,"redaction_level":1}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}), func(c *provider.Config) { c.MessagingProfileID = testProf })
	if _, err := c.Redact(context.Background(), testMsg); !errors.Is(err, provider.ErrRedactUnsupported) {
		t.Fatalf("Redact = %v, want ErrRedactUnsupported", err)
	}
	if on, ok := c.ProfileRedaction(context.Background()); !ok || !on {
		t.Errorf("ProfileRedaction = %v, %v; want on", on, ok)
	}
	enabled = `false`
	if on, ok := c.ProfileRedaction(context.Background()); !ok || on {
		t.Errorf("ProfileRedaction = %v, %v; want off", on, ok)
	}
	plain := client(t, http.NotFoundHandler())
	if _, ok := plain.ProfileRedaction(context.Background()); ok {
		t.Error("a channel without a messaging profile read a redaction setting")
	}
	var _ provider.Redactor = c
}
