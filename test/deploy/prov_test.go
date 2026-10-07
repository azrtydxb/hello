package deploy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/azrtydxb/hello/internal/store"
)

const provHost = "prov.hello.kw.watteel.lab"

// kwDocs decodes every document of the kw manifest generically.
func kwDocs(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				return docs
			}
			t.Fatalf("decode resources.yaml: %v", err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
}

// at walks a decoded document by map keys and list indexes.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func findDoc(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] == kind && at(d, "metadata", "name") == name {
			return d
		}
	}
	return nil
}

// controlContainer is hello-control's server container.
func controlContainer(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	dep := findDoc(docs, "Deployment", "hello-control")
	cs, _ := at(dep, "spec", "template", "spec", "containers").([]any)
	for _, c := range cs {
		if m, ok := c.(map[string]any); ok && m["name"] == "hello-control" {
			return m
		}
	}
	t.Fatal("no hello-control container in the hello-control Deployment")
	return nil
}

func envValue(c map[string]any, name string) string {
	env, _ := c["env"].([]any)
	for _, e := range env {
		if at(e, "name") == name {
			s, _ := at(e, "value").(string)
			return s
		}
	}
	return ""
}

// TestKwProvisioningIngress (spec S-12) fails if the kw manifest lacks the
// prov.hello.kw.watteel.lab Ingress, its cluster-ca issuer annotation,
// ssl-redirect "false", enable-access-log "false", the hello-prov Service
// on port 8083 reaching hello-control's prov port, the public URL and CA
// file on hello-control, or the optional redirect-secret env.
func TestKwProvisioningIngress(t *testing.T) {
	docs := kwDocs(t)

	ing := findDoc(docs, "Ingress", "hello-prov")
	if ing == nil {
		t.Fatal("no hello-prov Ingress")
	}
	if at(ing, "spec", "rules", 0, "host") != provHost || at(ing, "spec", "tls", 0, "hosts", 0) != provHost {
		t.Errorf("hello-prov Ingress rule/TLS host is not %s", provHost)
	}
	for _, a := range []string{"nginx.ingress.kubernetes.io/ssl-redirect", "nginx.ingress.kubernetes.io/enable-access-log"} {
		if at(ing, "metadata", "annotations", a) != "false" {
			t.Errorf("hello-prov Ingress: %s is not \"false\"", a)
		}
	}
	backend := at(ing, "spec", "rules", 0, "http", "paths", 0, "backend", "service")
	if at(backend, "name") != "hello-prov" || at(backend, "port", "number") != 8083 {
		t.Errorf("hello-prov Ingress backend = %v, want hello-prov:8083", backend)
	}

	// cert-manager's ingress-shim issues the certificate from the Ingress
	// annotation; the Sync deployer cannot create Certificate objects.
	if findDoc(docs, "Certificate", "hello-prov-tls") != nil {
		t.Error("hello-prov-tls is a Certificate object; the Sync deployer cannot create it, use the Ingress annotation")
	}
	if at(ing, "metadata", "annotations", "cert-manager.io/cluster-issuer") != "cluster-ca" {
		t.Error("hello-prov Ingress is not annotated cert-manager.io/cluster-issuer: cluster-ca")
	}
	secret := at(ing, "spec", "tls", 0, "secretName")
	if secret != "hello-prov-tls" {
		t.Errorf("the Ingress TLS secret is %v, want hello-prov-tls", secret)
	}

	svc := findDoc(docs, "Service", "hello-prov")
	if svc == nil {
		t.Fatal("no hello-prov Service")
	}
	if at(svc, "spec", "ports", 0, "port") != 8083 || at(svc, "spec", "ports", 0, "targetPort") != "prov" {
		t.Errorf("hello-prov Service port is not 8083 -> prov")
	}
	if at(svc, "spec", "selector", "app.kubernetes.io/component") != "control" {
		t.Errorf("hello-prov Service does not select hello-control")
	}

	c := controlContainer(t, docs)
	var prov bool
	ports, _ := c["ports"].([]any)
	for _, p := range ports {
		prov = prov || (at(p, "name") == "prov" && at(p, "containerPort") == 8083)
	}
	if !prov {
		t.Error("hello-control has no prov containerPort 8083")
	}
	if got := envValue(c, "HELLO_PROV_PUBLIC_URL"); got != "https://"+provHost {
		t.Errorf("HELLO_PROV_PUBLIC_URL = %q", got)
	}
	ca := envValue(c, "HELLO_PROV_CA_CERT")
	var caMounted bool
	mounts, _ := c["volumeMounts"].([]any)
	for _, m := range mounts {
		dir, _ := at(m, "mountPath").(string)
		if ca != "" && strings.HasPrefix(ca, dir+"/") {
			vols, _ := at(findDoc(docs, "Deployment", "hello-control"), "spec", "template", "spec", "volumes").([]any)
			for _, v := range vols {
				caMounted = caMounted || (at(v, "name") == at(m, "name") && at(v, "secret", "secretName") == secret)
			}
		}
	}
	if !caMounted {
		t.Errorf("HELLO_PROV_CA_CERT %q is not read from the %v secret", ca, secret)
	}
	// Spec S-11: each redirect credential from hello-prov-redirect, optional.
	redirectEnv := map[string]string{
		"HELLO_PROV_SNOM_KEY_ID": "snomSrapsAccessKeyId", "HELLO_PROV_SNOM_KEY_SECRET": "snomSrapsAccessKeySecret",
		"HELLO_PROV_YEALINK_KEY": "yealinkRpsAccessKey", "HELLO_PROV_YEALINK_SECRET": "yealinkRpsAccessSecret",
		"HELLO_PROV_YMCS_CLIENT_ID": "yealinkYmcsClientId", "HELLO_PROV_YMCS_CLIENT_SECRET": "yealinkYmcsClientSecret",
		"HELLO_PROV_YMCS_REGION":    "yealinkYmcsRegion",
		"HELLO_PROV_GDMS_CLIENT_ID": "gdmsClientId", "HELLO_PROV_GDMS_CLIENT_SECRET": "gdmsClientSecret",
		"HELLO_PROV_GDMS_USERNAME": "gdmsUsername", "HELLO_PROV_GDMS_PASSWORD": "gdmsPassword",
		"HELLO_PROV_GDMS_REGION": "gdmsRegion", "HELLO_PROV_GDMS_SITE_ID": "gdmsSiteId",
	}
	env, _ := c["env"].([]any)
	for name, key := range redirectEnv {
		var ok bool
		for _, e := range env {
			ref := at(e, "valueFrom", "secretKeyRef")
			ok = ok || (at(e, "name") == name && at(ref, "name") == "hello-prov-redirect" && at(ref, "key") == key && at(ref, "optional") == true)
		}
		if !ok {
			t.Errorf("hello-control lacks %s from hello-prov-redirect key %s (optional)", name, key)
		}
	}
}

