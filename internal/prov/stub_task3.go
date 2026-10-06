package prov

// Stand-ins for the plan Task 2 functions (contract 2's trailing comment),
// with exactly the contract's signatures, so the control plane (Task 3)
// builds and tests before prov-core lands. DELETE THIS FILE when merging
// prov-core: its real implementations replace every function here, and
// the duplicate definitions make the merge fail to build until this file
// is gone. Limiter, Options and NewHandler are not in the contract; they
// are the shape cmd/hello-control wires and are reconciled at that merge.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

var macRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// NormalizeMAC: see the contract comment in types.go.
func NormalizeMAC(s string) (string, error) {
	m := strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.TrimSpace(s)))
	if !macRe.MatchString(m) {
		return "", errors.New("prov: a MAC is 12 hex digits")
	}
	return m, nil
}

// MatchFile: see the contract comment in types.go.
func MatchFile(_ Vendor, _, mac, name string) (FileKind, bool) {
	if strings.Contains(strings.ToLower(name), mac) {
		return KindDevice, true
	}
	return KindCommon, true
}

// Resolve: see the contract comment in types.go.
func Resolve(phone Phone, override *Template, all []Template) (Template, bool) {
	if override != nil {
		return *override, true
	}
	var best *Template
	for i := range all {
		t := &all[i]
		if t.Vendor != phone.Vendor {
			continue
		}
		if ok, _ := path.Match(t.ModelGlob, phone.Model); !ok {
			continue
		}
		if best == nil || t.Priority > best.Priority || (t.Priority == best.Priority && t.ID < best.ID) {
			best = t
		}
	}
	if best == nil {
		return Template{}, false
	}
	return *best, true
}

var funcs = template.FuncMap{
	"xml": func(s string) string {
		var b bytes.Buffer
		_ = xmlEscape(&b, s)
		return b.String()
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"default": func(def, v string) string {
		if v == "" {
			return def
		}
		return v
	},
}

func xmlEscape(b *bytes.Buffer, s string) error {
	_, err := b.WriteString(strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s))
	return err
}

// Render: see the contract comment in types.go.
func Render(ctx context.Context, t Template, file string, d RenderData) ([]byte, error) {
	for _, f := range t.Files {
		name := strings.NewReplacer("{mac}", d.Phone.MAC, "{MAC}", d.Phone.MACUpper, "{model}", d.Phone.Model).Replace(f.Pattern)
		if !strings.EqualFold(name, file) {
			continue
		}
		tpl, err := template.New(f.Pattern).Funcs(funcs).Option("missingkey=error").Parse(f.Body)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		if err := tpl.Execute(&out, d); err != nil {
			return nil, err
		}
		return out.Bytes(), ctx.Err()
	}
	return nil, ErrNotFound
}

// Validate: see the contract comment in types.go.
func Validate(t Template) []FieldError {
	var out []FieldError
	if len(t.Files) == 0 {
		out = append(out, FieldError{Path: "files", Message: "at least one file is required"})
	}
	for i, f := range t.Files {
		if _, err := template.New(f.Pattern).Funcs(funcs).Parse(f.Body); err != nil {
			out = append(out, FieldError{Path: "files[" + strconv.Itoa(i) + "].body", Message: err.Error()})
		}
		if f.Pattern == "" || strings.ContainsAny(f.Pattern, "/\\") {
			out = append(out, FieldError{Path: "files[" + strconv.Itoa(i) + "].pattern", Message: "must be a file name"})
		}
	}
	return out
}

// NewToken: see the contract comment in types.go.
func NewToken() (plain string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	plain = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	h := sha256.Sum256([]byte(plain))
	return plain, h[:]
}

var tokenPathRe = regexp.MustCompile(`^/p/[a-z2-7]{52}(/|$)`)

// RedactPath: see the contract comment in types.go.
func RedactPath(p string) string { return tokenPathRe.ReplaceAllString(p, "/p/****$1") }

// Builtins: see the contract comment in types.go.
func Builtins() []Template { return nil }

// Limiter is the provisioning rate limiter (Task 2).
type Limiter interface{}

// Options configures the provisioning handler (Task 2).
type Options struct{}

// NewHandler is the provisioning handler (Task 2).
func NewHandler(Store, Limiter, Opener, Options) http.Handler { return http.NotFoundHandler() }
