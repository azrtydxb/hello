package scim

import (
	"strings"
)

// PATCH (RFC 7644 section 3.5.2): add, replace and remove, with or without a
// path, value-path filters and sub-attributes. What identity providers send
// in practice is accepted: operation names in any case (Entra sends "Add",
// "Replace"), string booleans ("False"), a no-path object whose keys are
// paths ("name.givenName", an extension-qualified attribute), the id that
// Okta repeats in a no-path replace, a bare id as the value of manager, a
// remove of members that lists the values to remove, and a replace of a
// filtered value that does not exist yet (it is added).

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type patchBody struct {
	Schemas    []string  `json:"schemas"`
	Operations []patchOp `json:"Operations"`
}

// maxPatchOps bounds one PATCH.
const maxPatchOps = 10000

// applyPatch applies the operations to a resource document in place.
func applyPatch(typ string, doc map[string]any, ops []patchOp) *Error {
	if len(ops) == 0 {
		return errInvalidSyntax("a PATCH needs at least one operation")
	}
	if len(ops) > maxPatchOps {
		return &Error{Status: 413, Type: typeTooMany, Detail: "too many operations"}
	}
	for _, o := range ops {
		op := strings.ToLower(strings.TrimSpace(o.Op))
		switch op {
		case "add", "replace", "remove":
		default:
			return errInvalidSyntax("unknown operation " + o.Op)
		}
		if strings.TrimSpace(o.Path) == "" {
			if op == "remove" {
				return errNoTarget("remove needs a path")
			}
			m, ok := o.Value.(map[string]any)
			if !ok {
				return errInvalidValue("an operation without a path needs an object value")
			}
			for k, v := range m {
				switch strings.ToLower(k) {
				case "id", "meta", "schemas":
					continue
				}
				if ext := extensionByName(typ, k); ext != nil {
					em, ok := v.(map[string]any)
					if !ok {
						return errInvalidValue(ext.ID + " must be an object")
					}
					for sk, sv := range em {
						if err := applyOne(typ, doc, op, ext.ID+":"+sk, sv); err != nil {
							return err
						}
					}
					continue
				}
				if err := applyOne(typ, doc, op, k, v); err != nil {
					return err
				}
			}
			continue
		}
		if err := applyOne(typ, doc, op, o.Path, o.Value); err != nil {
			return err
		}
	}
	for _, e := range extensionsOf(typ) {
		if m, ok := doc[e.ID].(map[string]any); ok && len(m) == 0 {
			delete(doc, e.ID)
		}
	}
	return nil
}

// target finds where an attribute lives: the map that holds it (created for
// an extension when create is set), its canonical name and its definition.
func target(typ string, doc map[string]any, name string, create bool) (map[string]any, *attr, *Error) {
	for _, e := range extensionsOf(typ) {
		pre := e.ID + ":"
		if len(name) > len(pre) && strings.EqualFold(name[:len(pre)], pre) {
			a := sub(e.Attrs, name[len(pre):])
			if a == nil {
				return nil, nil, nil
			}
			ext, _ := doc[e.ID].(map[string]any)
			if ext == nil {
				if !create {
					return nil, a, nil
				}
				ext = map[string]any{}
				doc[e.ID] = ext
			}
			return ext, a, nil
		}
	}
	switch strings.ToLower(name) {
	case "id", "meta", "schemas":
		return nil, nil, errMutability(name + " cannot be changed")
	case "externalid":
		return doc, &attr{Name: "externalId", Type: tString}, nil
	}
	a := sub(coreAttrs(typ), name)
	if a == nil {
		return nil, nil, nil
	}
	if a.Mutability == "readOnly" {
		return nil, nil, errMutability(a.Name + " is read-only")
	}
	return doc, a, nil
}

func applyOne(typ string, doc map[string]any, op, path string, value any) *Error {
	pp, perr := parsePath(path)
	if perr != nil {
		return perr
	}
	holder, a, err := target(typ, doc, pp.attr, op != "remove")
	if err != nil {
		return err
	}
	if a == nil {
		// An attribute Hello does not keep (a custom extension of the IdP):
		// ignored, like in a POST.
		return nil
	}
	if holder == nil {
		// remove in an extension that is not there: nothing to do.
		return nil
	}
	var sa *attr
	if pp.sub != "" {
		if a.Type != tComplex {
			return errInvalidPath(a.Name + " has no sub-attributes")
		}
		if sa = sub(a.Sub, pp.sub); sa == nil {
			return errInvalidPath(a.Name + " has no sub-attribute " + pp.sub)
		}
	}
	if pp.filter != nil && !a.Multi {
		return errInvalidPath(a.Name + " is not multi-valued")
	}
	if op == "remove" && pp.filter == nil && pp.sub == "" && a.Required {
		return errMutability(a.Name + " is required")
	}
	switch {
	case a.Multi:
		return patchMulti(holder, a, sa, pp.filter, op, value)
	case a.Type == tComplex:
		return patchComplex(holder, a, sa, op, value)
	}
	if op == "remove" {
		delete(holder, a.Name)
		return nil
	}
	v, cerr := coerce(*a, value, a.Name)
	if cerr != nil {
		return cerr
	}
	if v == nil || v == "" && a.Required {
		if a.Required {
			return errInvalidValue(a.Name + " is required")
		}
		delete(holder, a.Name)
		return nil
	}
	holder[a.Name] = v
	return nil
}

