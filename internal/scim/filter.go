package scim

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Filters (RFC 7644 section 3.4.2.2). The full grammar is parsed: eq, ne,
// co, sw, ew, gt, ge, lt, le and pr, and, or, not, parentheses and value
// paths (members[value eq "x"]). The spec's minimum (I-23) is userName eq,
// externalId eq, emails.value eq and and; the rest costs nothing more.
// Filters are evaluated in memory on the connector's resources, after the
// store narrowed them by an indexed equality when the filter has one.

type node interface{ eval(doc map[string]any) bool }

type andNode struct{ l, r node }
type orNode struct{ l, r node }
type notNode struct{ x node }
type cmpNode struct {
	path, op string
	val      any
}
type presentNode struct{ path string }
type valuePathNode struct {
	attr string
	f    node
}

func (n andNode) eval(d map[string]any) bool { return n.l.eval(d) && n.r.eval(d) }
func (n orNode) eval(d map[string]any) bool  { return n.l.eval(d) || n.r.eval(d) }
func (n notNode) eval(d map[string]any) bool { return !n.x.eval(d) }
func (n presentNode) eval(d map[string]any) bool {
	vals := resolve(d, n.path)
	if raw := lookup(d, n.path); raw != nil {
		vals = append(vals, raw)
	}
	for _, v := range vals {
		switch x := v.(type) {
		case nil:
		case string:
			if x != "" {
				return true
			}
		case []any:
			if len(x) > 0 {
				return true
			}
		case map[string]any:
			if len(x) > 0 {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func (n valuePathNode) eval(d map[string]any) bool {
	for _, it := range elements(lookup(d, n.attr)) {
		if m, ok := it.(map[string]any); ok && n.f.eval(m) {
			return true
		}
	}
	return false
}

func (n cmpNode) eval(d map[string]any) bool {
	vals := resolve(d, n.path)
	exact := caseExact[strings.ToLower(stripSchema(n.path))]
	if n.op == "ne" {
		return !cmpNode{n.path, "eq", n.val}.eval(d)
	}
	if n.val == nil {
		// eq null: the attribute is absent.
		return n.op == "eq" && !presentNode{n.path}.eval(d)
	}
	for _, v := range vals {
		if compare(v, n.op, n.val, exact) {
			return true
		}
	}
	return false
}

func compare(have any, op string, want any, exact bool) bool {
	switch w := want.(type) {
	case bool:
		h, ok := coerceBool(have)
		return ok && op == "eq" && h == w
	case json.Number:
		hs, err := coerceString(have)
		if err != nil {
			return false
		}
		hn, err1 := json.Number(hs).Float64()
		wn, err2 := w.Float64()
		if err1 != nil || err2 != nil {
			return false
		}
		switch op {
		case "eq":
			return hn == wn
		case "gt":
			return hn > wn
		case "ge":
			return hn >= wn
		case "lt":
			return hn < wn
		case "le":
			return hn <= wn
		}
		return false
	case string:
		hs, err := coerceString(have)
		if err != nil {
			return false
		}
		if !exact {
			hs, w = strings.ToLower(hs), strings.ToLower(w)
		}
		switch op {
		case "eq":
			return hs == w
		case "co":
			return strings.Contains(hs, w)
		case "sw":
			return strings.HasPrefix(hs, w)
		case "ew":
			return strings.HasSuffix(hs, w)
		case "gt":
			return hs > w
		case "ge":
			return hs >= w
		case "lt":
			return hs < w
		case "le":
			return hs <= w
		}
	}
	return false
}

// knownSchemas are the URNs an attribute path may be qualified with.
var knownSchemas = []string{SchemaUser, SchemaGroup, SchemaEnterprise, SchemaPBX}

// stripSchema drops a core schema qualifier ("urn:...:User:userName" is
// userName); an extension qualifier is kept, it names where the attribute is.
func stripSchema(path string) string {
	for _, s := range []string{SchemaUser, SchemaGroup} {
		if len(path) > len(s)+1 && strings.EqualFold(path[:len(s)], s) && path[len(s)] == ':' {
			return path[len(s)+1:]
		}
	}
	return path
}

// lookup returns a document's attribute, by a case-insensitive name; an
// extension-qualified name looks inside the extension's object.
func lookup(d map[string]any, name string) any {
	name = stripSchema(name)
	for _, s := range knownSchemas[2:] {
		if len(name) > len(s)+1 && strings.EqualFold(name[:len(s)], s) && name[len(s)] == ':' {
			ext, _ := getCI(d, s).(map[string]any)
			if ext == nil {
				return nil
			}
			return getCI(ext, name[len(s)+1:])
		}
	}
	return getCI(d, name)
}

func getCI(d map[string]any, k string) any {
	if v, ok := d[k]; ok {
		return v
	}
	for kk, v := range d {
		if strings.EqualFold(kk, k) {
			return v
		}
	}
	return nil
}

func elements(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	}
	return []any{v}
}

// resolve returns the values an attribute path names: "attr", "attr.sub"
// or an extension-qualified form of either. A multi-valued complex attribute
// without a sub-attribute compares on its value sub-attribute.
func resolve(d map[string]any, path string) []any {
	path = stripSchema(path)
	prefix := ""
	for _, s := range knownSchemas[2:] {
		if len(path) > len(s)+1 && strings.EqualFold(path[:len(s)], s) && path[len(s)] == ':' {
			prefix, path = s+":", path[len(s)+1:]
		}
	}
	attrName, subName, _ := strings.Cut(path, ".")
	v := lookup(d, prefix+attrName)
	var out []any
	for _, it := range elements(v) {
		m, isMap := it.(map[string]any)
		switch {
		case subName != "" && isMap:
			out = append(out, elements(getCI(m, subName))...)
		case subName != "":
		case isMap:
			if vv := getCI(m, "value"); vv != nil {
				out = append(out, vv)
			}
		default:
			out = append(out, it)
		}
	}
	return out
}

// Parsing.

type token struct {
	kind byte // '(' ')' '[' ']' 's' (string) 'w' (word)
	text string
}

func tokenize(s string) ([]token, *Error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			i++
		case '(', ')', '[', ']':
			out = append(out, token{kind: c, text: string(c)})
			i++
		case '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return nil, errInvalidFilter("unterminated string")
			}
			var v string
			if err := json.Unmarshal([]byte(s[i:j+1]), &v); err != nil {
				return nil, errInvalidFilter("bad string literal")
			}
			out = append(out, token{kind: 's', text: v})
			i = j + 1
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n()[]\"", rune(s[j])) {
				j++
			}
			out = append(out, token{kind: 'w', text: s[i:j]})
			i = j
		}
	}
	return out, nil
}

