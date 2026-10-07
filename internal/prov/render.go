package prov

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
)

// Render limits (spec S-8): a render that takes longer or writes more fails.
const (
	MaxRenderBytes = 256 << 10
	renderBudget   = 100 * time.Millisecond
)

// renderTimeout is the per-render deadline; a variable only so a test can
// prove the deadline is enforced without a template that is slow.
var renderTimeout = renderBudget

// ErrNoFile: the template has no file whose pattern names the requested
// file (the handler answers an empty 404, result not_found).
var ErrNoFile = errors.New("prov: the template has no such file")

// maxRangeDepth bounds nested range actions. The only lists a template
// sees are the BLF keys, so two levels bound a render's work by the square
// of the key count; deeper nesting (or ranging over a number) could spin
// without writing a byte, out of reach of the output deadline.
const maxRangeDepth = 2

// templateFuncs are the functions a template may call besides the
// comparison and logic builtins in allowedIdents (spec S-8). add, mul and
// div exist because vendor keys are numbered (line key N+2, minutes from
// seconds).
var templateFuncs = template.FuncMap{
	"xml": func(v any) (string, error) {
		var b strings.Builder
		err := xml.EscapeText(&b, []byte(fmt.Sprint(v)))
		return b.String(), err
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"default": func(def, v any) any {
		if v == nil || reflect.ValueOf(v).IsZero() {
			return def
		}
		return v
	},
	"add": func(a, b int) int { return a + b },
	"mul": func(a, b int) int { return a * b },
	"div": func(a, b int) (int, error) {
		if b == 0 {
			return 0, errors.New("div by zero")
		}
		return a / b, nil
	},
}

// allowedIdents is every function a template may name. text/template's
// call, printf (whose width can allocate without bound), print, println,
// slice, html, js and urlquery are refused, as are define, template and
// block.
var allowedIdents = map[string]bool{
	"and": true, "or": true, "not": true, "eq": true, "ne": true, "lt": true,
	"le": true, "gt": true, "ge": true, "len": true, "index": true,
	"xml": true, "upper": true, "lower": true, "default": true,
	"add": true, "mul": true, "div": true,
}

// Render renders the file of t that the requested name matches, within
// 100 ms and 256 KiB. The output depends on nothing but t, file and d, so
// identical input renders identical bytes (a stable ETag). ErrNoFile when
// no file of t matches the name.
func Render(ctx context.Context, t Template, file string, d RenderData) ([]byte, error) {
	for _, f := range t.Files {
		if strings.EqualFold(ExpandPattern(f.Pattern, d.Phone), file) {
			return renderBody(ctx, f.Body, d)
		}
	}
	return nil, ErrNoFile
}

// FileFor returns the file of t that the requested name matches.
func FileFor(t Template, file string, p Phone) (TemplateFile, bool) {
	for _, f := range t.Files {
		if strings.EqualFold(ExpandPattern(f.Pattern, p), file) {
			return f, true
		}
	}
	return TemplateFile{}, false
}

// ExpandPattern fills a file-name pattern's {mac}, {MAC} and {model}.
func ExpandPattern(pattern string, p Phone) string {
	return strings.NewReplacer("{mac}", p.MAC, "{MAC}", p.MACUpper, "{model}", strings.ReplaceAll(p.Model, " ", "")).Replace(pattern)
}

func renderBody(ctx context.Context, body string, d RenderData) ([]byte, error) {
	tmpl, _, err := parseBody(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, errDeadline
	}
	w := &cappedWriter{ctx: ctx}
	if err := tmpl.Execute(w, toData(reflect.ValueOf(d))); err != nil {
		if w.err != nil {
			return nil, w.err
		}
		return nil, err
	}
	if w.err != nil {
		return nil, w.err
	}
	return w.buf.Bytes(), nil
}

var (
	errDeadline = fmt.Errorf("prov: render exceeded %v", renderBudget)
	errTooLarge = fmt.Errorf("prov: render exceeded %d KiB", MaxRenderBytes>>10)
)

// cappedWriter fails a render that writes past MaxRenderBytes or its
// deadline; text/template stops at the first failed write.
type cappedWriter struct {
	ctx context.Context
	buf bytes.Buffer
	err error
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	switch {
	case w.err != nil:
	case w.ctx.Err() != nil:
		w.err = errDeadline
	case w.buf.Len()+len(p) > MaxRenderBytes:
		w.err = errTooLarge
	default:
		return w.buf.Write(p)
	}
	return 0, w.err
}

// toData turns RenderData into maps, slices and plain values, so a
// template reaches nothing but the documented fields: maps have no
// methods, and with missingkey=error an unknown field fails the render.
func toData(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return toData(v.Elem())
	case reflect.Struct:
		m := make(map[string]any, v.NumField())
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() {
				m[f.Name] = toData(v.Field(i))
			}
		}
		return m
	case reflect.Slice:
		out := make([]any, v.Len())
		for i := range v.Len() {
			out[i] = toData(v.Index(i))
		}
		return out
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int64:
		return int(v.Int())
	case reflect.Bool:
		return v.Bool()
	}
	return nil
}

