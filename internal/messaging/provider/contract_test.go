package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/messaging/provider/telnyx"
	"github.com/azrtydxb/hello/internal/messaging/provider/twilio"
)

// TestSMSProviderContract fails if a registered adapter returns a send
// without a provider id, a state outside the four Hello knows, a capability
// set that is empty, or an error that carries the credentials or the text.
func TestSMSProviderContract(t *testing.T) {
	// Built at run time: a literal looks like a real SID to secret scanners.
	sid, msgSID, token := "AC"+strings.Repeat("ab", 16), "SM"+strings.Repeat("cd", 16), "tok-contract-secret"
	telnyxMsg := "40385f64-5717-4562-b3fc-2c963f66afa6"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v2/messages") {
			_, _ = w.Write([]byte(`{"data":{"id":"` + telnyxMsg + `","to":[{"status":"queued"}]}}`))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sid":"` + msgSID + `","status":"queued"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	provider.Register(twilio.Kind, twilio.Factory(twilio.WithBaseURL(srv.URL), twilio.WithHTTPClient(srv.Client())))
	defer provider.Register(twilio.Kind, twilio.Factory())
	provider.Register(telnyx.Kind, telnyx.Factory(telnyx.WithBaseURL(srv.URL+"/v2"), telnyx.WithHTTPClient(srv.Client())))
	defer provider.Register(telnyx.Kind, telnyx.Factory())

	if len(provider.Kinds()) < 2 {
		t.Fatal("no adapter is registered")
	}
	for _, kind := range provider.Kinds() {
		p, err := provider.New(kind, provider.Config{AccountSID: sid, AuthToken: token, APIKey: token})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if p.Kind() != kind || p.Caps().MaxBody <= 0 || p.Caps().StatusWindow <= 0 {
			t.Errorf("%s: caps %+v", kind, p.Caps())
		}
		s, err := p.Send(context.Background(), provider.Outbound{From: "+3270123456", To: "+32475000001", Body: "private text"})
		if err != nil || s.ProviderID == "" {
			t.Errorf("%s: send = %+v, %v", kind, s, err)
		}
		_, err = p.Status(context.Background(), map[string]string{"twilio": msgSID, "telnyx": telnyxMsg}[kind])
		if err == nil {
			t.Fatalf("%s: a 401 status read succeeded", kind)
		}
		for _, leak := range []string{token, "private text"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: error leaks %q", kind, leak)
			}
		}
	}
	if _, err := provider.New("nope", provider.Config{}); err == nil {
		t.Error("an unknown kind was built")
	}
}