// TestDocsProvisioningLinks (spec S-19) fails if the provisioning guide is
// missing a section for a first-class vendor or a topic S-19 lists, if its
// DHCP and CA values differ from what GET /api/v1/prov/settings computes
// for kw's public URL, or if docs/phones.md or the README do not link to it.
func TestDocsProvisioningLinks(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "provisioning.md"))
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
	section := func(words ...string) bool {
		for _, h := range headings {
			ok := true
			for _, w := range words {
				ok = ok && strings.Contains(h, w)
			}
			if ok {
				return true
			}
		}
		return false
	}
	for _, v := range []string{"yealink", "poly", "grandstream", "snom", "fanvil"} {
		if !section(v) {
			t.Errorf("no section for %s", v)
		}
	}
	for _, topic := range [][]string{{"adding phones"}, {"csv"}, {"dhcp"}, {"mikrotik"}, {"kea"}, {"manual"}, {"ca", "certificate"}, {"redirect"}, {"template"}, {"token"}, {"security"}} {
		if !section(topic...) {
			t.Errorf("no section on %v", topic)
		}
	}

	public := envValue(controlContainer(t, kwDocs(t)), "HELLO_PROV_PUBLIC_URL")
	ps := store.ProvSettings{PublicURL: public}
	for _, want := range []string{ps.BootURL(), ps.CAURL()} {
		if want == "" || !strings.Contains(guide, want) {
			t.Errorf("the guide lacks %q", want)
		}
	}
	for _, opt := range []string{"66", "160", "43"} {
		if !regexp.MustCompile(`(?i)option[^\n]*\b` + opt + `\b`).MatchString(guide) {
			t.Errorf("the guide never names DHCP option %s", opt)
		}
	}

	for _, f := range []string{filepath.Join("docs", "phones.md"), "README.md"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "provisioning.md)") {
			t.Errorf("%s does not link to the provisioning guide", f)
		}
	}
}