// bodyError is a template body failure at a line (0 when unknown).
type bodyError struct {
	line int
	msg  string
}

func (e bodyError) Error() string {
	if e.line > 0 {
		return "line " + strconv.Itoa(e.line) + ": " + e.msg
	}
	return e.msg
}

var parseLineRE = regexp.MustCompile(`^template: [^:]*:(\d+):(?:\d+:)? ?(.*)$`)

// parseBody parses a body and checks it against the rules above.
func parseBody(body string) (*template.Template, *bodyError, error) {
	tmpl, err := template.New("body").Option("missingkey=error").Funcs(templateFuncs).Parse(body)
	if err != nil {
		be := bodyError{msg: err.Error()}
		if m := parseLineRE.FindStringSubmatch(err.Error()); m != nil {
			be.line, _ = strconv.Atoi(m[1])
			be.msg = m[2]
		}
		return nil, &be, be
	}
	if len(tmpl.Templates()) > 1 {
		be := bodyError{msg: "define and block are not allowed"}
		return nil, &be, be
	}
	c := checker{tree: tmpl.Tree, root: reflect.TypeFor[RenderData](), vars: map[string]reflect.Type{}}
	c.vars["$"] = c.root
	c.list(tmpl.Root, c.root, 0)
	if c.err != nil {
		return nil, c.err, *c.err
	}
	return tmpl, nil, nil
}

// checker walks a parsed body, tracking the type of dot, and refuses
// unknown variables, unknown functions, template calls and deep or
// unbounded ranges.
type checker struct {
	tree *parse.Tree
	root reflect.Type
	vars map[string]reflect.Type // nil type: unknown, not checked
	err  *bodyError
}

func (c *checker) fail(n parse.Node, msg string) {
	if c.err != nil {
		return
	}
	line := 0
	loc, _ := c.tree.ErrorContext(n)
	if parts := strings.Split(loc, ":"); len(parts) >= 2 {
		line, _ = strconv.Atoi(parts[1])
	}
	c.err = &bodyError{line: line, msg: msg}
}

func (c *checker) list(l *parse.ListNode, dot reflect.Type, depth int) {
	if l == nil {
		return
	}
	for _, n := range l.Nodes {
		c.node(n, dot, depth)
	}
}

