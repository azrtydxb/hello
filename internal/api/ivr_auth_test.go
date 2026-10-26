package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// TestIVRPagingNeedsAdmin: a menu with a paging destination lets anyone who
// reaches it page the group, so creating or replacing one needs what the
// paging groups need (admin role and scope). It fails if an operator, or an
// admin token without the admin scope, can save such a menu, or if a menu
// without paging is refused to an operator before the store is reached.
func TestIVRPagingNeedsAdmin(t *testing.T) {
	s := &server{}
	paging := `{"name":"main","greetingId":1,"options":[{"digit":"1","kind":"paging","target":"*60"}]}`
	timeoutPaging := `{"name":"main","greetingId":1,"timeout":{"kind":"paging","target":"*60"}}`
	for name, tc := range map[string]struct {
		body   string
		actor  auth.Actor
		method string
	}{
		"operator create option": {paging, auth.Actor{Role: auth.RoleOperator, Scopes: auth.AllScopes}, http.MethodPost},
		"operator put timeout":   {timeoutPaging, auth.Actor{Role: auth.RoleOperator, Scopes: auth.AllScopes}, http.MethodPut},
		"admin without scope":    {paging, auth.Actor{Role: auth.RoleAdmin, Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}}, http.MethodPost},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/v1/ivr-menus/1", strings.NewReader(tc.body))
			r.SetPathValue("id", "1")
			r = r.WithContext(auth.WithActor(r.Context(), tc.actor))
			w := httptest.NewRecorder()
			if tc.method == http.MethodPost {
				s.createIVRMenu(w, r)
			} else {
				s.putIVRMenu(w, r)
			}
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d (%s); want 403", w.Code, w.Body)
			}
		})
	}
	allowed := func(a auth.Actor, in store.IVRMenuInput) bool {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r = r.WithContext(auth.WithActor(r.Context(), a))
		return pagingAllowed(httptest.NewRecorder(), r, in)
	}
	pages := ivrBody{}.toInput()
	pages.Options = []store.IVROption{{Digit: "1", Kind: store.IVRPaging, Target: "*60"}}
	if !allowed(auth.Actor{Role: auth.RoleAdmin, Scopes: auth.AllScopes}, pages) {
		t.Fatal("an admin may not save a paging menu")
	}
	plain := ivrBody{}.toInput()
	plain.Options = []store.IVROption{{Digit: "1", Kind: store.IVRExtension, Target: "101"}}
	if !allowed(auth.Actor{Role: auth.RoleOperator, Scopes: auth.AllScopes}, plain) {
		t.Fatal("an operator may not save a menu without paging")
	}
}
