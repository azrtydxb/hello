package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The chart tests render deploy/helm/hello with `helm template` (a tool, not
// a service: nothing is started) and assert the invariants the Kubernetes
// spec (.procoder/specs/kubernetes.md) promises. The kw manifests stay the
// source of truth for env vars, ports and probes, so parity with them is
// asserted here.
//
// Without helm on PATH the tests skip and say so, unless HELLO_REQUIRE_HELM=1
// (set by the CI job that installs it), where a missing helm is a failure.

type object = map[string]any

func chartDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "deploy", "helm", "hello")
}

func requireTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("HELLO_REQUIRE_HELM") == "1" {
			t.Fatalf("%s is not on PATH but HELLO_REQUIRE_HELM=1", name)
		}
		t.Skipf("%s is not on PATH; the chart tests need it (HELLO_REQUIRE_HELM=1 makes this a failure)", name)
	}
	return path
}

// helmTemplate renders the chart as release "hello" in namespace "hello-test"
// and returns the raw output; args are extra `helm template` arguments.
func helmTemplate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	helm := requireTool(t, "helm")
	cmd := exec.Command(helm, append([]string{"template", "hello", chartDir(t), "--namespace", "hello-test"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("helm template %v: %w\n%s", args, err, errb.String())
	}
	return out.String(), nil
}

func decodeAll(t *testing.T, raw string) []object {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(raw))
	var out []object
	for {
		var o object
		if err := dec.Decode(&o); err != nil {
			if errors.Is(err, io.EOF) {
				return out
			}
			t.Fatalf("decode rendered manifests: %v", err)
		}
		if len(o) > 0 {
			out = append(out, o)
		}
	}
}