func (c *checker) node(n parse.Node, dot reflect.Type, depth int) {
	switch n := n.(type) {
	case *parse.ActionNode:
		c.pipe(n.Pipe, dot)
	case *parse.IfNode:
		c.pipe(n.Pipe, dot)
		c.list(n.List, dot, depth)
		c.list(n.ElseList, dot, depth)
	case *parse.WithNode:
		t := c.pipe(n.Pipe, dot)
		c.list(n.List, t, depth)
		c.list(n.ElseList, dot, depth)
	case *parse.RangeNode:
		if depth+1 > maxRangeDepth {
			c.fail(n, fmt.Sprintf("range nested deeper than %d", maxRangeDepth))
			return
		}
		if len(n.Pipe.Cmds) != 1 || len(n.Pipe.Cmds[0].Args) != 1 {
			c.fail(n, "range takes a list variable, not an expression")
			return
		}
		switch n.Pipe.Cmds[0].Args[0].(type) {
		case *parse.FieldNode, *parse.VariableNode:
		default:
			c.fail(n, "range takes a list variable, not an expression")
			return
		}
		t := c.pipeType(n.Pipe, dot)
		var elem reflect.Type
		if t != nil {
			if t.Kind() != reflect.Slice {
				c.fail(n, "range over a value that is not a list")
				return
			}
			elem = t.Elem()
		}
		for i, v := range n.Pipe.Decl {
			if len(n.Pipe.Decl) == 2 && i == 0 {
				c.vars[v.Ident[0]] = reflect.TypeFor[int]()
			} else {
				c.vars[v.Ident[0]] = elem
			}
		}
		c.list(n.List, elem, depth+1)
		c.list(n.ElseList, dot, depth)
	case *parse.TemplateNode:
		c.fail(n, "template calls are not allowed")
	case *parse.TextNode, *parse.CommentNode, *parse.BreakNode, *parse.ContinueNode:
	default:
		c.fail(n, "unsupported action")
	}
}

// pipe checks a pipeline and returns its type when it is a plain variable.
func (c *checker) pipe(p *parse.PipeNode, dot reflect.Type) reflect.Type {
	if p == nil {
		return nil
	}
	for _, cmd := range p.Cmds {
		for _, a := range cmd.Args {
			c.arg(a, dot)
		}
	}
	t := c.pipeType(p, dot)
	for _, v := range p.Decl {
		c.vars[v.Ident[0]] = t
	}
	return t
}

func (c *checker) pipeType(p *parse.PipeNode, dot reflect.Type) reflect.Type {
	if len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return nil
	}
	switch a := p.Cmds[0].Args[0].(type) {
	case *parse.FieldNode:
		t, _ := c.chain(a, dot, a.Ident)
		return t
	case *parse.VariableNode:
		t, _ := c.chain(a, c.vars[a.Ident[0]], a.Ident[1:])
		return t
	case *parse.DotNode:
		return dot
	}
	return nil
}

func (c *checker) arg(a parse.Node, dot reflect.Type) {
	switch a := a.(type) {
	case *parse.IdentifierNode:
		if !allowedIdents[a.Ident] {
			c.fail(a, "function "+strconv.Quote(a.Ident)+" is not allowed")
		}
	case *parse.FieldNode:
		if _, err := c.chain(a, dot, a.Ident); err != "" {
			c.fail(a, err)
		}
	case *parse.VariableNode:
		if _, err := c.chain(a, c.vars[a.Ident[0]], a.Ident[1:]); err != "" {
			c.fail(a, err)
		}
	case *parse.ChainNode:
		c.arg(a.Node, dot)
	case *parse.PipeNode:
		c.pipe(a, dot)
	}
}

// chain resolves a field chain against t; a nil t is not checked.
func (c *checker) chain(n parse.Node, t reflect.Type, idents []string) (reflect.Type, string) {
	for _, id := range idents {
		if t == nil {
			return nil, ""
		}
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return nil, "unknown variable ." + id
		}
		f, ok := t.FieldByName(id)
		if !ok || !f.IsExported() {
			return nil, "unknown variable ." + id
		}
		t = f.Type
	}
	return t, ""
}

