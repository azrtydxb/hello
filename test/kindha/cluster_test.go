package kindha

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// The kind lab: the real kw manifests (deploy/kuvryn-sync/kw/resources.yaml)
// applied to a kind cluster with only what a laptop-sized lab must change
// patched in (images, node names, the VIP, the NIC, the API server's address
// and the dispatcher's destination), so what runs is what kw runs.

type labEnv struct {
	nodes       []string // the two worker containers Kamailio runs on
	control     string   // the control-plane container (kube-vip's API server)
	vip         string
	image       string // the Kamailio image loaded into kind
	runnerIP    string // the address the nodes see the runner (the phones and Hello nodes) at
	stubPort    int
	nodeIPs     map[string]string
	namespace   string
	kubectlArgs []string
}

func loadLab(t *testing.T) *labEnv {
	t.Helper()
	if os.Getenv("HELLO_KINDHA") != "1" {
		t.Skip("HELLO_KINDHA=1 not set: this test needs the kind cluster the kamailio-ha CI job creates")
	}
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Fatalf("%s is required", k)
		}
		return v
	}
	l := &labEnv{
		nodes:     strings.Split(need("KINDHA_NODES"), ","),
		control:   need("KINDHA_CONTROL"),
		vip:       need("KINDHA_VIP"),
		image:     need("KINDHA_IMAGE"),
		stubPort:  15060,
		nodeIPs:   map[string]string{},
		namespace: "hello",
	}
	if len(l.nodes) != 2 {
		t.Fatalf("KINDHA_NODES = %v, want two nodes", l.nodes)
	}
	for _, n := range append([]string{l.control}, l.nodes...) {
		l.nodeIPs[n] = strings.TrimSpace(run(t, "docker", "inspect", "-f", `{{(index .NetworkSettings.Networks "kind").IPAddress}}`, n))
	}
	// The source address the nodes will see: the runner's, towards them.
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(l.nodeIPs[l.nodes[0]]), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	l.runnerIP = c.LocalAddr().(*net.UDPAddr).IP.String()
	_ = c.Close()
	if ctx := os.Getenv("KINDHA_CONTEXT"); ctx != "" {
		l.kubectlArgs = []string{"--context", ctx}
	}
	return l
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec // test tooling: the arguments are the lab's own
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func tryRun(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec // test tooling
	return string(out), err
}

