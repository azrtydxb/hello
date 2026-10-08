package proposal

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
)

// Allowlist is the operation ids a proposal may carry (spec S-11). Anything
// else, including every admin- or secrets-scoped operation and every
// operation whose response carries a secret, is refused.
var Allowlist = []string{
	"updateExtension", "updateDevice",
	"createRingGroup", "updateRingGroup", "deleteRingGroup",
	"putFeatureCodes",
	"createOutboundRoute", "updateOutboundRoute", "deleteOutboundRoute",
	"createInboundRoute", "updateInboundRoute", "deleteInboundRoute",
	"updateTrunk", "updatePhone",
}

// deletes are the allowlisted operations that remove a resource: only ring
// groups and routes (plan ai-agent, open question 3). They are marked in the
// proposal and need the applier's explicit confirmation.
var deletes = []string{"deleteRingGroup", "deleteOutboundRoute", "deleteInboundRoute"}

// Allowed reports whether id is on the allowlist.
func Allowed(id string) bool { return slices.Contains(Allowlist, id) }

// IsDelete reports whether id is an allowlisted delete.
func IsDelete(id string) bool { return slices.Contains(deletes, id) }

// CheckDocument fails if an allowlisted operation is missing from the
// document, is not a write-scoped operation below admin, deletes something
// other than a ring group or a route, or returns a secret (spec S-11).
func CheckDocument(spec *apispec.Spec) error {
	ops := map[string]apispec.Operation{}
	for _, op := range spec.Operations() {
		ops[op.ID] = op
	}
	for _, id := range Allowlist {
		op, ok := ops[id]
		switch {
		case !ok:
			return fmt.Errorf("allowlisted operation %q is not in the document", id)
		case op.Scope != auth.ScopeWrite:
			return fmt.Errorf("allowlisted operation %q is scoped %q, not write", id, op.Scope)
		case op.Role == auth.RoleAdmin:
			return fmt.Errorf("allowlisted operation %q needs the admin role", id)
		case len(op.Secrets) > 0:
			return fmt.Errorf("allowlisted operation %q returns a secret", id)
		case (op.Method == http.MethodDelete) != IsDelete(id):
			return fmt.Errorf("allowlisted operation %q: %s is not an allowed delete", id, op.Method)
		}
	}
	return nil
}