// Validate checks a template before it is saved (spec S-8): the fields,
// every file pattern (a plain file name using only {mac}, {MAC} and
// {model}), every body (parses, uses only documented variables and
// allowed functions), and a render of every file against a sample phone,
// with and without optional data, within 100 ms and 256 KiB.
func Validate(t Template) []FieldError {
	var errs []FieldError
	add := func(p string, line int, msg string) {
		errs = append(errs, FieldError{Path: p, Line: line, Message: msg})
	}
	if !t.Vendor.Valid() {
		add("vendor", 0, "unknown vendor")
	}
	if l := len(t.ModelGlob); l < 1 || l > 64 {
		add("modelGlob", 0, "must be 1 to 64 characters")
	} else if _, err := path.Match(t.ModelGlob, ""); err != nil {
		add("modelGlob", 0, "malformed glob")
	}
	if l := len(t.Name); l < 1 || l > 128 {
		add("name", 0, "must be 1 to 128 characters")
	}
	if len(t.Files) == 0 {
		add("files", 0, "a template needs at least one file")
	}
	seen := map[string]int{}
	for i, f := range t.Files {
		pre := "files[" + strconv.Itoa(i) + "]."
		if msg := checkPattern(f.Pattern); msg != "" {
			add(pre+"pattern", 0, msg)
		} else if j, dup := seen[strings.ToLower(f.Pattern)]; dup {
			add(pre+"pattern", 0, "same pattern as files["+strconv.Itoa(j)+"]")
		} else {
			seen[strings.ToLower(f.Pattern)] = i
		}
		if _, _, err := mime.ParseMediaType(f.ContentType); err != nil {
			add(pre+"contentType", 0, "not a media type")
		}
		if _, be, _ := parseBody(f.Body); be != nil {
			add(pre+"body", be.line, be.msg)
			continue
		}
		for _, d := range []RenderData{SampleData(true), SampleData(false)} {
			if _, err := renderBody(context.Background(), f.Body, d); err != nil {
				add(pre+"body", 0, "sample render: "+err.Error())
				break
			}
		}
	}
	return errs
}

var placeholderRE = regexp.MustCompile(`\{[^}]*\}`)

func checkPattern(p string) string {
	for _, ph := range placeholderRE.FindAllString(p, -1) {
		if ph != "{mac}" && ph != "{MAC}" && ph != "{model}" {
			return "unknown variable " + ph + " (use {mac}, {MAC} or {model})"
		}
	}
	if !safeName(ExpandPattern(p, SampleData(true).Phone)) {
		return "must be a plain file name (letters, digits, '.', '-', '_', '+')"
	}
	return ""
}

// SampleData is the phone templates are validated against (and the
// templates editor previews without a phone): full has a pinned firmware,
// a label, a voicemail code and twelve BLF keys; empty has none of them.
func SampleData(full bool) RenderData {
	d := RenderData{
		Phone:  Phone{MAC: "0015651234ab", MACUpper: "0015651234AB", Vendor: Generic, Model: "MODEL", AdminPassword: "sample-admin-password"},
		Line:   Line{Username: "1001-1234ab", AuthName: "1001-1234ab", Password: "sample-secret", DisplayName: "Sample User", Label: "1001", Domain: "hello.example"},
		Server: Server{Host: "sip.hello.example", Port: 5060, Transport: "udp", Expiry: 3600},
		Prov: ProvInfo{URL: "https://prov.hello.example/p/sample/", CAURL: "http://prov.hello.example/p/ca.crt", ResyncSeconds: 86400,
			CACertPEM: "-----BEGIN CERTIFICATE-----\nU0FNUExF\n-----END CERTIFICATE-----"},
		Time: TimeInfo{Zone: "UTC", NTP: "pool.ntp.org"},
	}
	if full {
		d.Phone.Label = "Reception"
		d.Line.VoicemailCode = "*97"
		d.Firmware = &FirmwareInfo{URL: "https://prov.hello.example/p/sample/fw/firmware.rom", Version: "1.2.3"}
		for i := range 12 {
			n := strconv.Itoa(1002 + i)
			d.BLF = append(d.BLF, BLFKey{Number: n, Label: "User " + n, URI: "sip:" + n + "@hello.example"})
		}
	}
	return d
}