func patchComplex(holder map[string]any, a, sa *attr, op string, value any) *Error {
	cur, _ := holder[a.Name].(map[string]any)
	if op == "remove" {
		if sa == nil {
			delete(holder, a.Name)
		} else if cur != nil {
			delete(cur, sa.Name)
			if len(cur) == 0 {
				delete(holder, a.Name)
			}
		}
		return nil
	}
	if cur == nil {
		cur = map[string]any{}
	}
	if sa != nil {
		v, err := coerce(*sa, value, a.Name+"."+sa.Name)
		if err != nil {
			return err
		}
		if v == nil {
			delete(cur, sa.Name)
		} else {
			cur[sa.Name] = v
		}
	} else {
		if s, ok := value.(string); ok && sub(a.Sub, "value") != nil {
			// manager: "<id>" is manager.value.
			value = map[string]any{"value": s}
		}
		if value == nil {
			delete(holder, a.Name)
			return nil
		}
		v, err := coerce(*a, value, a.Name)
		if err != nil {
			return err
		}
		m, _ := v.(map[string]any)
		for k, x := range m {
			cur[k] = x
		}
	}
	if len(cur) == 0 {
		delete(holder, a.Name)
		return nil
	}
	holder[a.Name] = cur
	return nil
}

func sameValue(x, y map[string]any) bool {
	a, _ := x["value"].(string)
	b, _ := y["value"].(string)
	return a != "" && a == b
}

// dedupe keeps the last element of each value and at most one primary.
func dedupe(items []any) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		replaced := false
		for i, o := range out {
			if om, _ := o.(map[string]any); sameValue(om, m) {
				merged := map[string]any{}
				for k, v := range om {
					merged[k] = v
				}
				for k, v := range m {
					merged[k] = v
				}
				out[i] = merged
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, m)
		}
	}
	primary := -1
	for i := len(out) - 1; i >= 0; i-- {
		m := out[i].(map[string]any)
		if p, _ := m["primary"].(bool); p {
			if primary >= 0 {
				delete(m, "primary")
			} else {
				primary = i
			}
		}
	}
	return out
}

// filterEqualities are the attribute = value pairs an and-only equality
// filter states: the attributes a new element made from the filter gets.
func filterEqualities(n node, out map[string]any) bool {
	switch x := n.(type) {
	case cmpNode:
		if x.op != "eq" || strings.Contains(x.path, ".") || strings.Contains(x.path, ":") {
			return false
		}
		out[x.path] = x.val
		return true
	case andNode:
		return filterEqualities(x.l, out) && filterEqualities(x.r, out)
	}
	return false
}

func patchMulti(holder map[string]any, a, sa *attr, f node, op string, value any) *Error {
	items := elements(holder[a.Name])
	set := func(list []any) {
		list = dedupe(list)
		if len(list) == 0 {
			delete(holder, a.Name)
		} else {
			holder[a.Name] = list
		}
	}
	if f == nil {
		switch {
		case op == "remove" && sa == nil:
			if value == nil {
				delete(holder, a.Name)
				return nil
			}
			// A remove that lists values (Entra's members removal).
			gone, err := coerce(*a, value, a.Name)
			if err != nil {
				return err
			}
			var keep []any
			for _, it := range items {
				m, _ := it.(map[string]any)
				drop := false
				for _, g := range elements(gone) {
					if gm, _ := g.(map[string]any); sameValue(m, gm) {
						drop = true
					}
				}
				if !drop {
					keep = append(keep, it)
				}
			}
			set(keep)
			return nil
		case op == "remove":
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					delete(m, sa.Name)
				}
			}
			set(items)
			return nil
		case sa != nil:
			v, err := coerce(*sa, value, a.Name+"."+sa.Name)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				items = []any{map[string]any{}}
			}
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					if v == nil {
						delete(m, sa.Name)
					} else {
						m[sa.Name] = v
					}
				}
			}
			set(items)
			return nil
		}
		v, err := coerce(*a, value, a.Name)
		if err != nil {
			return err
		}
		if op == "replace" {
			set(elements(v))
			return nil
		}
		set(append(items, elements(v)...))
		return nil
	}
	var matched []int
	for i, it := range items {
		if m, ok := it.(map[string]any); ok && f.eval(m) {
			matched = append(matched, i)
		}
	}
	if op == "remove" {
		var keep []any
		for i, it := range items {
			hit := false
			for _, j := range matched {
				hit = hit || i == j
			}
			switch {
			case !hit:
				keep = append(keep, it)
			case sa != nil:
				m := it.(map[string]any)
				delete(m, sa.Name)
				if len(m) > 0 {
					keep = append(keep, m)
				}
			}
		}
		set(keep)
		return nil
	}
	var patch map[string]any
	if sa != nil {
		v, err := coerce(*sa, value, a.Name+"."+sa.Name)
		if err != nil {
			return err
		}
		patch = map[string]any{sa.Name: v}
	} else {
		if l, ok := value.([]any); ok && len(l) == 1 {
			value = l[0]
		}
		v, err := coerceOne(*a, value, a.Name)
		if err != nil {
			return err
		}
		patch, _ = v.(map[string]any)
	}
	if len(matched) == 0 {
		// The filtered value does not exist: make it from the filter's
		// equalities (Entra replaces emails[type eq "work"].value of a user
		// with no work address).
		eqs := map[string]any{}
		if !filterEqualities(f, eqs) {
			return errNoTarget("no value matches the filter")
		}
		elem := map[string]any{}
		for k, v := range eqs {
			if s := sub(a.Sub, k); s != nil && v != nil {
				cv, err := coerceOne(*s, v, a.Name+"."+s.Name)
				if err != nil {
					return err
				}
				elem[s.Name] = cv
			}
		}
		for k, v := range patch {
			if v != nil {
				elem[k] = v
			}
		}
		set(append(items, elem))
		return nil
	}
	for _, j := range matched {
		m := items[j].(map[string]any)
		for k, v := range patch {
			if v == nil {
				delete(m, k)
			} else {
				m[k] = v
			}
		}
	}
	set(items)
	return nil
}
