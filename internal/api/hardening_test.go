package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
)

// tokenStore accepts any bearer token as user admin.
type tokenStore struct{ stubStore }

func (tokenStore) TokenActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{UserID: 1, Username: "admin", TokenID: 1, Role: auth.RoleAdmin}, nil
}

type oneBinding struct{}

func (oneBinding) AllBindings(context.Context) ([]livestate.Binding, error) {
	return []livestate.Binding{{AOR: "sip:a@hello.test", Path: []string{"<sip:10.0.0.1:5060;lr;hflow=c2VjcmV0LXRva2Vu>"}}}, nil
}
func (oneBinding) Calls(context.Context) ([]livestate.Call, error) { return nil, nil }
func (oneBinding) DeviceStates(context.Context) ([]livestate.DeviceState, error) {
	return nil, nil
}

type liveDown struct{}

func (liveDown) AllBindings(context.Context) ([]livestate.Binding, error) {
	return nil, errors.New("valkey: connection refused")
}
func (liveDown) DeviceStates(context.Context) ([]livestate.DeviceState, error) {
	return nil, context.DeadlineExceeded
}

func (liveDown) Calls(context.Context) ([]livestate.Call, error) {
	return nil, errors.New("valkey: connection refused")
}

// TestLiveViewsUnavailable fails if a Valkey outage turns the live views into
// a 500 instead of a 503 with the unavailable code.
func TestLiveViewsUnavailable(t *testing.T) {
	h := Handler(Config{Store: tokenStore{}, Live: liveDown{}})
	for _, path := range []string{"/api/v1/registrations", "/api/v1/calls"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer anything")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"unavailable"`) {
			t.Fatalf("%s with Valkey down = %d %s, want 503 unavailable", path, rec.Code, rec.Body)
		}
	}
}

// TestCrossOriginRejected fails if a cross-site browser POST reaches a
// handler, or if a same-origin or non-browser request is blocked.
func TestCrossOriginRejected(t *testing.T) {
	h := Handler(Config{Store: stubStore{}})
	for _, tc := range []struct {
		site string
		want int
	}{
		{"cross-site", http.StatusForbidden},
		{"same-site", http.StatusForbidden}, // a sibling origin is still cross-origin
		{"same-origin", http.StatusBadRequest},
		{"", http.StatusBadRequest}, // curl, SDKs
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(""))
		if tc.site != "" {
			req.Header.Set("Sec-Fetch-Site", tc.site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Sec-Fetch-Site %q: POST login = %d, want %d", tc.site, rec.Code, tc.want)
		}
	}
}

// TestRegistrationsRedactFlowToken fails if a binding's edge flow token is
// returned to API clients.
func TestRegistrationsRedactFlowToken(t *testing.T) {
	h := Handler(Config{Store: tokenStore{}, Live: oneBinding{}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/registrations", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "c2VjcmV0LXRva2Vu") || !strings.Contains(body, "hflow=REDACTED") {
		t.Fatalf("registrations = %d %s, want 200 with the flow token redacted", rec.Code, body)
	}
}

func (tokenStore) UserActor(context.Context, int64) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}