type parser struct {
	t   []token
	pos int
}

func (p *parser) peek() *token {
	if p.pos < len(p.t) {
		return &p.t[p.pos]
	}
	return nil
}

func (p *parser) kw(word string) bool {
	t := p.peek()
	if t != nil && t.kind == 'w' && strings.EqualFold(t.text, word) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expect(kind byte) *Error {
	t := p.peek()
	if t == nil || t.kind != kind {
		return errInvalidFilter("expected " + string(kind))
	}
	p.pos++
	return nil
}

// maxFilterLen bounds a filter, so a request cannot make the parser work hard.
const maxFilterLen = 4096

func parseFilter(s string) (node, *Error) {
	if len(s) > maxFilterLen {
		return nil, errInvalidFilter("the filter is too long")
	}
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	p := &parser{t: toks}
	n, err := p.or(0)
	if err != nil {
		return nil, err
	}
	if p.peek() != nil {
		return nil, errInvalidFilter("unexpected " + p.peek().text)
	}
	return n, nil
}

const maxDepth = 32

func (p *parser) or(depth int) (node, *Error) {
	l, err := p.and(depth)
	if err != nil {
		return nil, err
	}
	for p.kw("or") {
		r, err := p.and(depth)
		if err != nil {
			return nil, err
		}
		l = orNode{l, r}
	}
	return l, nil
}

func (p *parser) and(depth int) (node, *Error) {
	l, err := p.unary(depth)
	if err != nil {
		return nil, err
	}
	for p.kw("and") {
		r, err := p.unary(depth)
		if err != nil {
			return nil, err
		}
		l = andNode{l, r}
	}
	return l, nil
}

func (p *parser) unary(depth int) (node, *Error) {
	if depth > maxDepth {
		return nil, errInvalidFilter("the filter is nested too deeply")
	}
	if p.kw("not") {
		if err := p.expect('('); err != nil {
			return nil, err
		}
		x, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		return notNode{x}, nil
	}
	if t := p.peek(); t != nil && t.kind == '(' {
		p.pos++
		x, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		return x, nil
	}
	return p.attrExp(depth)
}

func validPath(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(":._-$", r) {
			return false
		}
	}
	return unicode.IsLetter(rune(s[0])) || s[0] == '$'
}

var compareOps = map[string]bool{"eq": true, "ne": true, "co": true, "sw": true, "ew": true, "gt": true, "ge": true, "lt": true, "le": true}