// transient reports whether kubectl's output is the API server of a small
// kind cluster on a busy runner stumbling (etcd timeouts, resets), not an
// answer.
func transient(out string) bool {
	for _, s := range []string{"Unable to connect", "EOF", "timed out", "connection reset", "connection refused", "etcdserver", "Forbidden", "TLS handshake", "i/o timeout"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// kubectl runs kubectl and fails the test on an error, retrying a few times
// when the failure looks like the API server being slow.
func (l *labEnv) kubectl(t *testing.T, args ...string) string {
	t.Helper()
	var out string
	var err error
	for i := 0; i < 4; i++ {
		if out, err = l.tryKubectl(args...); err == nil {
			return out
		}
		if !transient(out) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	return ""
}

func (l *labEnv) tryKubectl(args ...string) (string, error) {
	return tryRun("kubectl", append(append([]string{}, l.kubectlArgs...), args...)...)
}

// holder returns the node (of those not excluded) that has the VIP on its NIC.
func (l *labEnv) holders(skip ...string) []string {
	var out []string
	for _, n := range l.nodes {
		if contains(skip, n) {
			continue
		}
		s, err := tryRun("docker", "exec", n, "ip", "-4", "-o", "addr", "show", "dev", "eth0")
		if err == nil && strings.Contains(s, l.vip+"/") {
			out = append(out, n)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// podOn returns the kamailio pod running on node.
func (l *labEnv) podOn(t *testing.T, node string) string {
	t.Helper()
	out := l.kubectl(t, "-n", l.namespace, "get", "pods", "-l", "app.kubernetes.io/component=edge",
		"--field-selector", "spec.nodeName="+node, "-o", "jsonpath={.items[*].metadata.name}")
	f := strings.Fields(out)
	if len(f) != 1 {
		t.Fatalf("pods on %s = %v, want exactly one kamailio pod", node, f)
	}
	return f[0]
}

// readyPods counts the Ready kamailio pods.
func (l *labEnv) readyPods() (int, error) {
	out, err := l.tryKubectl("-n", l.namespace, "get", "pods", "-l", "app.kubernetes.io/component=edge",
		"-o", `jsonpath={range .items[*]}{.metadata.deletionTimestamp}{"|"}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "|True" {
			n++
		}
	}
	return n, nil
}

func (l *labEnv) waitPodsReady(t *testing.T, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var n int
	for time.Now().Before(deadline) {
		var err error
		if n, err = l.readyPods(); err == nil && n == want {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%d kamailio pods Ready, want %d after %s", n, want, d)
}

// waitVIPOn waits until exactly the given node (and no other) holds the VIP.
func (l *labEnv) waitVIP(t *testing.T, d time.Duration, skip ...string) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if h := l.holders(skip...); len(h) == 1 {
			return h[0]
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no single holder of the VIP %s among %v (skipping %v) after %s", l.vip, l.nodes, skip, d)
	return ""
}

func kwResources(t *testing.T) []map[string]any {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []map[string]any
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if len(d) > 0 {
			out = append(out, d)
		}
	}
	return out
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }

func named(l []any, name string) map[string]any {
	for _, e := range l {
		if m := asMap(e); m["name"] == name {
			return m
		}
	}
	return nil
}

// manifests returns the kw edge objects patched for the lab.
func (l *labEnv) manifests(t *testing.T) []byte {
	t.Helper()
	keep := map[string]bool{
		"ConfigMap/hello-kamailio": true, "ServiceAccount/kamailio-vip": true, "Role/kamailio-vip": true,
		"RoleBinding/kamailio-vip": true, "PodDisruptionBudget/kamailio": true, "Deployment/kamailio": true,
	}
	var out bytes.Buffer
	seen := 0
	for _, d := range kwResources(t) {
		key := fmt.Sprint(d["kind"], "/", asMap(d["metadata"])["name"])
		if !keep[key] {
			continue
		}
		seen++
		switch key {
		case "ConfigMap/hello-kamailio":
			// The Hello node is the test itself, on the runner: a name that
			// cluster DNS answers (a headless Service, as on kw) with the
			// runner's address.
			asMap(d["data"])["dispatcher.list"] = fmt.Sprintf("1 sip:hello-sip-1.%s.svc.cluster.local:%d 0 0 duid=hello-sip-1\n", l.namespace, l.stubPort)
		case "Deployment/kamailio":
			l.patchDeployment(t, d)
		}
		b, err := yaml.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString("---\n")
		out.Write(b)
	}
	if seen != len(keep) {
		t.Fatalf("found %d of the %d kw edge objects", seen, len(keep))
	}
	fmt.Fprintf(&out, `---
apiVersion: v1
kind: Service
metadata:
  name: hello-sip-1
  namespace: %[1]s
spec:
  clusterIP: None
  ports:
    - name: sip
      port: %[3]d
      protocol: UDP
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: hello-sip-1
  namespace: %[1]s
  labels:
    kubernetes.io/service-name: hello-sip-1
addressType: IPv4
ports:
  - name: sip
    port: %[3]d
    protocol: UDP
endpoints:
  - addresses: ["%[2]s"]
`, l.namespace, l.runnerIP, l.stubPort)
	return out.Bytes()
}

func (l *labEnv) patchDeployment(t *testing.T, d map[string]any) {
	t.Helper()
	spec := asMap(asMap(asMap(d["spec"])["template"])["spec"])

	// The two kind workers instead of master-11 and master-12.
	term := asMap(asList(asMap(asMap(asMap(spec["affinity"])["nodeAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])["nodeSelectorTerms"])[0])
	expr := asMap(asList(term["matchExpressions"])[0])
	var vals []any
	for _, n := range l.nodes {
		vals = append(vals, n)
	}
	expr["values"] = vals

	// kube-vip's "kubernetes" is the lab's API server, not this node's own.
	asMap(asList(spec["hostAliases"])[0])["ip"] = l.nodeIPs[l.control]

	for _, c := range asList(spec["initContainers"]) {
		asMap(c)["image"] = l.image
	}
	setEnv := func(c map[string]any, name, value string) {
		e := named(asList(c["env"]), name)
		if e == nil {
			t.Fatalf("env %s not in the manifest", name)
		}
		e["value"] = value
	}
	kam := named(asList(spec["containers"]), "kamailio")
	kam["image"] = l.image
	setEnv(kam, "KAMAILIO_PUBLIC_HOST", l.vip)
	setEnv(kam, "KAMAILIO_EDGE_VIP", l.vip)
	vip := named(asList(spec["containers"]), "vip")
	setEnv(vip, "address", l.vip)
	setEnv(vip, "vip_interface", "eth0")
}

// waitAPI waits until the kind API server answers readyz: a loaded runner can
// still be settling etcd when the test starts.
func (l *labEnv) waitAPI(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		out, err := l.tryKubectl("get", "--raw=/readyz")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the API server is not ready after 6m: %v\n%s", err, out)
		}
		time.Sleep(3 * time.Second)
	}
}

// rolloutStatus waits for a rollout, polling again when the API server
// stumbles in the middle of the wait.
func (l *labEnv) rolloutStatus(t *testing.T, deployment string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		out, err := l.tryKubectl("-n", l.namespace, "rollout", "status", "deployment/"+deployment, "--timeout=120s")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollout of %s not done after %s: %v\n%s", deployment, d, err, out)
		}
		time.Sleep(3 * time.Second)
	}
}

// retry runs a kubectl command up to three times: the API server of a small
// kind cluster on a busy runner can time out a request.
func (l *labEnv) retry(t *testing.T, args ...string) {
	t.Helper()
	var out string
	var err error
	for i := 0; i < 3; i++ {
		if out, err = l.tryKubectl(args...); err == nil {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
}

// noMasqueradeForVIP exempts the VIP from kind's masquerading. kind's
// kind-masq-agent source-NATs every new flow that leaves a node for a non-pod
// address, host-network traffic included, so a Kamailio that has just taken
// the VIP would send its first packet to a Hello node from the node's address
// and an ephemeral port instead of from the VIP: not what a LAN does (Cilium
// masquerades pod traffic, never the host's) and not what Hello's symmetric
// answers and the Via it records expect.
func (l *labEnv) noMasqueradeForVIP(t *testing.T) {
	t.Helper()
	for _, n := range l.nodes {
		run(t, "docker", "exec", n, "iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-s", l.vip+"/32", "-j", "RETURN")
	}
}

func (l *labEnv) apply(t *testing.T) {
	t.Helper()
	l.waitAPI(t)
	l.noMasqueradeForVIP(t)
	if _, err := l.tryKubectl("get", "namespace", l.namespace); err != nil {
		l.retry(t, "create", "namespace", l.namespace)
	}
	f := filepath.Join(t.TempDir(), "edge.yaml")
	if err := os.WriteFile(f, l.manifests(t), 0o600); err != nil {
		t.Fatal(err)
	}
	l.retry(t, "apply", "-f", f)
	l.rolloutStatus(t, "kamailio", 10*time.Minute)
}

// diagnostics prints what an operator would look at.
func (l *labEnv) diagnostics(t *testing.T) {
	for _, c := range [][]string{
		{"uptime"}, {"free", "-m"}, {"docker", "stats", "--no-stream", "--format", "{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}"},
		{"docker", "logs", "--tail", "40", l.control},
	} {
		out, _ := tryRun(c[0], c[1:]...)
		t.Logf("%s:\n%s", strings.Join(c, " "), out)
	}
	for _, args := range [][]string{
		{"-n", l.namespace, "get", "pods", "-o", "wide"},
		{"-n", l.namespace, "get", "lease"},
		{"-n", l.namespace, "describe", "pods", "-l", "app.kubernetes.io/component=edge"},
		{"-n", l.namespace, "get", "events", "--sort-by=.lastTimestamp"},
		{"-n", "kube-system", "get", "pods", "-o", "wide"},
		{"-n", l.namespace, "get", "svc,endpointslices"},
	} {
		out, _ := l.tryKubectl(args...)
		t.Logf("kubectl %s:\n%s", strings.Join(args, " "), out)
	}
	for _, n := range l.nodes {
		pods, err := l.tryKubectl("-n", l.namespace, "get", "pods", "-l", "app.kubernetes.io/component=edge",
			"--field-selector", "spec.nodeName="+n, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			pods = ""
		}
		for _, p := range strings.Fields(pods) {
			for _, c := range []string{"wait-dispatcher", "kamailio", "vip"} {
				out, _ := l.tryKubectl("-n", l.namespace, "logs", p, "-c", c, "--tail=60")
				t.Logf("%s %s/%s log:\n%s", n, p, c, out)
				if prev, err := l.tryKubectl("-n", l.namespace, "logs", p, "-c", c, "--previous", "--tail=60"); err == nil {
					t.Logf("%s %s/%s previous log:\n%s", n, p, c, prev)
				}
			}
		}
		out, _ := tryRun("docker", "exec", n, "ip", "-4", "-o", "addr", "show", "dev", "eth0")
		t.Logf("%s eth0: %s", n, out)
	}
}
