package proposal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Fingerprint is source + ":" + the first 16 hex digits of the SHA-256 of
// the canonical JSON of the actions (spec S-14). Only what the operator
// would apply counts (operation, path parameters, body), not the read
// state, so a refreshed proposal keeps its fingerprint; object keys are
// sorted and formatting does not matter.
func Fingerprint(source string, actions []Action) string {
	type core struct {
		OperationID string            `json:"operationId"`
		PathParams  map[string]string `json:"pathParams"`
		Body        any               `json:"body"`
	}
	cs := make([]core, len(actions))
	for i, a := range actions {
		body, _ := decode(a.Body)
		if a.OperationID == BatchOp {
			body = batchCore(a.Body)
		}
		cs[i] = core{a.OperationID, a.PathParams, body}
		if cs[i].PathParams == nil {
			cs[i].PathParams = map[string]string{}
		}
	}
	raw, _ := json.Marshal(cs) // maps marshal with sorted keys
	sum := sha256.Sum256(raw)
	return source + ":" + hex.EncodeToString(sum[:])[:16]
}

// Targets names the existing resources the actions change, as
// "<resource>/<id>" (or "<resource>" for a singleton such as the feature
// codes). A create has no target yet and names none, so a newer proposal
// that adds another resource never supersedes an older one.
func Targets(actions []Action) []string {
	var out []string
	for _, a := range actions {
		if a.OperationID == BatchOp {
			// A batch names the objects its operations change.
			out = append(out, batchTargets(a.Body)...)
			continue
		}
		if t, ok := target(a.OperationID, a.PathParams); ok {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// target is the resource an operation changes, or false for a create.
func target(operationID string, params map[string]string) (string, bool) {
	if strings.HasPrefix(operationID, "create") {
		return "", false
	}
	res := operationID
	for _, p := range []string{"update", "delete", "put"} {
		res = strings.TrimPrefix(res, p)
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t := res
	for _, k := range keys {
		t += "/" + params[k]
	}
	return t, true
}

func intersects(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if set[y] {
			return true
		}
	}
	return false
}
