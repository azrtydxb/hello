package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestKamailioConfigSyntax runs `kamailio -c` over deploy/kamailio/kamailio.cfg
// in the real Kamailio image, once with no environment (the lab's defaults)
// and once with the exact environment the kw kamailio Deployment sets, so a
// cfg change or an env change that Kamailio rejects (a parse error, an
// unknown parameter, a bad listen line) fails here and not on the node.
//
// It needs a Docker daemon and the built docker/kamailio image, named by
// HELLO_KAMAILIO_IMAGE; CI's kamailio-config job builds it. Without the
// variable the test skips, unless HELLO_REQUIRE_KAMAILIO=1 (set in that job),
// where a missing image is a failure. Never run it against a Docker daemon
// you did not mean to use: it starts short-lived containers.
func TestKamailioConfigSyntax(t *testing.T) {
	image := os.Getenv("HELLO_KAMAILIO_IMAGE")
	if image == "" {
		if os.Getenv("HELLO_REQUIRE_KAMAILIO") == "1" {
			t.Fatal("HELLO_KAMAILIO_IMAGE is not set (HELLO_REQUIRE_KAMAILIO=1)")
		}
		t.Skip("HELLO_KAMAILIO_IMAGE is not set: no Kamailio image to check the config with")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		if os.Getenv("HELLO_REQUIRE_KAMAILIO") == "1" {
			t.Fatal("docker not on PATH")
		}
		t.Skip("docker not on PATH")
	}

	root := repoRoot(t)
	dir := t.TempDir()
	for _, f := range []string{"kamailio.cfg", "dispatcher.list"} {
		b, err := os.ReadFile(filepath.Join(root, "deploy", "kamailio", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	variants := map[string]map[string]string{"lab defaults": {}}
	for name, env := range kwKamailioEnvs(t) {
		variants[name] = env
	}
	names := make([]string, 0, len(variants))
	for n := range variants {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		env := variants[name]
		t.Run(name, func(t *testing.T) {
			// docker create + cp + start, not a bind mount: in CI the Docker
			// daemon is a sidecar that cannot see the runner's temp files.
			ctr := "kamailio-cfgcheck-" + strings.ReplaceAll(strings.ToLower(name), " ", "-")
			_ = exec.Command(docker, "rm", "-f", ctr).Run()
			args := []string{"create", "--name", ctr}
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				args = append(args, "-e", k+"="+env[k])
			}
			args = append(args, image, "-c", "-f", "/etc/kamailio/kamailio.cfg")
			if out, err := exec.Command(docker, args...).CombinedOutput(); err != nil {
				t.Fatalf("docker create: %v\n%s", err, out)
			}
			t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", ctr).Run() })
			if out, err := exec.Command(docker, "cp", dir+"/.", ctr+":/etc/kamailio/").CombinedOutput(); err != nil {
				t.Fatalf("docker cp: %v\n%s", err, out)
			}
			out, err := exec.Command(docker, "start", "-a", ctr).CombinedOutput()
			if err != nil {
				t.Fatalf("kamailio -c rejected the config with env %v: %v\n%s", env, err, out)
			}
			if !strings.Contains(string(out), "config file ok") {
				t.Fatalf("kamailio -c did not say the config is ok:\n%s", out)
			}
		})
	}
}

// kwKamailioEnvs is the environment of the kw kamailio container: literals
// as written, a status.podIP field reference as a sample node address.
func kwKamailioEnvs(t *testing.T) map[string]map[string]string {
	t.Helper()
	c := containerNamed(t, kwObject(t, "Deployment", "kamailio"), false, "kamailio")
	env := map[string]string{}
	for name, e := range envOf(c) {
		switch {
		case e["value"] != nil:
			env[name] = str(e["value"])
		case str(dig(e, "valueFrom", "fieldRef", "fieldPath")) == "status.podIP":
			env[name] = "192.168.10.101"
		default:
			t.Fatalf("kamailio env %s: neither a literal nor status.podIP; teach kwKamailioEnvs", name)
		}
	}
	return map[string]map[string]string{"kw environment": env}
}
