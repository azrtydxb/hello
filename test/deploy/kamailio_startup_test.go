package deploy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type kamailioContainer struct {
	Name         string   `yaml:"name"`
	Image        string   `yaml:"image"`
	Command      []string `yaml:"command"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
	} `yaml:"volumeMounts"`
}

func kwKamailioPod(t *testing.T) (initContainers, containers []kamailioContainer) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						InitContainers []kamailioContainer `yaml:"initContainers"`
						Containers     []kamailioContainer `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatal("no Deployment kamailio")
			}
			t.Fatal(err)
		}
		if d.Kind == "Deployment" && d.Metadata.Name == "kamailio" {
			return d.Spec.Template.Spec.InitContainers, d.Spec.Template.Spec.Containers
		}
	}
}

// scriptSed returns the sed program (the quoted 's/.../p' argument) of the
// wait-dispatcher script.
func scriptSed(script string) string {
	const marker = "sed -n '"
	i := strings.Index(script, marker)
	if i < 0 {
		return ""
	}
	rest := script[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestKwKamailioWaitsForDispatcherNames guards the clean start of Kamailio on
// a rollout. With ds_load_mode 1 the dispatcher exits 255 at startup when a
// destination name does not resolve ("could not resolve
// hello-sip-2.hello.svc.cluster.local"), and during a rollout the headless
// hello-sip-N Services have no DNS record until the new pods are ready, so
// Kamailio crash-looped (CrashLoopBackOff) on every deploy. While ds_load_mode
// is 1, the kamailio pod must have an init container, on the same image, that
// waits (bounded, logged) for every host in the mounted dispatcher.list.
func TestKwKamailioWaitsForDispatcherNames(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "kamailio.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"ds_load_mode", 1`) {
		t.Skip("ds_load_mode is not 1: an unresolved name is not fatal at startup")
	}

	list := find(t, kwManifests(t), "ConfigMap", "hello-kamailio").Data["dispatcher.list"]
	inits, mains := kwKamailioPod(t)
	if len(mains) != 1 {
		t.Fatalf("kamailio Deployment has %d containers, want 1", len(mains))
	}
	var wait *kamailioContainer
	for i := range inits {
		if inits[i].Name == "wait-dispatcher" {
			wait = &inits[i]
		}
	}
	if wait == nil {
		t.Fatal("kamailio Deployment has no wait-dispatcher init container: Kamailio exits 255 when a hello-sip name does not resolve at start, i.e. crash-loops on every rollout")
	}
	if wait.Image != mains[0].Image {
		t.Errorf("wait-dispatcher image = %q, want the kamailio image %q (no extra image to pin)", wait.Image, mains[0].Image)
	}
	script := strings.Join(wait.Command, " ")
	for _, want := range []string{"/etc/kamailio/dispatcher.list", "getent hosts", "limit=", "exit 0", "echo "} {
		if !strings.Contains(script, want) {
			t.Errorf("wait-dispatcher script lacks %q: it must read the mounted list, resolve each host, be bounded, and log", want)
		}
	}
	mounted := false
	for _, vm := range wait.VolumeMounts {
		mounted = mounted || (vm.Name == "config" && vm.MountPath == "/etc/kamailio")
	}
	if !mounted {
		t.Error("wait-dispatcher must mount the hello-kamailio config volume at /etc/kamailio")
	}

	// Run the script's host extraction against the real kw list: it must
	// find every destination host.
	prog := scriptSed(script)
	if prog == "" {
		t.Fatal("cannot find the host-extraction sed in the wait-dispatcher script")
	}
	cmd := exec.Command("sed", "-n", prog)
	cmd.Stdin = strings.NewReader(list)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("sed unavailable: %v", err)
	}
	got := strings.Join(strings.Fields(string(out)), ",")
	var want []string
	for _, l := range strings.Split(list, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && !strings.HasPrefix(l, "#") {
			want = append(want, strings.SplitN(f[1], ":", 3)[1])
		}
	}
	if len(want) == 0 || got != strings.Join(want, ",") {
		t.Errorf("wait-dispatcher extracts hosts %q from dispatcher.list, want %q", got, strings.Join(want, ","))
	}
}
