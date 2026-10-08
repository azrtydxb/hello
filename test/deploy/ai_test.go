package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/mcp"
)

const helloHost = "hello.kw.watteel.lab"

// nginxLocation is the body of the location block opened by head in the
// UI's nginx template, or "" when there is none.
func nginxLocation(t *testing.T, head string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "nginx", "default.conf.template"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\n\s*location ` + regexp.QuoteMeta(head) + ` \{(.*?)\n    \}`).FindStringSubmatch(string(raw))
	if m == nil {
		return ""
	}
	return m[1]
}

// TestKwAIAccess (spec ai-external-access S-19) fails if the kw manifest
// lacks TLS on the hello Ingress (hello-tls from cluster-ca through the
// ingress-shim annotation), HELLO_PUBLIC_URL=https://hello.kw.watteel.lab
// or HELLO_OAUTH_DCR=true on hello-control, or if the UI's nginx template
// does not proxy /mcp, /oauth/ and /.well-known/oauth- to hello-control,
// or proxies the console's consent page or lets it be framed.
func TestKwAIAccess(t *testing.T) {
	docs := kwDocs(t)
	ing := findDoc(docs, "Ingress", "hello")
	if ing == nil {
		t.Fatal("no hello Ingress")
	}
	if at(ing, "metadata", "annotations", "cert-manager.io/cluster-issuer") != "cluster-ca" {
		t.Error("hello Ingress is not annotated cert-manager.io/cluster-issuer: cluster-ca")
	}
	if at(ing, "spec", "rules", 0, "host") != helloHost || at(ing, "spec", "tls", 0, "hosts", 0) != helloHost {
		t.Errorf("hello Ingress rule/TLS host is not %s", helloHost)
	}
	if at(ing, "spec", "tls", 0, "secretName") != "hello-tls" {
		t.Errorf("hello Ingress TLS secret = %v, want hello-tls", at(ing, "spec", "tls", 0, "secretName"))
	}
	// HTTP must redirect to HTTPS (the controller's default with TLS).
	if v := at(ing, "metadata", "annotations", "nginx.ingress.kubernetes.io/ssl-redirect"); v != nil && v != "true" {
		t.Errorf("hello Ingress ssl-redirect = %v", v)
	}
	c := controlContainer(t, docs)
	if got := envValue(c, "HELLO_PUBLIC_URL"); got != "https://"+helloHost {
		t.Errorf("HELLO_PUBLIC_URL = %q", got)
	}
	if got := envValue(c, "HELLO_OAUTH_DCR"); got != "true" {
		t.Errorf("HELLO_OAUTH_DCR = %q", got)
	}

	for _, head := range []string{"= /mcp", "/oauth/", "/.well-known/oauth-"} {
		body := nginxLocation(t, head)
		if !strings.Contains(body, "set $hello_control ${HELLO_CONTROL_UPSTREAM};") || !strings.Contains(body, "proxy_pass $hello_control;") {
			t.Errorf("nginx: location %s does not proxy to hello-control", head)
		}
		if !strings.Contains(body, "X-Forwarded-Proto $hello_forwarded_proto") {
			t.Errorf("nginx: location %s does not pass the client's scheme", head)
		}
	}
	consent := nginxLocation(t, "= /oauth/consent")
	if strings.Contains(consent, "proxy_pass") || !strings.Contains(consent, "/index.html") {
		t.Error("nginx: /oauth/consent is not served from the console")
	}
	if !strings.Contains(consent, `Content-Security-Policy "frame-ancestors 'none'" always`) {
		t.Error("nginx: /oauth/consent may be framed")
	}
}

// TestDocsAIAccess (spec S-21) fails if docs/ai-access.md lacks a section
// S-21 lists, its tool and resource tables differ from what
// `go run ./internal/mcp/cmd/doctable` generates, its URLs are not kw's
// public URL, or the README does not link it.
func TestDocsAIAccess(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "ai-access.md"))
	if err != nil {
		t.Fatal(err)
	}
	guide := string(raw)
	var headings []string
	for _, l := range strings.Split(guide, "\n") {
		if strings.HasPrefix(l, "#") {
			headings = append(headings, strings.ToLower(l))
		}
	}
	for _, topic := range []string{"connect an mcp client", "claude code", "claude desktop", "any other client", "cluster-ca",
		"scopes", "consent and revocation", "service accounts", "tools and resources", "skills", "security model"} {
		var ok bool
		for _, h := range headings {
			ok = ok || strings.Contains(h, topic)
		}
		if !ok {
			t.Errorf("no section on %q", topic)
		}
	}
	public := envValue(controlContainer(t, kwDocs(t)), "HELLO_PUBLIC_URL")
	for _, want := range []string{public + "/mcp", public + "/.well-known/oauth-authorization-server",
		public + "/.well-known/oauth-protected-resource/mcp", "http://prov.hello.kw.watteel.lab/p/ca.crt", "x-hello-secret", "via", mcp.Withheld} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide lacks %q", want)
		}
	}

	spec, err := apispec.Load(api.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	want, err := mcp.DocTables(spec)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)<!-- doctable:begin -->\n(.*)<!-- doctable:end -->`).FindStringSubmatch(guide)
	if m == nil {
		t.Fatal("the guide has no doctable markers")
	}
	if mdTable(m[1]) != mdTable(want) {
		t.Error("the guide's tool and resource tables are stale: paste `go run ./internal/mcp/cmd/doctable` between the doctable markers")
	}

	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "(docs/ai-access.md)") {
		t.Error("README.md does not link docs/ai-access.md")
	}
}

// mdTable is Markdown table text with each cell trimmed and each separator
// row reduced, so a formatter's column alignment does not count as drift.
func mdTable(s string) string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "|") {
			out = append(out, l)
			continue
		}
		var row []string
		for _, c := range strings.Split(strings.Trim(strings.ReplaceAll(l, `\|`, "\x00"), "|"), "|") {
			c = strings.TrimSpace(c)
			if strings.Trim(c, "-") == "" {
				c = "---"
			}
			row = append(row, c)
		}
		out = append(out, strings.Join(row, "|"))
	}
	return strings.Join(out, "\n")
}
