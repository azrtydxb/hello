// Package deploy guards the kw deployment manifests against drifting from
// the sources they copy. It needs no services and runs in CI's go job.
package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// kwKamailioSubstitutions is the complete, explicit list of differences
// allowed between deploy/kamailio/kamailio.cfg (the lab source) and the
// kamailio.cfg carried by the hello-kamailio ConfigMap in
// deploy/kuvryn-sync/kw/resources.yaml. Each entry is applied to the source
// (old -> new) before comparing; an entry whose old text is missing from the
// source fails the test, so the list cannot go stale either.
//
// It is empty on purpose: every kw-specific value is supplied through the
// cfg's #!trydefenv parameters by the kamailio Deployment's env
// (KAMAILIO_LISTEN_IP = pod IP, KAMAILIO_PUBLIC_HOST = 192.168.10.101,
// KAMAILIO_PUBLIC_PORT = 30508, KAMAILIO_INVITE_TIMEOUT) and the kw
// dispatcher.list (Service names instead of the lab's fixed IPs), which is a
// separate ConfigMap key and not compared here. Prefer a new #!trydefenv
// parameter over adding an entry.
var kwKamailioSubstitutions = []struct{ old, new, why string }{}

// configChecksumAnnotation on the kamailio pod template is the sha256 of the
// ConfigMap's kamailio.cfg followed by its dispatcher.list. Kamailio reads
// its config only at startup and Sync does not restart pods on a ConfigMap
// change, so a config change must change this annotation (and roll the pod).
const configChecksumAnnotation = "hello.watteel.com/kamailio-config-sha256"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

type manifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Template struct {
			Metadata struct {
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func kwManifests(t *testing.T) []manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []manifest
	for {
		var m manifest
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return out
			}
			t.Fatalf("decode resources.yaml: %v", err)
		}
		out = append(out, m)
	}
}

func find(t *testing.T, ms []manifest, kind, name string) manifest {
	t.Helper()
	for _, m := range ms {
		if m.Kind == kind && m.Metadata.Name == name {
			return m
		}
	}
	t.Fatalf("%s/%s not found in deploy/kuvryn-sync/kw/resources.yaml", kind, name)
	return manifest{}
}

// TestKwKamailioConfigMatchesSource fails when the hello-kamailio ConfigMap's
// kamailio.cfg differs from deploy/kamailio/kamailio.cfg beyond the
// documented kwKamailioSubstitutions. Fix: copy the source into the
// ConfigMap (indented four spaces) and update the checksum annotation.
func TestKwKamailioConfigMatchesSource(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "kamailio.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	want := string(src)
	for _, s := range kwKamailioSubstitutions {
		if !strings.Contains(want, s.old) {
			t.Fatalf("substitution %q (%s): old text not in deploy/kamailio/kamailio.cfg", s.old, s.why)
		}
		want = strings.ReplaceAll(want, s.old, s.new)
	}

	cm := find(t, kwManifests(t), "ConfigMap", "hello-kamailio")
	got, ok := cm.Data["kamailio.cfg"]
	if !ok {
		t.Fatal("ConfigMap hello-kamailio has no kamailio.cfg key")
	}
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("kw ConfigMap kamailio.cfg drifted from deploy/kamailio/kamailio.cfg at line %d:\n  configmap: %q\n  source:    %q", i+1, g, w)
			}
		}
	}
}

// TestKwKamailioConfigChecksum fails when the kamailio pod template's
// checksum annotation does not match the ConfigMap, i.e. a config change
// that would not restart Kamailio.
func TestKwKamailioConfigChecksum(t *testing.T) {
	ms := kwManifests(t)
	cm := find(t, ms, "ConfigMap", "hello-kamailio")
	h := sha256.New()
	h.Write([]byte(cm.Data["kamailio.cfg"]))
	h.Write([]byte(cm.Data["dispatcher.list"]))
	want := hex.EncodeToString(h.Sum(nil))

	dep := find(t, ms, "Deployment", "kamailio")
	if got := dep.Spec.Template.Metadata.Annotations[configChecksumAnnotation]; got != want {
		t.Fatalf("Deployment kamailio pod template annotation %s = %q, want %q (sha256 of the hello-kamailio kamailio.cfg + dispatcher.list)", configChecksumAnnotation, got, want)
	}
}