func (p *parser) attrExp(depth int) (node, *Error) {
	t := p.peek()
	if t == nil || t.kind != 'w' || !validPath(t.text) {
		return nil, errInvalidFilter("expected an attribute path")
	}
	p.pos++
	path := t.text
	if n := p.peek(); n != nil && n.kind == '[' {
		p.pos++
		f, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := p.expect(']'); err != nil {
			return nil, err
		}
		return valuePathNode{attr: path, f: f}, nil
	}
	op := p.peek()
	if op == nil || op.kind != 'w' {
		return nil, errInvalidFilter("expected an operator after " + path)
	}
	p.pos++
	o := strings.ToLower(op.text)
	if o == "pr" {
		return presentNode{path}, nil
	}
	if !compareOps[o] {
		return nil, errInvalidFilter("unsupported operator " + op.text)
	}
	v := p.peek()
	if v == nil {
		return nil, errInvalidFilter("expected a value after " + op.text)
	}
	p.pos++
	switch {
	case v.kind == 's':
		return cmpNode{path, o, v.text}, nil
	case v.kind == 'w' && strings.EqualFold(v.text, "true"):
		return cmpNode{path, o, true}, nil
	case v.kind == 'w' && strings.EqualFold(v.text, "false"):
		return cmpNode{path, o, false}, nil
	case v.kind == 'w' && strings.EqualFold(v.text, "null"):
		return cmpNode{path, o, nil}, nil
	case v.kind == 'w' && isNumber(v.text):
		return cmpNode{path, o, json.Number(v.text)}, nil
	}
	return nil, errInvalidFilter("bad comparison value " + v.text)
}

func isNumber(s string) bool {
	var n json.Number
	return json.Unmarshal([]byte(s), &n) == nil
}

// Hint is an indexed equality a filter implies, used to narrow the rows read
// before the filter itself runs: field is id, name or external_id.
type Hint struct {
	Field string
	Value string
}

// hintOf finds an equality on an indexed attribute that every match must
// satisfy: the filter itself, or one side of a top-level and.
func hintOf(typ string, n node) *Hint {
	switch x := n.(type) {
	case cmpNode:
		s, ok := x.val.(string)
		if x.op != "eq" || !ok {
			return nil
		}
		switch strings.ToLower(stripSchema(x.path)) {
		case "id":
			return &Hint{"id", s}
		case "externalid":
			return &Hint{"external_id", s}
		case strings.ToLower(nameAttr(typ)):
			return &Hint{"name", s}
		}
	case andNode:
		if h := hintOf(typ, x.l); h != nil {
			return h
		}
		return hintOf(typ, x.r)
	}
	return nil
}

// patchPath is a parsed PATCH path (RFC 7644 section 3.5.2): an attribute,
// optionally a filter on its values and a sub-attribute.
type patchPath struct {
	attr   string // the attribute, extension-qualified when it is in one
	sub    string // a sub-attribute ("givenName" of name.givenName)
	filter node
}

func parsePath(s string) (patchPath, *Error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 512 {
		return patchPath{}, errInvalidPath("empty or too long path")
	}
	var pp patchPath
	head := s
	rest := ""
	if i := strings.IndexByte(s, '['); i >= 0 {
		j := strings.LastIndexByte(s, ']')
		if j < i {
			return patchPath{}, errInvalidPath("unbalanced brackets in " + s)
		}
		f, err := parseFilter(s[i+1 : j])
		if err != nil {
			return patchPath{}, errInvalidPath("bad filter in path: " + err.Detail)
		}
		pp.filter = f
		head, rest = s[:i], s[j+1:]
		if rest != "" {
			if rest[0] != '.' || !validPath(rest[1:]) {
				return patchPath{}, errInvalidPath("bad sub-attribute in " + s)
			}
			rest = rest[1:]
		}
	}
	if !validPath(head) {
		return patchPath{}, errInvalidPath("bad attribute path " + s)
	}
	head = stripSchema(head)
	prefix := ""
	for _, sc := range knownSchemas[2:] {
		if len(head) > len(sc)+1 && strings.EqualFold(head[:len(sc)], sc) && head[len(sc)] == ':' {
			prefix, head = sc+":", head[len(sc)+1:]
		}
	}
	a, subName, _ := strings.Cut(head, ".")
	if subName != "" && rest != "" {
		return patchPath{}, errInvalidPath("bad path " + s)
	}
	if rest != "" {
		subName = rest
	}
	pp.attr, pp.sub = prefix+a, subName
	return pp, nil
}