func render(t *testing.T, args ...string) []object {
	t.Helper()
	raw, err := helmTemplate(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	return decodeAll(t, raw)
}

func valuesFile(t *testing.T, name string) string {
	return filepath.Join(chartDir(t), name)
}

// evalRender is the default install: everything bundled (values-eval.yaml).
func evalRender(t *testing.T, extra ...string) []object {
	t.Helper()
	return render(t, append([]string{"-f", valuesFile(t, "values-eval.yaml")}, extra...)...)
}

// haRender is the highly available layout (values-ha.yaml).
func haRender(t *testing.T, extra ...string) []object {
	t.Helper()
	return render(t, append([]string{"-f", valuesFile(t, "values-ha.yaml")}, extra...)...)
}

// at walks a decoded document: string keys into maps, ints into lists. A
// missing step yields nil.
func dig(v any, path ...any) any {
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

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func intOf(t *testing.T, v any) int {
	t.Helper()
	n, err := strconv.Atoi(str(v))
	if err != nil {
		t.Fatalf("%v is not an integer", v)
	}
	return n
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func findObj(t *testing.T, objs []object, kind, name string) object {
	t.Helper()
	for _, o := range objs {
		if str(o["kind"]) == kind && str(dig(o, "metadata", "name")) == name {
			return o
		}
	}
	names := []string{}
	for _, o := range objs {
		names = append(names, str(o["kind"])+"/"+str(dig(o, "metadata", "name")))
	}
	t.Fatalf("%s/%s not rendered; have %v", kind, name, names)
	return nil
}

func hasObj(objs []object, kind, name string) bool {
	for _, o := range objs {
		if str(o["kind"]) == kind && str(dig(o, "metadata", "name")) == name {
			return true
		}
	}
	return false
}

func podSpec(o object) any { return dig(o, "spec", "template", "spec") }

func containerNamed(t *testing.T, o object, init bool, name string) object {
	t.Helper()
	key := "containers"
	if init {
		key = "initContainers"
	}
	for _, c := range list(dig(podSpec(o), key)) {
		if str(dig(c, "name")) == name {
			return c.(object)
		}
	}
	t.Fatalf("%s/%s has no %s %q", o["kind"], dig(o, "metadata", "name"), key, name)
	return nil
}

// envOf returns a container's env as name -> the env entry.
func envOf(c object) map[string]object {
	out := map[string]object{}
	for _, e := range list(c["env"]) {
		m := e.(object)
		out[str(m["name"])] = m
	}
	return out
}

// TestChartDefaultRender fails if the default (evaluation) render lacks any
// of hello-control, hello-sip, Kamailio or hello-ui, or hello-control lacks
// two replicas and the migrate init container.
func TestChartDefaultRender(t *testing.T) {
	objs := evalRender(t)

	control := findObj(t, objs, "Deployment", "hello-control")
	if n := intOf(t, dig(control, "spec", "replicas")); n != 2 {
		t.Errorf("hello-control replicas = %d, want 2", n)
	}
	migrate := containerNamed(t, control, true, "migrate")
	if !strings.Contains(strings.Join(strOf(list(migrate["command"])), " "), "hello-control migrate up") {
		t.Errorf("migrate init container command = %v, want hello-control migrate up", migrate["command"])
	}
	for _, kind := range []struct{ kind, name string }{
		{"StatefulSet", "hello-sip"},
		{"Service", "hello-sip"},
		{"Deployment", "hello-kamailio"},
		{"ConfigMap", "hello-kamailio"},
		{"Deployment", "hello-ui"},
		{"Service", "hello-ui"},
		{"Service", "hello-control"},
	} {
		findObj(t, objs, kind.kind, kind.name)
	}
	sip := findObj(t, objs, "StatefulSet", "hello-sip")
	if n := intOf(t, dig(sip, "spec", "replicas")); n != 2 {
		t.Errorf("hello-sip nodes = %d, want 2", n)
	}
	if str(dig(findObj(t, objs, "Service", "hello-sip"), "spec", "clusterIP")) != "None" {
		t.Error("the hello-sip Service must be headless: pod names resolve to the pod address")
	}
	if str(dig(podSpec(sip), "hostNetwork")) != "true" {
		t.Error("hello-sip must default to hostNetwork (anchored RTP needs a routable address)")
	}
	kam := findObj(t, objs, "Deployment", "hello-kamailio")
	wait := containerNamed(t, kam, true, "wait-dispatcher")
	if !strings.Contains(strings.Join(strOf(list(wait["command"])), "\n"), "getent hosts") {
		t.Error("kamailio's wait-dispatcher init container must wait for the dispatcher names to resolve")
	}
	// Probes mirror kw: readiness on /readyz and liveness on /healthz.
	for _, c := range []object{containerNamed(t, sip, false, "hello-sip"), containerNamed(t, control, false, "hello-control")} {
		if p := str(dig(c, "readinessProbe", "httpGet", "path")); p != "/readyz" {
			t.Errorf("%s readiness path = %q, want /readyz", c["name"], p)
		}
		if p := str(dig(c, "livenessProbe", "httpGet", "path")); p != "/healthz" {
			t.Errorf("%s liveness path = %q, want /healthz", c["name"], p)
		}
		if dig(c, "resources", "limits", "memory") == nil || dig(c, "resources", "requests", "cpu") == nil {
			t.Errorf("%s has no resource requests and limits", c["name"])
		}
	}
	// Every image names an explicit tag, and none is "latest": an install
	// must not change under a pod restart. (Hello's own images default to
	// "main", which the guide requires production to pin to sha-<commit>.)
	for _, o := range objs {
		if podSpec(o) == nil {
			continue
		}
		for _, key := range []string{"initContainers", "containers"} {
			for _, c := range list(dig(podSpec(o), key)) {
				img := str(dig(c, "image"))
				ref := img[strings.LastIndex(img, "/")+1:]
				tag, ok := "", strings.Contains(ref, ":")
				if ok {
					tag = ref[strings.LastIndex(ref, ":")+1:]
				}
				if !ok || tag == "" || tag == "latest" {
					t.Errorf("%s/%s %s: image %q is not pinned by tag", o["kind"], dig(o, "metadata", "name"), dig(c, "name"), img)
				}
			}
		}
	}
	// The UI Ingress is opt-in; the HA layout turns it on.
	if hasObj(objs, "Ingress", "hello") {
		t.Error("the default render must not create an Ingress")
	}
	if !hasObj(haRender(t), "Ingress", "hello") {
		t.Error("the HA render must create the console Ingress")
	}
}

func strOf(l []any) []string {
	out := make([]string, 0, len(l))
	for _, v := range l {
		out = append(out, str(v))
	}
	return out
}

// kwDeployment returns the named kw Deployment as a generic document.
func kwObject(t *testing.T, kind, name string) object {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range decodeAll(t, string(raw)) {
		if str(o["kind"]) == kind && str(dig(o, "metadata", "name")) == name {
			return o
		}
	}
	t.Fatalf("%s/%s not in the kw manifests", kind, name)
	return nil
}

// kwOnlyEnv lists kw env vars the chart deliberately does not render by
// default, each with its reason; everything else in the kw manifests must
// be in the chart.
var kwOnlyEnv = map[string]string{
	"HELLO_AI_ALLOW_PUBLIC_ENDPOINT":     "kw tuning of the in-product agent; set through control.extraEnv",
	"HELLO_AI_STRUCTURED_OUTPUT":         "kw tuning for its local model; set through control.extraEnv",
	"HELLO_AI_AIOPS_INTERVAL":            "kw tuning; set through control.extraEnv",
	"HELLO_AI_EXPLAIN_MIN_INTERVAL":      "kw tuning; set through control.extraEnv",
	"HELLO_AI_DAILY_TOKEN_BUDGET":        "kw tuning; set through control.extraEnv",
	"HELLO_AI_BACKGROUND_BUDGET_PERCENT": "kw tuning; set through control.extraEnv",
	"HELLO_AI_MAX_CONCURRENCY":           "kw tuning; set through control.extraEnv",
	"HELLO_AI_TIMEOUT":                   "kw tuning; set through control.extraEnv",
}

// kwKamailioChartLag lists Kamailio env vars the kw manifests set and the
// chart does not render yet, each with the kamailio-ha phase that closes the
// gap. The list must be empty once the chart option for two
// instances and the VIP lands (phase 4).
var kwKamailioChartLag = map[string]string{
	"KAMAILIO_EDGE_VIP":  "phase 2: kw binds the phones and hello sockets to the VIP; the chart option follows in phase 4",
	"KAMAILIO_LEGACY_IP": "phase 2: kw serves phones still on the node address; the chart option follows in phase 4",
}

// TestChartEnvParityWithKw fails if a hello-sip, hello-control or Kamailio
// environment variable in the kw manifests is missing from the chart's
// render (other than the documented kw-only tuning), or a variable the kw
// manifests read from a field or a Secret is rendered as a literal.
func TestChartEnvParityWithKw(t *testing.T) {
	// The HA render with every optional feature on, so conditional env vars
	// are rendered too.
	objs := haRender(t,
		"--set", "global.publicUrl=https://hello.example.com",
		"--set", "control.oauthDynamicClientRegistration=true",
		"--set", "control.provisioning.publicUrl=https://prov.hello.example.com",
		"--set", "control.provisioning.trustedProxies=10.0.0.0/8",
		"--set", "control.provisioning.caSecret=hello-prov-tls",
		"--set", "secrets.provRedirectSecret=hello-prov-redirect",
	)
	pairs := []struct {
		name        string
		kw          object
		chart       object
		kwContainer string
		init        bool
		chartInit   bool
	}{
		{"hello-sip", kwObject(t, "Deployment", "hello-sip-1"), findObj(t, objs, "StatefulSet", "hello-sip"), "hello-sip", false, false},
		{"hello-control", kwObject(t, "Deployment", "hello-control"), findObj(t, objs, "Deployment", "hello-control"), "hello-control", false, false},
		{"hello-control migrate", kwObject(t, "Deployment", "hello-control"), findObj(t, objs, "Deployment", "hello-control"), "migrate", true, true},
		{"kamailio", kwObject(t, "Deployment", "kamailio"), findObj(t, objs, "Deployment", "hello-kamailio"), "kamailio", false, false},
		{"hello-ui", kwObject(t, "Deployment", "hello-ui"), findObj(t, objs, "Deployment", "hello-ui"), "hello-ui", false, false},
	}
	// Env vars the chart sets differently on purpose: the node identity of a
	// StatefulSet pod replaces kw's per-node Deployments and preStop stagger.
	for _, p := range pairs {
		kwC := containerNamed(t, p.kw, p.init, p.kwContainer)
		chartC := containerNamed(t, p.chart, p.chartInit, p.kwContainer)
		kwEnv, chartEnv := envOf(kwC), envOf(chartC)
		for name, e := range kwEnv {
			if _, skip := kwOnlyEnv[name]; skip && p.name == "hello-control" {
				continue
			}
			if _, lag := kwKamailioChartLag[name]; lag && p.name == "kamailio" {
				continue
			}
			ce, ok := chartEnv[name]
			if !ok {
				t.Errorf("%s: kw sets %s, the chart does not render it", p.name, name)
				continue
			}
			// Same source kind: a field reference stays a field reference
			// (same field), a Secret reference stays a Secret reference.
			switch {
			case e["valueFrom"] != nil && ce["valueFrom"] == nil:
				t.Errorf("%s: %s is a reference in kw but a literal in the chart", p.name, name)
			case dig(e, "valueFrom", "fieldRef") != nil:
				if got, want := str(dig(ce, "valueFrom", "fieldRef", "fieldPath")), str(dig(e, "valueFrom", "fieldRef", "fieldPath")); got != want && name != "HELLO_MEDIA_ANCHOR_HOST" {
					t.Errorf("%s: %s reads field %q, kw reads %q", p.name, name, got, want)
				}
			case dig(e, "valueFrom", "secretKeyRef") != nil && dig(ce, "valueFrom", "secretKeyRef") == nil:
				t.Errorf("%s: %s must come from a Secret as in kw", p.name, name)
			}
		}
		// Probes and ports: same paths, same container ports.
		for _, probe := range []string{"readinessProbe", "livenessProbe"} {
			if kwC[probe] == nil {
				continue
			}
			if got, want := str(dig(chartC, probe, "httpGet", "path")), str(dig(kwC, probe, "httpGet", "path")); got != want {
				t.Errorf("%s %s path = %q, kw %q", p.name, probe, got, want)
			}
		}
		kwPorts := map[string]string{}
		for _, port := range list(kwC["ports"]) {
			kwPorts[str(dig(port, "name"))] = str(dig(port, "containerPort"))
		}
		chartPorts := map[string]string{}
		for _, port := range list(chartC["ports"]) {
			chartPorts[str(dig(port, "name"))] = str(dig(port, "containerPort"))
		}
		for name, num := range kwPorts {
			// kw moves the Kamailio ports to its node's free ones; the
			// chart's are values.
			if p.name == "kamailio" && (name == "phones" || name == "phones-tcp" || name == "metrics") {
				if _, ok := chartPorts[name]; !ok {
					t.Errorf("kamailio: chart lacks port %q", name)
				}
				continue
			}
			if chartPorts[name] != num {
				t.Errorf("%s: port %q = %q, kw %q", p.name, name, chartPorts[name], num)
			}
		}
		// Security context: everything kw drops, the chart drops.
		if dig(kwC, "securityContext", "allowPrivilegeEscalation") != nil && str(dig(chartC, "securityContext", "allowPrivilegeEscalation")) != "false" {
			t.Errorf("%s: allowPrivilegeEscalation must be false as in kw", p.name)
		}
	}

	// Static values that are not site-specific must equal kw's.
	kwSIP := envOf(containerNamed(t, kwObject(t, "Deployment", "hello-sip-1"), false, "hello-sip"))
	sip := envOf(containerNamed(t, findObj(t, objs, "StatefulSet", "hello-sip"), false, "hello-sip"))
	for _, name := range []string{"HELLO_RTP_PORT_MIN", "HELLO_RTP_PORT_MAX", "HELLO_MEDIA_FORCE_ANCHOR", "HELLO_MEDIA_RECORDING_NOTICE", "HELLO_DRAIN_TIMEOUT"} {
		if str(sip[name]["value"]) != str(kwSIP[name]["value"]) {
			t.Errorf("%s = %q, kw %q", name, sip[name]["value"], kwSIP[name]["value"])
		}
	}
	// The kw hello-sip pod keeps its 120s termination grace and the drain
	// must finish inside it; the chart's grace is longer than its drain.
	grace := intOf(t, dig(podSpec(findObj(t, objs, "StatefulSet", "hello-sip")), "terminationGracePeriodSeconds"))
	if grace < 120 {
		t.Errorf("hello-sip grace = %ds, kw gives 120s", grace)
	}
}

var credentialName = regexp.MustCompile(`(?i)(SECRET|PASSWORD|API_KEY|ACCESS_KEY|TOKEN|NONCE|DSN|DATABASE_URL|_KEY$|_KEY_)`)

// TestChartNoPlaintextSecrets fails if any rendered object is a Secret, a
// credential-named env var carries a literal value instead of a
// secretKeyRef, a rendered value contains a connection string with a
// password, or a values file carries a credential.
func TestChartNoPlaintextSecrets(t *testing.T) {
	for name, objs := range map[string][]object{
		"eval": evalRender(t),
		"ha":   haRender(t, "--set", "control.provisioning.publicUrl=https://p.example.com", "--set", "secrets.provRedirectSecret=r"),
	} {
		for _, o := range objs {
			if str(o["kind"]) == "Secret" {
				t.Errorf("%s render: chart creates Secret/%s; credentials are referenced, never rendered", name, dig(o, "metadata", "name"))
			}
			if podSpec(o) == nil {
				continue
			}
			spec := podSpec(o).(object)
			for _, key := range []string{"initContainers", "containers"} {
				for _, c := range list(spec[key]) {
					cm := c.(object)
					for envName, e := range envOf(cm) {
						if credentialName.MatchString(envName) && dig(e, "valueFrom", "secretKeyRef") == nil {
							// Non-credential names that merely match: the
							// pod's own references and public identifiers.
							if envName == "HELLO_SIP_ADVERTISED_ADDR" {
								continue
							}
							t.Errorf("%s render: %s/%s %s: %s is a literal, want a secretKeyRef", name, o["kind"], dig(o, "metadata", "name"), cm["name"], envName)
						}
					}
				}
			}
		}
	}
	raw, err := helmTemplate(t, "-f", valuesFile(t, "values-eval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`postgres(ql)?://[^\s:@/"]+:[^\s@/"]+@`).FindString(raw); m != "" {
		t.Errorf("the render contains a connection string with a password: %q", m)
	}
	for _, f := range []string{"values.yaml", "values-ha.yaml", "values-eval.yaml"} {
		b, err := os.ReadFile(valuesFile(t, f))
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := yaml.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		walkValues(t, f, "", v)
	}
}

// walkValues fails on a non-empty string under a credential-named key.
func walkValues(t *testing.T, file, path string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			p := path + "." + k
			if s, ok := c.(string); ok && s != "" && regexp.MustCompile(`(?i)^(password|secret|secretkey|dsn|token|apikey|accesskey|nonceSecret)$`).MatchString(k) {
				t.Errorf("%s%s carries a value %q; credentials belong in the existing Secret", file, p, s)
			}
			walkValues(t, file, p, c)
		}
	case []any:
		for i, c := range x {
			walkValues(t, file, fmt.Sprintf("%s[%d]", path, i), c)
		}
	}
}

// matchesLabels reports whether the selector's matchLabels all appear in labels.
func matchesLabels(selector, labels any) bool {
	sel, _ := selector.(object)
	lab, _ := labels.(object)
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if str(lab[k]) != str(v) {
			return false
		}
	}
	return true
}

// TestChartAntiAffinityAndPDBs fails if hello-sip lacks required
// anti-affinity on the hostname topology (selecting its own pods), or
// hello-sip and hello-control lack a PodDisruptionBudget selecting their
// pods in the HA render, or a single-node install gets a budget that would
// block every drain.
func TestChartAntiAffinityAndPDBs(t *testing.T) {
	objs := haRender(t)
	for _, tc := range []struct {
		kind, name, pdb string
		required        bool
	}{
		{"StatefulSet", "hello-sip", "hello-sip", true},
		{"Deployment", "hello-control", "hello-control", true},
	} {
		w := findObj(t, objs, tc.kind, tc.name)
		labels := dig(w, "spec", "template", "metadata", "labels")
		terms := list(dig(podSpec(w), "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution"))
		if len(terms) == 0 {
			t.Errorf("%s has no required podAntiAffinity", tc.name)
			continue
		}
		term := terms[0]
		if str(dig(term, "topologyKey")) != "kubernetes.io/hostname" {
			t.Errorf("%s anti-affinity topologyKey = %q, want kubernetes.io/hostname", tc.name, dig(term, "topologyKey"))
		}
		if !matchesLabels(dig(term, "labelSelector", "matchLabels"), labels) {
			t.Errorf("%s anti-affinity selector %v does not select its own pods %v", tc.name, dig(term, "labelSelector"), labels)
		}
		pdb := findObj(t, objs, "PodDisruptionBudget", tc.pdb)
		if !matchesLabels(dig(pdb, "spec", "selector", "matchLabels"), labels) {
			t.Errorf("PDB %s does not select the %s pods", tc.pdb, tc.name)
		}
		if dig(pdb, "spec", "maxUnavailable") == nil && dig(pdb, "spec", "minAvailable") == nil {
			t.Errorf("PDB %s sets neither maxUnavailable nor minAvailable", tc.pdb)
		}
	}
	sipPDB := findObj(t, objs, "PodDisruptionBudget", "hello-sip")
	if n := intOf(t, dig(sipPDB, "spec", "maxUnavailable")); n != 1 {
		t.Errorf("hello-sip PDB maxUnavailable = %d, want 1: the other node is the survivor", n)
	}
	// Kamailio is a single replica: a budget there would block every drain
	// of its node, so it has none by design (docs/kubernetes.md).
	if hasObj(objs, "PodDisruptionBudget", "hello-kamailio") {
		t.Error("a PodDisruptionBudget on the single-replica Kamailio blocks node drains")
	}
	single := evalRender(t, "--set", "sip.nodes=1")
	if hasObj(single, "PodDisruptionBudget", "hello-sip") {
		t.Error("a single hello-sip node must not get a PodDisruptionBudget (it would block every drain)")
	}
	off := evalRender(t, "--set", "podDisruptionBudget.enabled=false")
	for _, o := range off {
		if str(o["kind"]) == "PodDisruptionBudget" {
			t.Errorf("podDisruptionBudget.enabled=false still renders %s", dig(o, "metadata", "name"))
		}
	}
}

// TestChartUpgradeOrdering fails if hello-sip can update more than one node
// at a time, its grace period does not exceed the drain timeout, its
// dispatcher entries do not match its pods, or Kamailio lacks Recreate, the
// wait-dispatcher init container and a config checksum that follows the
// config.
func TestChartUpgradeOrdering(t *testing.T) {
	objs := haRender(t)
	sip := findObj(t, objs, "StatefulSet", "hello-sip")
	if got := str(dig(sip, "spec", "podManagementPolicy")); got != "OrderedReady" {
		t.Errorf("hello-sip podManagementPolicy = %q, want OrderedReady (one node at a time)", got)
	}
	if got := str(dig(sip, "spec", "updateStrategy", "type")); got != "RollingUpdate" {
		t.Errorf("hello-sip updateStrategy = %q, want RollingUpdate", got)
	}
	if got := str(dig(sip, "spec", "updateStrategy", "rollingUpdate", "maxUnavailable")); got != "" {
		t.Errorf("hello-sip rollingUpdate.maxUnavailable = %q: nodes must update strictly one at a time", got)
	}
	// Kamailio refreshes a replaced pod's address every 5s from cluster DNS,
	// whose default answer TTL is 30s: the next node's drain must wait that out.
	if got := intOf(t, dig(sip, "spec", "minReadySeconds")); got < 35 {
		t.Errorf("hello-sip minReadySeconds = %d: below the 30s DNS TTL plus Kamailio's 5s refresh, a drain can hand its calls to a node Kamailio cannot reach yet", got)
	}
	grace := intOf(t, dig(podSpec(sip), "terminationGracePeriodSeconds"))
	drain := envOf(containerNamed(t, sip, false, "hello-sip"))["HELLO_DRAIN_TIMEOUT"]["value"]
	secs, err := strconv.Atoi(strings.TrimSuffix(str(drain), "s"))
	if err != nil {
		t.Fatalf("HELLO_DRAIN_TIMEOUT = %v", drain)
	}
	if grace <= secs {
		t.Errorf("terminationGracePeriodSeconds %d must exceed HELLO_DRAIN_TIMEOUT %ds, or Kubernetes kills a draining node", grace, secs)
	}
	if t.Failed() {
		return
	}
	// A drain longer than the grace period is refused at render time.
	if _, err := helmTemplate(t, "-f", valuesFile(t, "values-ha.yaml"), "--set", "sip.drainTimeoutSeconds=200"); err == nil {
		t.Error("a drain timeout above the termination grace period must fail to render")
	}

	// Node identity: pod name = HELLO_NODE_ID, advertised under the headless
	// Service the StatefulSet is bound to, and one dispatcher entry per node.
	if got := str(dig(sip, "spec", "serviceName")); got != "hello-sip" {
		t.Errorf("serviceName = %q, want the headless hello-sip Service", got)
	}
	env := envOf(containerNamed(t, sip, false, "hello-sip"))
	if got := str(dig(env["HELLO_NODE_ID"], "valueFrom", "fieldRef", "fieldPath")); got != "metadata.name" {
		t.Errorf("HELLO_NODE_ID reads %q, want metadata.name (the stable pod name)", got)
	}
	adv := str(env["HELLO_SIP_ADVERTISED_ADDR"]["value"])
	if !strings.HasPrefix(adv, "$(HELLO_NODE_ID).hello-sip.hello-test.svc.cluster.local:") {
		t.Errorf("HELLO_SIP_ADVERTISED_ADDR = %q, want the pod DNS name under the headless Service", adv)
	}

	cm := findObj(t, objs, "ConfigMap", "hello-kamailio")
	dispatcher := str(dig(cm, "data", "dispatcher.list"))
	nodes := intOf(t, dig(sip, "spec", "replicas"))
	entries := regexp.MustCompile(`(?m)^1 sip:(\S+):5060 0 0 duid=(\S+)$`).FindAllStringSubmatch(dispatcher, -1)
	if len(entries) != nodes {
		t.Fatalf("dispatcher.list has %d entries for %d nodes:\n%s", len(entries), nodes, dispatcher)
	}
	for i, e := range entries {
		pod := fmt.Sprintf("hello-sip-%d", i)
		if e[2] != pod || e[1] != pod+".hello-sip.hello-test.svc.cluster.local" {
			t.Errorf("dispatcher entry %d = %v, want pod %s under hello-sip.hello-test", i, e[1:], pod)
		}
	}

	kam := findObj(t, objs, "Deployment", "hello-kamailio")
	if got := str(dig(kam, "spec", "strategy", "type")); got != "Recreate" {
		t.Errorf("kamailio strategy = %q, want Recreate (hostNetwork ports)", got)
	}
	containerNamed(t, kam, true, "wait-dispatcher")
	cfg, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "kamailio.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	sum := func(cfg, list string) string {
		h := sha256.Sum256([]byte(cfg + list))
		return hex.EncodeToString(h[:])
	}
	annotation := func(objs []object) string {
		k := findObj(t, objs, "Deployment", "hello-kamailio")
		return str(dig(k, "spec", "template", "metadata", "annotations", configChecksumAnnotation))
	}
	got := annotation(objs)
	if got == "" {
		t.Fatal("the kamailio pod template has no config checksum annotation, so a config change would not roll it")
	}
	if want := sum(string(cfg), strings.TrimSuffix(dispatcher, "\n")); got != want {
		t.Errorf("config checksum = %s, want sha256(kamailio.cfg + dispatcher.list) = %s", got, want)
	}
	if more := annotation(haRender(t, "--set", "sip.nodes=4")); more == got {
		t.Error("changing sip.nodes (the dispatcher list) must change the kamailio config checksum")
	}

	// Kamailio waits for an unanswered INVITE longer than hello rings.
	def := evalRender(t)
	kenv := envOf(containerNamed(t, findObj(t, def, "Deployment", "hello-kamailio"), false, "kamailio"))
	senv := envOf(containerNamed(t, findObj(t, def, "StatefulSet", "hello-sip"), false, "hello-sip"))
	ring, err := durationMS(str(senv["HELLO_SIP_RING_TIMEOUT"]["value"]))
	if err != nil {
		t.Fatal(err)
	}
	if inv := intOf(t, kenv["KAMAILIO_INVITE_TIMEOUT"]["value"]); inv <= ring {
		t.Errorf("KAMAILIO_INVITE_TIMEOUT %dms must exceed HELLO_SIP_RING_TIMEOUT %dms", inv, ring)
	}
}

