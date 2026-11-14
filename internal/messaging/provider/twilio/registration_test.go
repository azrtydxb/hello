package twilio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

func regClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(provider.Config{AccountSID: testSID, AuthToken: testToken},
		WithBaseURL(srv.URL+"/2010-04-01"), WithMessagingURL(srv.URL+"/v1"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRegistrationGolden(t *testing.T) {
	svcA, svcB := "MG"+strings.Repeat("a1", 16), "MG"+strings.Repeat("b2", 16)
	c := regClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != testSID || p != testToken {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/a2p/BrandRegistrations":
			_, _ = io.WriteString(w, `{"data":[{"sid":"BN1","status":"APPROVED","brand_type":"STANDARD"},{"sid":"BN2","status":"DELETED"}]}`)
		case "/v1/Services":
			_, _ = io.WriteString(w, `{"services":[{"sid":"`+svcA+`"},{"sid":"`+svcB+`"}]}`)
		case "/v1/Services/" + svcA + "/UsAppToPerson":
			_, _ = io.WriteString(w, `{"compliance":[{"sid":"QE1","brand_registration_sid":"BN1","us_app_to_person_usecase":"CUSTOMER_CARE","campaign_status":"VERIFIED"}]}`)
		case "/v1/Services/" + svcB + "/UsAppToPerson":
			_, _ = io.WriteString(w, `{"compliance":[{"sid":"QE2","brand_registration_sid":"BN1","us_app_to_person_usecase":"MIXED","campaign_status":"IN_PROGRESS"}]}`)
		case "/v1/Services/" + svcA + "/PhoneNumbers":
			_, _ = io.WriteString(w, `{"phone_numbers":[{"phone_number":"+12125550100"}]}`)
		case "/v1/Services/" + svcB + "/PhoneNumbers":
			_, _ = io.WriteString(w, `{"phone_numbers":[{"phone_number":"+12125550101"}]}`)
		case "/v1/Tollfree/Verifications":
			_, _ = io.WriteString(w, `{"verifications":[{"tollfree_phone_number_sid":"PN1","status":"TWILIO_APPROVED"},{"tollfree_phone_number_sid":"PN2","status":"TWILIO_REJECTED","rejection_reason":"no opt-in"},{"tollfree_phone_number_sid":"PN3","status":"IN_REVIEW"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	nums := []provider.Number{{E164: "+12125550100"}, {E164: "+12125550101"}, {E164: "+12125550102"},
		{ID: "PN1", E164: "+18005550100"}, {ID: "PN2", E164: "+18335550100"}, {ID: "PN3", E164: "+18445550100"}, {ID: "PN4", E164: "+18555550100"},
		{E164: "+3270123456"}}
	reg, err := c.Registration(context.Background(), nums)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Brands) != 1 || reg.Brands[0].State != provider.RegApproved || reg.Brands[0].Type != "standard" {
		t.Errorf("brands = %+v", reg.Brands)
	}
	if len(reg.Campaigns) != 2 || reg.Campaigns[0].State != provider.RegApproved || reg.Campaigns[1].State != provider.RegPending {
		t.Errorf("campaigns = %+v", reg.Campaigns)
	}
	want := []string{provider.RegApproved, provider.RegPending, provider.RegUnregistered,
		provider.RegApproved, provider.RegRejected, provider.RegPending, provider.RegUnregistered, provider.RegUnknown}
	for i, n := range reg.Numbers {
		if n.State != want[i] {
			t.Errorf("%s = %+v, want %s", nums[i].E164, n, want[i])
		}
	}
	if reg.Numbers[4].Detail != "no opt-in" || len(reg.Unavailable) != 0 {
		t.Errorf("detail %q, unavailable %v", reg.Numbers[4].Detail, reg.Unavailable)
	}
}

func TestRegistrationDegrades(t *testing.T) {
	c := regClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/Tollfree") {
			_, _ = io.WriteString(w, `{"verifications":[]}`)
			return
		}
		w.WriteHeader(403)
	}))
	reg, err := c.Registration(context.Background(), []provider.Number{{E164: "+12125550100"}, {ID: "PN1", E164: "+18005550100"}})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Numbers[0].State != provider.RegUnknown || reg.Numbers[1].State != provider.RegUnregistered {
		t.Errorf("numbers = %+v", reg.Numbers)
	}
	if strings.Join(reg.Unavailable, ",") != "brands,campaigns" {
		t.Errorf("unavailable = %v", reg.Unavailable)
	}
	bad := regClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	if _, err := bad.Registration(context.Background(), nil); !errors.Is(err, provider.ErrAuth) {
		t.Errorf("err = %v", err)
	}
}
