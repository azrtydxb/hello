package deploy

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type envDeployment struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name string `yaml:"name"`
					Env  []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// TestKwUIUpstreamIsFQDN guards the hello-ui proxy on kw. nginx resolves its
// variable proxy_pass through the `resolver` directive (kube-dns), which
// applies no search domains: a short Service name such as "hello-control"
// never resolves and every /api/ call answers 502. The upstream must be
// <service>.<namespace>.svc.cluster.local of a Service in resources.yaml.
func TestKwUIUpstreamIsFQDN(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []envDeployment
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var d envDeployment
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode resources.yaml: %v", err)
		}
		docs = append(docs, d)
	}

	services := map[string]bool{}
	for _, d := range docs {
		if d.Kind == "Service" {
			services[d.Metadata.Name+"."+d.Metadata.Namespace+".svc.cluster.local"] = true
		}
	}

	var upstream string
	for _, d := range docs {
		if d.Kind != "Deployment" {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name != "hello-ui" {
				continue
			}
			for _, e := range c.Env {
				if e.Name == "HELLO_CONTROL_UPSTREAM" {
					upstream = e.Value
				}
			}
		}
	}
	if upstream == "" {
		t.Fatal("hello-ui container has no HELLO_CONTROL_UPSTREAM in deploy/kuvryn-sync/kw/resources.yaml")
	}
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("HELLO_CONTROL_UPSTREAM %q: %v", upstream, err)
	}
	host := u.Hostname()
	if !strings.HasSuffix(host, ".svc.cluster.local") {
		t.Fatalf("HELLO_CONTROL_UPSTREAM host %q is not a Service FQDN (<svc>.<ns>.svc.cluster.local); nginx's resolver applies no search domains", host)
	}
	if !services[host] {
		t.Fatalf("HELLO_CONTROL_UPSTREAM host %q names no Service in resources.yaml", host)
	}
}
