package directory

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Transform steps for an attribute mapping (I-3). Applied in order.
const (
	TransformDigits = "digits" // strip every non-digit
	TransformLast   = "last"   // keep the last N characters
	TransformRegex  = "regex"  // the first capture group of Pattern
)

// Active mapping modes.
const (
	ActiveAlways  = "always" // every fetched user is active
	ActiveUAC     = "uac"    // userAccountControl bit 2 set means disabled
	ActiveBool    = "bool"   // the attribute reads true/false
	ActiveNotBool = "bool_inverted"
)

// Transform is one step.
type Transform struct {
	Kind    string `json:"kind"`
	N       int    `json:"n,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

// AttrMap maps one Hello field from an ordered list of source attributes (the
// first non-empty value wins), then optional transforms.
type AttrMap struct {
	Sources    []string    `json:"sources"`
	Transforms []Transform `json:"transforms"`
}

// ActiveMap says how a source user's active state is read.
type ActiveMap struct {
	Attribute string `json:"attribute"`
	Mode      string `json:"mode"`
}

// Mappings are the per-connector attribute mappings, evaluated by one engine
// for every kind.
type Mappings struct {
	Username    AttrMap   `json:"username"`
	Email       AttrMap   `json:"email"`
	DisplayName AttrMap   `json:"displayName"`
	Extension   AttrMap   `json:"extension"`
	Active      ActiveMap `json:"active"`
}

// Mapped is a source user after the mappings.
type Mapped struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	// Extension is the extension value; ExtensionError says why a value that
	// is present is not usable (extension_invalid), empty otherwise.
	Extension      string `json:"extension"`
	ExtensionError string `json:"extensionError,omitempty"`
	Active         bool   `json:"active"`
}

var extensionRe = regexp.MustCompile(`^[0-9]{2,10}$`)

// usernameRe is what a Hello login may be (the same as the users API).
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,254}$`)

// Managed returns the Hello fields these mappings own, which become read-only
// for administrators (I-5): every mapped field, and the role when group rules
// give one.
func (m Mappings) Managed(rules GroupRules) []string {
	var out []string
	add := func(name string, on bool) {
		if on {
			out = append(out, name)
		}
	}
	add("username", len(m.Username.Sources) > 0)
	add("email", len(m.Email.Sources) > 0)
	add("displayName", len(m.DisplayName.Sources) > 0)
	add("extension", len(m.Extension.Sources) > 0)
	add("role", len(rules.Roles) > 0)
	add("enabled", m.Active.Mode != "")
	return out
}

// Normalize returns the mappings with every list non-nil, so they marshal as
// arrays.
func (m Mappings) Normalize() Mappings {
	fix := func(a AttrMap) AttrMap {
		if a.Sources == nil {
			a.Sources = []string{}
		}
		if a.Transforms == nil {
			a.Transforms = []Transform{}
		}
		return a
	}
	m.Username, m.Email, m.DisplayName, m.Extension = fix(m.Username), fix(m.Email), fix(m.DisplayName), fix(m.Extension)
	return m
}

// Validate checks the mappings.
func (m Mappings) Validate() *ValidationError {
	var v ValidationError
	check := func(path string, a AttrMap) {
		if len(a.Sources) > 8 {
			v.Fields = append(v.Fields, FieldError{path + ".sources", "at most 8 attributes"})
		}
		for i, s := range a.Sources {
			if !attrNameRe.MatchString(s) {
				v.Fields = append(v.Fields, FieldError{path + ".sources[" + strconv.Itoa(i) + "]", "not an attribute name"})
			}
		}
		if len(a.Transforms) > 6 {
			v.Fields = append(v.Fields, FieldError{path + ".transforms", "at most 6 transforms"})
		}
		for i, t := range a.Transforms {
			p := path + ".transforms[" + strconv.Itoa(i) + "]"
			switch t.Kind {
			case TransformDigits:
			case TransformLast:
				if t.N < 1 || t.N > 32 {
					v.Fields = append(v.Fields, FieldError{p + ".n", "must be 1 to 32"})
				}
			case TransformRegex:
				re, err := regexp.Compile(t.Pattern)
				switch {
				case err != nil || len(t.Pattern) > 200:
					v.Fields = append(v.Fields, FieldError{p + ".pattern", "not a valid regular expression (at most 200 characters)"})
				case re.NumSubexp() != 1:
					v.Fields = append(v.Fields, FieldError{p + ".pattern", "needs exactly one capture group"})
				}
			default:
				v.Fields = append(v.Fields, FieldError{p + ".kind", "must be digits, last or regex"})
			}
		}
	}
	check("mappings.username", m.Username)
	check("mappings.email", m.Email)
	check("mappings.displayName", m.DisplayName)
	check("mappings.extension", m.Extension)
	switch m.Active.Mode {
	case "", ActiveAlways:
	case ActiveUAC, ActiveBool, ActiveNotBool:
		if !attrNameRe.MatchString(m.Active.Attribute) {
			v.Fields = append(v.Fields, FieldError{"mappings.active.attribute", "not an attribute name"})
		}
	default:
		v.Fields = append(v.Fields, FieldError{"mappings.active.mode", "must be always, uac, bool or bool_inverted"})
	}
	if len(v.Fields) == 0 {
		return nil
	}
	return &v
}

var attrNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.;:\[\]-]{0,127}$`)

// Apply evaluates the mappings on a source user. Usernames and emails are
// lower-cased only for comparison, never here.
func (m Mappings) Apply(u SourceUser) Mapped {
	out := Mapped{Active: true}
	out.Username = clean(m.Username.eval(u))
	out.Email = clean(m.Email.eval(u))
	out.DisplayName = clean(m.DisplayName.eval(u))
	if len(m.Extension.Sources) > 0 {
		out.Extension = clean(m.Extension.eval(u))
		if out.Extension != "" && !extensionRe.MatchString(out.Extension) {
			out.ExtensionError = ReasonExtensionInvalid
		}
	}
	out.Active = m.Active.eval(u)
	return out
}

// eval returns the first non-empty attribute value after the transforms. A
// transform that leaves nothing (a regex that does not match) makes the
// attribute count as empty, so the next source in the list is tried.
func (a AttrMap) eval(u SourceUser) string {
	for _, src := range a.Sources {
		for _, v := range u.Attrs[strings.ToLower(src)] {
			if r := a.transform(strings.TrimSpace(v)); r != "" {
				return r
			}
		}
	}
	return ""
}

func (a AttrMap) transform(v string) string {
	for _, t := range a.Transforms {
		switch t.Kind {
		case TransformDigits:
			v = strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, v)
		case TransformLast:
			if rs := []rune(v); len(rs) > t.N {
				v = string(rs[len(rs)-t.N:])
			}
		case TransformRegex:
			re, err := regexp.Compile(t.Pattern)
			if err != nil {
				return ""
			}
			m := re.FindStringSubmatch(v)
			if len(m) != 2 {
				return ""
			}
			v = m[1]
		}
	}
	return v
}

// eval reads the active state. A missing attribute reads as active, except
// for bool where it is unknown and also active: a directory that does not say
// is never taken as a reason to disable.
func (a ActiveMap) eval(u SourceUser) bool {
	if a.Mode == "" || a.Mode == ActiveAlways {
		return true
	}
	vals := u.Attrs[strings.ToLower(a.Attribute)]
	if len(vals) == 0 {
		return true
	}
	v := strings.TrimSpace(vals[0])
	switch a.Mode {
	case ActiveUAC:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return true
		}
		return n&2 == 0
	case ActiveBool:
		if b, err := strconv.ParseBool(strings.ToLower(v)); err == nil {
			return b
		}
	case ActiveNotBool:
		if b, err := strconv.ParseBool(strings.ToLower(v)); err == nil {
			return !b
		}
	}
	return true
}

// clean drops control characters and caps the length of a value that came from
// a directory (T-14).
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 254 {
		s = s[:254]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// GroupRules map source groups to Hello roles and ring groups (I-4). Matching
// is on the group's stable id; the name is for display.
type GroupRules struct {
	Roles      []RoleRule      `json:"roles"`
	RingGroups []RingGroupRule `json:"ringGroups"`
}

// RoleRule gives members of a source group a role.
type RoleRule struct {
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
	Role      string `json:"role"`
}

// RingGroupRule adds members of a source group to a Hello ring group.
type RingGroupRule struct {
	GroupID     string `json:"groupId"`
	GroupName   string `json:"groupName"`
	RingGroupID int64  `json:"ringGroupId"`
}

var roleRank = map[string]int{"user": 0, "viewer": 1, "operator": 2, "admin": 3}

// Validate checks the rules.
func (g GroupRules) Validate() *ValidationError {
	var v ValidationError
	if len(g.Roles) > 200 || len(g.RingGroups) > 200 {
		v.Fields = append(v.Fields, FieldError{"groupRules", "at most 200 rules of each kind"})
	}
	for i, r := range g.Roles {
		p := "groupRules.roles[" + strconv.Itoa(i) + "]"
		if r.GroupID == "" || len(r.GroupID) > 512 {
			v.Fields = append(v.Fields, FieldError{p + ".groupId", "required (at most 512 characters)"})
		}
		if _, ok := roleRank[r.Role]; !ok {
			v.Fields = append(v.Fields, FieldError{p + ".role", "must be user, viewer, operator or admin"})
		}
	}
	for i, r := range g.RingGroups {
		p := "groupRules.ringGroups[" + strconv.Itoa(i) + "]"
		if r.GroupID == "" || len(r.GroupID) > 512 {
			v.Fields = append(v.Fields, FieldError{p + ".groupId", "required (at most 512 characters)"})
		}
		if r.RingGroupID <= 0 {
			v.Fields = append(v.Fields, FieldError{p + ".ringGroupId", "required"})
		}
	}
	if len(v.Fields) == 0 {
		return nil
	}
	return &v
}

// GroupIDs are the source groups the rules name.
func (g GroupRules) GroupIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range g.Roles {
		if !seen[r.GroupID] {
			seen[r.GroupID] = true
			out = append(out, r.GroupID)
		}
	}
	for _, r := range g.RingGroups {
		if !seen[r.GroupID] {
			seen[r.GroupID] = true
			out = append(out, r.GroupID)
		}
	}
	return out
}

// Role is the role the rules give a user: the highest matching rule, "user"
// when none matches. Admin is granted only when allowAdmin; a user that
// matches only an admin rule then gets the highest other match (T-8).
func (g GroupRules) Role(groups []string, allowAdmin bool) string {
	in := map[string]bool{}
	for _, id := range groups {
		in[id] = true
	}
	best := "user"
	for _, r := range g.Roles {
		if !in[r.GroupID] || (r.Role == "admin" && !allowAdmin) {
			continue
		}
		if roleRank[r.Role] > roleRank[best] {
			best = r.Role
		}
	}
	return best
}

// RingGroupIDs are the ring groups the user's groups put them in, sorted.
func (g GroupRules) RingGroupIDs(groups []string) []int64 {
	in := map[string]bool{}
	for _, id := range groups {
		in[id] = true
	}
	seen := map[int64]bool{}
	var out []int64
	for _, r := range g.RingGroups {
		if in[r.GroupID] && !seen[r.RingGroupID] {
			seen[r.RingGroupID] = true
			out = append(out, r.RingGroupID)
		}
	}
	sortInt64(out)
	return out
}
