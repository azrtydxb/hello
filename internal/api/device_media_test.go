package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// deviceMediaStub records what PATCH /devices/{id} hands the store; every
// other Store method would panic on the nil embedded interface.
type deviceMediaStub struct {
	Store
	got   store.DeviceMedia
	calls int
}

func (d *deviceMediaStub) UpdateDevice(_ context.Context, _ string, id int64, _ *bool, _ *int64, m store.DeviceMedia) (store.Device, error) {
	d.calls++
	d.got = m
	return store.Device{ID: id, SRTP: "optional", SRTPKeyEx: "dtls", MediaICE: true}, nil
}

// TestUpdateDeviceMediaFields fails if PATCH /devices/{id} does not validate
// and pass on srtpKeyExchange and mediaIce (phone-feature-parity S-16): an
// unknown key exchange or an empty body is a 400 that never reaches the store,
// a valid one is normalised and reaches it, and the response carries both.
func TestUpdateDeviceMediaFields(t *testing.T) {
	do := func(body string) (*deviceMediaStub, *httptest.ResponseRecorder) {
		stub := &deviceMediaStub{}
		s := &server{Store: stub}
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/devices/3", strings.NewReader(body))
		r.SetPathValue("id", "3")
		w := httptest.NewRecorder()
		s.updateDevice(w, r)
		return stub, w
	}
	for _, bad := range []string{`{"srtpKeyExchange":"zrtp"}`, `{"srtpKeyExchange":""}`, `{}`, `{"srtp":"sometimes"}`} {
		stub, w := do(bad)
		if w.Code != http.StatusBadRequest || stub.calls != 0 {
			t.Errorf("%s: status %d, store called %d times; want 400 and none", bad, w.Code, stub.calls)
		}
	}
	stub, w := do(`{"srtpKeyExchange":" DTLS ","mediaIce":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if stub.got.KeyEx == nil || *stub.got.KeyEx != "dtls" || stub.got.ICE == nil || !*stub.got.ICE || stub.got.SRTP != nil {
		t.Fatalf("the store got %+v", stub.got)
	}
	if body := w.Body.String(); !strings.Contains(body, `"srtpKeyExchange":"dtls"`) || !strings.Contains(body, `"mediaIce":true`) {
		t.Fatalf("response lacks the fields: %s", body)
	}
	if stub, w := do(`{"mediaIce":false}`); w.Code != http.StatusOK || stub.got.ICE == nil || *stub.got.ICE {
		t.Fatalf("mediaIce false was not passed on (%d)", w.Code)
	}
}
