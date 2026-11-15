package scim

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
)

// Resource is a pushed User or Group as stored. Name is the userName of a
// User or the displayName of a Group; Data holds every other attribute but
// externalId, in canonical case (never a password).
type Resource struct {
	ID          string
	ConnectorID int64
	Type        string
	Name        string
	ExternalID  string
	Data        map[string]any
	Version     int64
	Deleted     bool
	Created     time.Time
	Updated     time.Time
}

func nameAttr(typ string) string {
	if typ == TypeUser {
		return "userName"
	}
	return "displayName"
}

// doc is the resource as one attribute map: Data with the name and the
// externalId put back. members of a Group are added by the caller.
func (r Resource) doc() map[string]any {
	d := make(map[string]any, len(r.Data)+2)
	for k, v := range r.Data {
		d[k] = cloneValue(v)
	}
	d[nameAttr(r.Type)] = r.Name
	if r.ExternalID != "" {
		d["externalId"] = r.ExternalID
	}
	return d
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = cloneValue(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = cloneValue(e)
		}
		return a
	}
	return v
}

// fromDoc splits an attribute map back into the resource's columns. members
// are not part of Data; the caller takes them first.
func fromDoc(typ string, d map[string]any) (name, externalID string, data map[string]any, err *Error) {
	data = map[string]any{}
	for k, v := range d {
		switch k {
		case nameAttr(typ):
			name, _ = v.(string)
		case "externalId":
			externalID, _ = v.(string)
		case "members", "groups":
		default:
			data[k] = v
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", nil, errInvalidValue(nameAttr(typ) + " is required")
	}
	if len(name) > 512 {
		return "", "", nil, errInvalidValue(nameAttr(typ) + " is longer than 512 characters")
	}
	if len(externalID) > 512 {
		return "", "", nil, errInvalidValue("externalId is longer than 512 characters")
	}
	return name, externalID, data, nil
}

// extensionByName finds an extension schema of the type by its URN.
func extensionByName(typ, urn string) *schemaDef {
	for _, e := range extensionsOf(typ) {
		if strings.EqualFold(e.ID, urn) {
			e := e
			return &e
		}
	}
	return nil
}

// splitURN splits a fully qualified attribute name ("<schema urn>:<attr>")
// into its extension schema and attribute, for a known extension.
func splitURN(typ, name string) (*schemaDef, string) {
	for _, e := range extensionsOf(typ) {
		if len(name) > len(e.ID)+1 && strings.EqualFold(name[:len(e.ID)], e.ID) && name[len(e.ID)] == ':' {
			e := e
			return &e, name[len(e.ID)+1:]
		}
	}
	return nil, ""
}

// normalize turns a body (a POST or PUT, or the object of a PATCH value) into
// the canonical attribute map: attribute names in canonical case, values of
// their type, read-only and unknown attributes dropped, the password never
// kept. The core schema's attributes may also be written with its URN.
func normalize(typ string, in map[string]any) (map[string]any, *Error) {
	out := map[string]any{}
	core := coreSchema(typ)
	for k, v := range in {
		key := k
		if len(key) > len(core)+1 && strings.EqualFold(key[:len(core)], core) && key[len(core)] == ':' {
			key = key[len(core)+1:]
		}
		switch strings.ToLower(key) {
		case "schemas", "id", "meta", "password", "groups":
			continue
		case "externalid":
			s, err := coerceString(v)
			if err != nil {
				return nil, errInvalidValue("externalId must be a string")
			}
			if s != "" {
				out["externalId"] = s
			}
			continue
		}
		if ext := extensionByName(typ, key); ext != nil {
			m, ok := v.(map[string]any)
			if v == nil {
				continue
			}
			if !ok {
				return nil, errInvalidValue(ext.ID + " must be an object")
			}
			sub, err := normalizeAttrs(ext.Attrs, m, ext.ID+":")
			if err != nil {
				return nil, err
			}
			mergeExt(out, ext.ID, sub)
			continue
		}
		if ext, name := splitURN(typ, key); ext != nil {
			sub, err := normalizeAttrs(ext.Attrs, map[string]any{name: v}, ext.ID+":")
			if err != nil {
				return nil, err
			}
			mergeExt(out, ext.ID, sub)
			continue
		}
		a := sub(coreAttrs(typ), key)
		if a == nil || a.Mutability == "readOnly" {
			continue
		}
		cv, err := coerce(*a, v, a.Name)
		if err != nil {
			return nil, err
		}
		if cv != nil {
			out[a.Name] = cv
		}
	}
	return out, nil
}

func mergeExt(out map[string]any, urn string, sub map[string]any) {
	have, _ := out[urn].(map[string]any)
	if have == nil {
		have = map[string]any{}
	}
	for k, v := range sub {
		have[k] = v
	}
	if len(have) > 0 {
		out[urn] = have
	}
}

func normalizeAttrs(defs []attr, in map[string]any, path string) (map[string]any, *Error) {
	out := map[string]any{}
	for k, v := range in {
		a := sub(defs, k)
		if a == nil || a.Mutability == "readOnly" && a.Name != "displayName" {
			continue
		}
		cv, err := coerce(*a, v, path+a.Name)
		if err != nil {
			return nil, err
		}
		if cv != nil {
			out[a.Name] = cv
		}
	}
	return out, nil
}

// coerce converts a value to an attribute's type; nil means unset.
func coerce(a attr, v any, path string) (any, *Error) {
	if v == nil {
		return nil, nil
	}
	if a.Multi {
		var items []any
		switch x := v.(type) {
		case []any:
			items = x
		default:
			items = []any{x}
		}
		out := make([]any, 0, len(items))
		for _, it := range items {
			if it == nil {
				continue
			}
			if a.Type == tComplex {
				if s, ok := it.(string); ok {
					// A bare value of a multi-valued complex attribute is its
					// value sub-attribute.
					it = map[string]any{"value": s}
				}
			}
			cv, err := coerceOne(a, it, path)
			if err != nil {
				return nil, err
			}
			if cv != nil {
				out = append(out, cv)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	return coerceOne(a, v, path)
}

func coerceOne(a attr, v any, path string) (any, *Error) {
	switch a.Type {
	case tBoolean:
		b, ok := coerceBool(v)
		if !ok {
			return nil, errInvalidValue(path + " must be a boolean")
		}
		return b, nil
	case tComplex:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errInvalidValue(path + " must be an object")
		}
		out, err := normalizeAttrs(a.Sub, m, path+".")
		if err != nil {
			return nil, err
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	default:
		s, err := coerceString(v)
		if err != nil {
			return nil, errInvalidValue(path + " must be a string")
		}
		return s, nil
	}
}

// coerceBool reads a boolean, or the string forms identity providers send
// ("True", "False", "true", "false").
func coerceBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func coerceString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return "", errInvalidValue("not a string")
}

// Attribute keys the mappings read (directory I-3). They are the names the
// connector's mapping sources use, lower-cased by the engine.
const (
	AttrPBXExtension = SchemaPBX + ":extension"
)

// sourceUser is the directory engine's view of a SCIM user: a flat attribute
// bag (I-2, I-3). Multi-valued attributes give "<attr>" (every value, the
// primary one first), "<attr>[primary]" and "<attr>[<type>]"; complex ones
// "<attr>.<sub>"; extension attributes their fully qualified name. groups are
// the ids of the user's groups and their externalIds.
func sourceUser(r Resource, groups []groupRef) directory.SourceUser {
	attrs := map[string][]string{}
	add := func(k, v string) {
		if v == "" {
			return
		}
		k = strings.ToLower(k)
		attrs[k] = append(attrs[k], v)
	}
	add("id", r.ID)
	add("userName", r.Name)
	add("externalId", r.ExternalID)
	keys := make([]string, 0, len(r.Data))
	for k := range r.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := r.Data[k]
		if ext := extensionByName(TypeUser, k); ext != nil {
			m, _ := v.(map[string]any)
			for sk, sv := range m {
				switch x := sv.(type) {
				case string:
					add(ext.ID+":"+sk, x)
				case map[string]any:
					if s, ok := x["value"].(string); ok {
						add(ext.ID+":"+sk, s)
					}
					for ssk, ssv := range x {
						if s, ok := ssv.(string); ok {
							add(ext.ID+":"+sk+"."+ssk, s)
						}
					}
				}
			}
			continue
		}
		switch x := v.(type) {
		case string:
			add(k, x)
		case bool:
			add(k, strconv.FormatBool(x))
		case map[string]any:
			for sk, sv := range x {
				if s, ok := sv.(string); ok {
					add(k+"."+sk, s)
				}
			}
		case []any:
			flattenMulti(k, x, add)
		}
	}
	if _, ok := r.Data["active"]; !ok {
		// A user the IdP says nothing about is active (RFC 7643: active is
		// optional), so a mapping on active never disables it by omission.
		add("active", "true")
	}
	if n, ok := r.Data["name"].(map[string]any); ok {
		given, _ := n["givenName"].(string)
		family, _ := n["familyName"].(string)
		add("name.full", strings.TrimSpace(given+" "+family))
	}
	u := directory.SourceUser{ExternalID: r.ID, Attrs: attrs}
	for _, g := range groups {
		u.Groups = append(u.Groups, g.ID)
		if g.ExternalID != "" {
			u.Groups = append(u.Groups, g.ExternalID)
		}
	}
	return u
}

func flattenMulti(k string, items []any, add func(k, v string)) {
	var primary []string
	var rest []string
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		v, _ := m["value"].(string)
		if v == "" {
			continue
		}
		if p, _ := m["primary"].(bool); p {
			primary = append(primary, v)
			add(k+"[primary]", v)
		} else {
			rest = append(rest, v)
		}
		if t, _ := m["type"].(string); t != "" {
			add(k+"["+strings.ToLower(t)+"]", v)
		}
	}
	for _, v := range append(primary, rest...) {
		add(k, v)
	}
}

// DefaultMappings are the suggested mappings of a SCIM connector (I-3): the
// userName is the username, the primary (then work, then any) e-mail the
// e-mail, displayName (then name.formatted, then given and family name) the
// display name, Hello's extension attribute the extension, and active the
// account state.
func DefaultMappings() directory.Mappings {
	return directory.Mappings{
		Username:    directory.AttrMap{Sources: []string{"userName"}},
		Email:       directory.AttrMap{Sources: []string{"emails[primary]", "emails[work]", "emails"}},
		DisplayName: directory.AttrMap{Sources: []string{"displayName", "name.formatted", "name.full"}},
		Extension:   directory.AttrMap{Sources: []string{AttrPBXExtension}},
		Active:      directory.ActiveMap{Attribute: "active", Mode: directory.ActiveBool},
	}.Normalize()
}
