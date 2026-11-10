package api

import (
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// TestValidateTrunkAnonymous fails if a bad anonymous policy, identity header
// or asserted identity is accepted, or a good (or empty, defaulted) one refused.
func TestValidateTrunkAnonymous(t *testing.T) {
	base := func() store.TrunkInput {
		return store.TrunkInput{Name: "t1", Mode: "ip", RegisterExpires: 3600, OptionsInterval: 30, SourceCIDRs: []string{}}
	}
	has := func(f fieldErrs, path string) bool {
		for _, e := range f {
			if e.Path == path {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name    string
		mut     func(*store.TrunkInput)
		badPath string
	}{
		{"defaults", func(*store.TrunkInput) {}, ""},
		{"always ppi", func(in *store.TrunkInput) {
			in.AnonymousPolicy, in.IdentityHeader, in.AssertedIdentity = "always", "ppi", "+97140000999"
		}, ""},
		{"bad policy", func(in *store.TrunkInput) { in.AnonymousPolicy = "sometimes" }, "anonymousPolicy"},
		{"bad header", func(in *store.TrunkInput) { in.IdentityHeader = "rpid" }, "identityHeader"},
		{"bad identity", func(in *store.TrunkInput) { in.AssertedIdentity = "sip:x@y" }, "assertedIdentity"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := base()
			c.mut(&in)
			f := validateTrunk(&in, false)
			if c.badPath == "" && (has(f, "anonymousPolicy") || has(f, "identityHeader") || has(f, "assertedIdentity")) {
				t.Fatalf("valid anonymous settings refused: %v", f)
			}
			if c.badPath != "" && !has(f, c.badPath) {
				t.Fatalf("%s accepted: %v", c.badPath, f)
			}
		})
	}
}
