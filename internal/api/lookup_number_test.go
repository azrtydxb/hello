package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestLookupNumber (spec integrations I-43) fails if the number lookup does
// not name an extension or company contact for any credential, if a user's
// personal contact names a number for anybody but its owner (another user,
// an administrator or a service-account-style token), if the personal book
// beats the company book, if a missing number is accepted, or if a
// credential without the read scope gets through.
func TestLookupNumber(t *testing.T) {
	e := newEnv(t, noLive{})
	alice := e.login()
	bob, bobID := e.asRole("bob", auth.RoleViewer)
	carol, _ := e.asRole("carol", auth.RoleViewer)
	user, userID := e.asRole("uma", auth.RoleUser)

	e101 := alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e102 := alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	alice.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(bobID, 10), map[string]any{"extensionId": e101["id"]})
	alice.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(userID, 10), map[string]any{"extensionId": e102["id"]})
	company := bookID(t, alice, "company")
	alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks/"+company+"/entries", entryBody("Corp", "+41791234567"))

	look := func(c *client, number string) map[string]any {
		t.Helper()
		return c.must(http.StatusOK, "GET", "/api/v1/lookup/number?number="+url2(number), nil).json(t)
	}

	t.Run("extensions and the company book answer every credential", func(t *testing.T) {
		for name, c := range map[string]*client{"admin": alice, "viewer": carol, "user": user} {
			if r := look(c, "+41 79 123 45 67"); r["result"] != "match" || r["name"] != "T Corp" || r["source"] != "company" || r["via"] != "exact" {
				t.Errorf("%s, company: %v", name, r)
			}
			if r := look(c, "101"); r["result"] != "match" || r["source"] != "extensions" || r["name"] != "Sales" {
				t.Errorf("%s, extension: %v", name, r)
			}
			if r := look(c, "+41000000000"); r["result"] != "none" {
				t.Errorf("%s, unknown: %v", name, r)
			}
			if r := look(c, "anonymous"); r["result"] != "unresolvable" {
				t.Errorf("%s, anonymous: %v", name, r)
			}
		}
	})

	t.Run("a personal contact names a number for its owner only", func(t *testing.T) {
		bob.must(http.StatusCreated, "POST", "/api/v1/me/contacts", entryBody("Bobs Friend", "+3225550101"))
		bob.must(http.StatusCreated, "POST", "/api/v1/me/contacts", entryBody("Corp Private", "+41791234567"))
		r := look(bob, "+32 2 555 01 01")
		if r["result"] != "match" || r["source"] != "personal" || r["name"] != "T Bobs Friend" {
			t.Errorf("owner: %v", r)
		}
		if !strings.Contains(strings.Join(searched(r), ","), "personal") {
			t.Errorf("the personal book is not listed as searched: %v", r["searched"])
		}
		// The company book still wins over a personal contact on the same number.
		if r := look(bob, "+41791234567"); r["name"] != "T Corp" || r["source"] != "company" {
			t.Errorf("company beats personal: %v", r)
		}
		for name, c := range map[string]*client{"another user": user, "viewer without a book": carol, "administrator": alice} {
			if r := look(c, "+3225550101"); r["result"] != "none" || strings.Contains(fmt.Sprint(r), "Friend") {
				t.Errorf("%s sees bob's contact: %v", name, r)
			}
		}
	})

	t.Run("an ambiguous suffix in the personal book names nobody", func(t *testing.T) {
		user.must(http.StatusCreated, "POST", "/api/v1/me/contacts", entryBody("Twin A", "+33612345678"))
		user.must(http.StatusCreated, "POST", "/api/v1/me/contacts", entryBody("Twin B", "+49612345678"))
		if r := look(user, "+34612345678"); r["result"] != "ambiguous" || r["name"] != nil || r["matches"].(float64) != 2 {
			t.Errorf("ambiguous: %v", r)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		alice.must(http.StatusBadRequest, "GET", "/api/v1/lookup/number", nil)
		alice.must(http.StatusBadRequest, "GET", "/api/v1/lookup/number?number="+strings.Repeat("1", 129), nil)
		// A token that holds only the events scope cannot read contacts.
		tok := alice.must(http.StatusCreated, "POST", "/api/v1/tokens", map[string]any{"name": "ev", "scopes": []string{"events"}}).json(t)["token"].(string)
		b := e.client()
		b.bearer = tok
		if r := b.do("GET", "/api/v1/lookup/number?number=101", nil); r.code != http.StatusForbidden {
			t.Errorf("events-only token: %d %s", r.code, r.body)
		}
		e.client().must(http.StatusUnauthorized, "GET", "/api/v1/lookup/number?number=101", nil)
	})
}

func searched(r map[string]any) []string {
	var out []string
	for _, v := range r["searched"].([]any) {
		out = append(out, v.(string))
	}
	return out
}
