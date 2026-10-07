package deploy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestUIProxyAllowsFirmwareUploads guards the hello-ui nginx /api/ proxy:
// firmware uploads go up to 512 MB and nginx's 1 MB default answers 413.
func TestUIProxyAllowsFirmwareUploads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "nginx", "default.conf.template"))
	if err != nil {
		t.Fatal(err)
	}
	conf := string(raw)
	i := strings.Index(conf, "location /api/ {")
	if i < 0 {
		t.Fatal("no location /api/ block")
	}
	block := conf[i : i+strings.Index(conf[i:], "\n    }")]
	for _, want := range []string{
		"client_max_body_size 520m;",
		"proxy_request_buffering off;",
		"proxy_read_timeout 600s;",
		"proxy_send_timeout 600s;",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("location /api/ lacks %q", want)
		}
	}
}

// TestKwIngressAllowsFirmwareUploads guards the kw ingress in front of
// hello-ui: ingress-nginx also defaults to a 1 MB body limit.
func TestKwIngressAllowsFirmwareUploads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	type ingress struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name        string            `yaml:"name"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var d ingress
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode resources.yaml: %v", err)
		}
		if d.Kind != "Ingress" || d.Metadata.Name != "hello" {
			continue
		}
		want := map[string]string{
			"nginx.ingress.kubernetes.io/proxy-body-size":         "520m",
			"nginx.ingress.kubernetes.io/proxy-request-buffering": "off",
			"nginx.ingress.kubernetes.io/proxy-read-timeout":      "600",
			"nginx.ingress.kubernetes.io/proxy-send-timeout":      "600",
		}
		for k, v := range want {
			if got := d.Metadata.Annotations[k]; got != v {
				t.Errorf("ingress hello %s = %q, want %q", k, got, v)
			}
		}
		return
	}
	t.Fatal("no Ingress hello in resources.yaml")
}