func durationMS(s string) (int, error) {
	if v, ok := strings.CutSuffix(s, "ms"); ok {
		return strconv.Atoi(v)
	}
	if v, ok := strings.CutSuffix(s, "s"); ok {
		n, err := strconv.Atoi(v)
		return n * 1000, err
	}
	return 0, fmt.Errorf("unsupported duration %q", s)
}

// TestChartStateServices fails if the evaluation render omits the bundled
// services, the production render still bundles them, the Valkey and MinIO
// wiring does not follow the values, or a service that is neither bundled
// nor external renders instead of failing.
func TestChartStateServices(t *testing.T) {
	eval := evalRender(t)
	findObj(t, eval, "StatefulSet", "hello-postgresql")
	findObj(t, eval, "Deployment", "hello-valkey")
	findObj(t, eval, "StatefulSet", "hello-minio")
	sipEnv := envOf(containerNamed(t, findObj(t, eval, "StatefulSet", "hello-sip"), false, "hello-sip"))
	if got := str(sipEnv["HELLO_VALKEY_ADDR"]["value"]); got != "hello-valkey:6379" {
		t.Errorf("bundled HELLO_VALKEY_ADDR = %q", got)
	}
	if got := str(sipEnv["MINIO_ENDPOINT"]["value"]); got != "hello-minio:9000" {
		t.Errorf("bundled MINIO_ENDPOINT = %q", got)
	}
	// The bundled database credentials come from the Secret.
	pg := envOf(containerNamed(t, findObj(t, eval, "StatefulSet", "hello-postgresql"), false, "postgres"))
	for _, n := range []string{"POSTGRES_USER", "POSTGRES_PASSWORD"} {
		if dig(pg[n], "valueFrom", "secretKeyRef") == nil {
			t.Errorf("bundled postgres %s must come from the Secret", n)
		}
	}

	ha := haRender(t)
	for _, o := range ha {
		name := str(dig(o, "metadata", "name"))
		if strings.Contains(name, "postgresql") || strings.Contains(name, "valkey") || strings.Contains(name, "minio") {
			t.Errorf("the HA render bundles %s/%s", o["kind"], name)
		}
	}
	haEnv := envOf(containerNamed(t, findObj(t, ha, "StatefulSet", "hello-sip"), false, "hello-sip"))
	if _, ok := haEnv["HELLO_VALKEY_ADDR"]; ok {
		t.Error("with Sentinels configured HELLO_VALKEY_ADDR must not be set (the app rejects both)")
	}
	if got := str(haEnv["HELLO_VALKEY_SENTINELS"]["value"]); strings.Count(got, ":26379") != 3 {
		t.Errorf("HELLO_VALKEY_SENTINELS = %q, want the three Sentinels", got)
	}
	if got := str(haEnv["HELLO_VALKEY_MASTER"]["value"]); got != "hello" {
		t.Errorf("HELLO_VALKEY_MASTER = %q", got)
	}
	if got := str(haEnv["MINIO_ENDPOINT"]["value"]); got != "minio.databases:9000" {
		t.Errorf("external MINIO_ENDPOINT = %q", got)
	}

	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"no valkey", "Valkey is neither bundled nor external", []string{"--set", "valkey.enabled=false", "--set", "valkey.external.sentinels=null"}},
		{"no minio", "MinIO is neither bundled nor external", []string{"--set", "minio.enabled=false"}},
		{"sentinels without a master", "valkey.external.master is required", []string{"--set", "valkey.enabled=false", "--set", "valkey.external.sentinels={a:26379}"}},
	} {
		_, err := helmTemplate(t, append([]string{"-f", valuesFile(t, "values-eval.yaml")}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a render failure containing %q, got %v", tc.name, tc.want, err)
		}
	}
	// Required values.
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"no secret", "secrets.existingSecret is required", []string{"--set", "secrets.existingSecret="}},
		{"no sip domain", "global.sipDomain is required", []string{"--set", "global.sipDomain="}},
		{"no public host", "kamailio.publicHost is required", []string{"--set", "kamailio.publicHost="}},
	} {
		_, err := helmTemplate(t, append([]string{"-f", valuesFile(t, "values-eval.yaml")}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a render failure containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

// TestChartKamailioConfigMatchesSource fails if the chart's copy of the
// Kamailio config differs from deploy/kamailio/kamailio.cfg, or the rendered
// ConfigMap differs from that copy. Fix: cp deploy/kamailio/kamailio.cfg
// deploy/helm/hello/files/kamailio.cfg.
func TestChartKamailioConfigMatchesSource(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "kamailio.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	copyOf, err := os.ReadFile(filepath.Join(chartDir(t), "files", "kamailio.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, copyOf) {
		t.Fatal("deploy/helm/hello/files/kamailio.cfg differs from deploy/kamailio/kamailio.cfg; copy the source over it")
	}
	cm := findObj(t, evalRender(t), "ConfigMap", "hello-kamailio")
	if got := str(dig(cm, "data", "kamailio.cfg")); got != string(src) {
		t.Error("the rendered hello-kamailio ConfigMap's kamailio.cfg differs from the source")
	}
}

// TestChartKubeconform fails if kubeconform rejects any object either
// layout renders. It skips without kubeconform on PATH.
func TestChartKubeconform(t *testing.T) {
	kc := requireTool(t, "kubeconform")
	for name, args := range map[string][]string{
		"eval":     {"-f", valuesFile(t, "values-eval.yaml"), "--set", "ui.ingress.enabled=true", "--set", "ui.ingress.host=h.example.com"},
		"ha":       {"-f", valuesFile(t, "values-ha.yaml"), "--set", "control.provisioning.publicUrl=https://p.example.com", "--set", "control.provisioning.ingress.enabled=true", "--set", "control.provisioning.ingress.host=p.example.com"},
		"nodeport": {"-f", valuesFile(t, "values-eval.yaml"), "--set", "kamailio.hostNetwork=false", "--set", "kamailio.service.type=NodePort", "--set", "kamailio.service.nodePort=30508", "--set", "sip.hostNetwork=false", "--set", "sip.mediaAnchorHost=podIP"},
	} {
		raw, err := helmTemplate(t, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cmd := exec.Command(kc, "-strict", "-summary", "-kubernetes-version", "1.31.0", "-")
		cmd.Stdin = strings.NewReader(raw)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s render: kubeconform: %v\n%s", name, err, out)
		}
	}
}

// TestChartLint fails if `helm lint --strict` rejects the chart with either
// values file.
func TestChartLint(t *testing.T) {
	helm := requireTool(t, "helm")
	for _, f := range []string{"values-eval.yaml", "values-ha.yaml"} {
		out, err := exec.Command(helm, "lint", "--strict", chartDir(t), "-f", valuesFile(t, f)).CombinedOutput()
		if err != nil {
			t.Errorf("helm lint --strict -f %s: %v\n%s", f, err, out)
		}
	}
}

// TestKubernetesGuideCoversTopics fails if docs/kubernetes.md is missing or
// lacks any topic the spec promises (S-6).
func TestKubernetesGuideCoversTopics(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "kubernetes.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, topic := range []string{
		"hostNetwork", "NodePort", "LoadBalancer", "RTP", "20000", "anti-affinity", "PodDisruptionBudget",
		"CloudNativePG", "Sentinel", "helm upgrade", "existingSecret", "advertised", "NAT",
		"HELLO_SIP_TRUSTED_PROXIES", "Kuvryn Sync", "values-ha.yaml", "Recreate",
	} {
		if !strings.Contains(doc, topic) {
			t.Errorf("docs/kubernetes.md does not mention %q", topic)
		}
	}
	for _, heading := range []string{"## Install", "## SIP networking", "## Placement and disruption", "## Highly available state", "## Upgrades"} {
		if !strings.Contains(doc, "\n"+heading) {
			t.Errorf("docs/kubernetes.md lacks the section %q", heading)
		}
	}
	readme, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "docs/kubernetes.md") {
		t.Error("README.md does not link docs/kubernetes.md")
	}
}
