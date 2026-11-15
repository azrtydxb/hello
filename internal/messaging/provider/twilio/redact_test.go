package twilio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// TestRedact fails if a sent message is not deleted with DELETE on its
// resource, if a message Twilio cannot delete yet is not blanked with
// Body="", if a message Twilio no longer has is not done, if refused
// credentials or a carrier outage read as done, or if the token or the
// message id reach an error.
func TestRedact(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
		del   = http.StatusNoContent
		blank = http.StatusOK
	)
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if u, p, _ := r.BasicAuth(); u != testSID || p != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/2010-04-01/Accounts/"+testSID+"/Messages/"+testMsg+".json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+string(b))
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(del)
			if del == http.StatusBadRequest {
				_, _ = io.WriteString(w, `{"code":20009,"message":"Cannot delete message because delivery has not been completed."}`)
			}
		case http.MethodPost:
			form, _ := url.ParseQuery(string(b))
			if v, ok := form["Body"]; !ok || v[0] != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(blank)
			_, _ = io.WriteString(w, `{"sid":"`+testMsg+`","body":""}`)
		}
	}))
	ctx := context.Background()
	got := func() []string { mu.Lock(); defer mu.Unlock(); out := calls; calls = nil; return out }

	red, err := c.Redact(ctx, testMsg)
	if err != nil || !red.Deleted || red.Gone {
		t.Fatalf("Redact = %+v, %v; want deleted", red, err)
	}
	if g := got(); len(g) != 1 || g[0] != "DELETE " {
		t.Fatalf("calls %v, want one DELETE", g)
	}

	del = http.StatusBadRequest
	red, err = c.Redact(ctx, testMsg)
	if err != nil || red.Deleted || red.Gone {
		t.Fatalf("Redact of an undeletable message = %+v, %v; want blanked", red, err)
	}
	if g := got(); len(g) != 2 || g[1] != "POST Body=" {
		t.Fatalf("calls %v, want DELETE then POST Body=", g)
	}

	del = http.StatusNotFound
	if red, err = c.Redact(ctx, testMsg); err != nil || !red.Gone {
		t.Fatalf("Redact of a gone message = %+v, %v", red, err)
	}
	if red, err = c.Redact(ctx, "not-a-sid"); err != nil || !red.Gone {
		t.Fatalf("Redact of a malformed id = %+v, %v", red, err)
	}

	for _, tc := range []struct {
		del  int
		want error
	}{{http.StatusUnauthorized, provider.ErrAuth}, {http.StatusServiceUnavailable, provider.ErrAmbiguous}} {
		del = tc.del
		_, err := c.Redact(ctx, testMsg)
		if !errors.Is(err, tc.want) {
			t.Errorf("DELETE %d: err %v, want %v", tc.del, err, tc.want)
		}
		if err != nil && (strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), testMsg)) {
			t.Errorf("error %q carries a secret or the id", err)
		}
	}
	del, blank = http.StatusBadRequest, http.StatusBadRequest
	var rej *provider.Rejected
	if _, err := c.Redact(ctx, testMsg); !errors.As(err, &rej) {
		t.Errorf("a refused blank = %v, want a rejection", err)
	}
	var _ provider.Redactor = c
}
