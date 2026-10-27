package deploy

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Kamailio HA (.procoder/specs/kamailio-ha.md, Option A): two hostNetwork
// Kamailio on master-11 and master-12. These tests guard the shape of that
// pair in the kw manifests: one instance per node, a rollout that never
// takes both down, a PodDisruptionBudget, and the grants the deployer needs
// to apply them. They need no cluster.

// TestKwKamailioPair fails if the kamailio Deployment is not a pair that
// can be rolled one instance at a time: two replicas, RollingUpdate with
// maxSurge 0 and maxUnavailable 1 (hostNetwork ports forbid a surge pod,
// and Sync applies every Deployment at once, so the Deployment itself must
// hold the second instance back), required hostname anti-affinity on its
// own labels, and only the two master nodes allowed.
func TestKwKamailioPair(t *testing.T) {
	dep := kwObject(t, "Deployment", "kamailio")

	replicas := intOf(t, dig(dep, "spec", "replicas"))
	if replicas != 2 {
		t.Errorf("kamailio replicas = %d, want 2 (one per node)", replicas)
	}
	if got := str(dig(dep, "spec", "strategy", "type")); got != "RollingUpdate" {
		t.Errorf("kamailio strategy = %q, want RollingUpdate: Recreate takes both instances down together", got)
	}
	maxUnavail := intOf(t, dig(dep, "spec", "strategy", "rollingUpdate", "maxUnavailable"))
	if maxUnavail != 1 {
		t.Errorf("kamailio maxUnavailable = %d, want 1", maxUnavail)
	}
	if surge := intOf(t, dig(dep, "spec", "strategy", "rollingUpdate", "maxSurge")); surge != 0 {
		t.Errorf("kamailio maxSurge = %d, want 0: a surge pod cannot bind the node's ports beside the pod it replaces", surge)
	}
	// The invariant itself: however a rollout interleaves, at least one
	// instance is available.
	if replicas-maxUnavail < 1 {
		t.Errorf("replicas %d - maxUnavailable %d leaves no Kamailio up during a rollout", replicas, maxUnavail)
	}
	if intOf(t, dig(dep, "spec", "minReadySeconds")) < 1 {
		t.Error("kamailio minReadySeconds must be set: the replacement must be up (dispatcher probes run) before its peer is touched")
	}
	if str(dig(podSpec(dep), "hostNetwork")) != "true" {
		t.Error("kamailio must run with hostNetwork: phones must reach it with no NAT in between")
	}
	if dig(podSpec(dep), "nodeSelector") != nil {
		t.Error("kamailio still has a nodeSelector: two instances need a node affinity over both nodes")
	}

	labels := dig(dep, "spec", "template", "metadata", "labels")
	terms := list(dig(podSpec(dep), "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution"))
	if len(terms) != 1 {
		t.Fatalf("kamailio needs one required podAntiAffinity term, has %d", len(terms))
	}
	if got := str(dig(terms[0], "topologyKey")); got != "kubernetes.io/hostname" {
		t.Errorf("anti-affinity topologyKey = %q, want kubernetes.io/hostname", got)
	}
	if !matchesLabels(dig(terms[0], "labelSelector", "matchLabels"), labels) {
		t.Errorf("anti-affinity selector %v does not select the kamailio pods %v", dig(terms[0], "labelSelector"), labels)
	}

	var nodes []string
	for _, term := range list(dig(podSpec(dep), "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")) {
		for _, expr := range list(dig(term, "matchExpressions")) {
			if str(dig(expr, "key")) == "kubernetes.io/hostname" && str(dig(expr, "operator")) == "In" {
				nodes = append(nodes, strOf(list(dig(expr, "values")))...)
			}
		}
	}
	if got, want := strings.Join(nodes, ","), "master-11,master-12"; got != want {
		t.Errorf("kamailio node affinity = %q, want %q (192.168.10.101 and .102)", got, want)
	}

	// The probes that gate a rollout step and take a dead instance out.
	c := containerNamed(t, dep, false, "kamailio")
	if c["readinessProbe"] == nil {
		t.Error("kamailio has no readinessProbe: a rollout step would not wait for the replacement")
	}
}

// TestKwKamailioPDB fails if the kamailio pods have no PodDisruptionBudget
// that keeps one instance up through a node drain (kured reboots the nodes).
func TestKwKamailioPDB(t *testing.T) {
	dep := kwObject(t, "Deployment", "kamailio")
	pdb := kwObject(t, "PodDisruptionBudget", "kamailio")
	labels := dig(dep, "spec", "template", "metadata", "labels")
	if !matchesLabels(dig(pdb, "spec", "selector", "matchLabels"), labels) {
		t.Errorf("PDB kamailio selects %v, not the kamailio pods %v", dig(pdb, "spec", "selector", "matchLabels"), labels)
	}
	replicas := intOf(t, dig(dep, "spec", "replicas"))
	allowed := 0
	switch {
	case dig(pdb, "spec", "maxUnavailable") != nil:
		allowed = intOf(t, dig(pdb, "spec", "maxUnavailable"))
	case dig(pdb, "spec", "minAvailable") != nil:
		allowed = replicas - intOf(t, dig(pdb, "spec", "minAvailable"))
	default:
		t.Fatal("PDB kamailio sets neither maxUnavailable nor minAvailable")
	}
	if allowed != 1 {
		t.Errorf("PDB kamailio allows %d disruptions of %d instances, want exactly 1", allowed, replicas)
	}
	if got := str(dig(pdb, "metadata", "namespace")); got != "hello" {
		t.Errorf("PDB kamailio namespace = %q, want hello", got)
	}
}

// kwVIP is the address phones are moved to (decision "Kamailio HA option and
// VIP", 2026-10-26): outside Cilium LB-IPAM's 192.168.10.120-159 and not a
// node.
const kwVIP = "192.168.10.109"

// TestKwKamailioVIP guards the VIP stage: every address a dialog names is the
// VIP (advertised host, bound sockets, kube-vip's address), the node address
// stays served for phones not yet moved, kube-vip is the mechanism already
// running on the cluster (same image) electing on its own Lease, and its VIP
// follows the health of the Kamailio beside it.
func TestKwKamailioVIP(t *testing.T) {
	dep := kwObject(t, "Deployment", "kamailio")
	kam := containerNamed(t, dep, false, "kamailio")
	env := envOf(kam)
	for _, name := range []string{"KAMAILIO_PUBLIC_HOST", "KAMAILIO_EDGE_VIP"} {
		if got := str(env[name]["value"]); got != kwVIP {
			t.Errorf("%s = %q, want the VIP %s: the phone-facing and Hello-facing names must survive the instance", name, got, kwVIP)
		}
	}
	for _, name := range []string{"KAMAILIO_LISTEN_IP", "KAMAILIO_LEGACY_IP"} {
		if got := str(dig(env[name], "valueFrom", "fieldRef", "fieldPath")); got != "status.podIP" {
			t.Errorf("%s = %v, want the pod IP: the node address stays served until the last phone has moved", name, env[name])
		}
	}

	// The VIP is not an address anything else owns.
	if ip := net.ParseIP(kwVIP).To4(); ip == nil || ip[3] >= 120 && ip[3] <= 159 || ip[3] >= 101 && ip[3] <= 108 || ip[3] == 100 || ip[3] == 245 || ip[3] == 246 {
		t.Errorf("VIP %s is inside Cilium LB-IPAM's 120-159, a node (101-108, 245, 246) or the API server's .100", kwVIP)
	}

	// Hello trusts the edge by address: the VIP is in HELLO_SIP_TRUSTED_PROXIES.
	for _, n := range []string{"hello-sip-1", "hello-sip-2"} {
		sip := envOf(containerNamed(t, kwObject(t, "Deployment", n), false, "hello-sip"))
		trusted := false
		for _, cidr := range strings.Split(str(sip["HELLO_SIP_TRUSTED_PROXIES"]["value"]), ",") {
			if _, nw, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil && nw.Contains(net.ParseIP(kwVIP)) {
				trusted = true
			}
		}
		if !trusted {
			t.Errorf("%s: HELLO_SIP_TRUSTED_PROXIES %q does not cover the VIP %s", n, sip["HELLO_SIP_TRUSTED_PROXIES"]["value"], kwVIP)
		}
	}

	vip := containerNamed(t, dep, false, "vip")
	venv := envOf(vip)
	want := map[string]string{
		"vip_arp": "true", "address": kwVIP, "vip_interface": "bond0", "cp_enable": "true",
		"cp_namespace": "hello", "svc_enable": "false", "vip_leaderelection": "true",
		"vip_leasename": "kamailio-vip", "vip_leaseduration": "5",
	}
	for k, v := range want {
		if got := str(venv[k]["value"]); got != v {
			t.Errorf("vip env %s = %q, want %q", k, got, v)
		}
	}
	if venv["vip_leasename"]["value"] == "plndr-cp-lock" {
		t.Error("vip must not share the API server's Lease")
	}
	// The same image as the DaemonSet that already holds .100 on this cluster.
	if got := str(vip["image"]); got != "ghcr.io/kube-vip/kube-vip:v1.2.3" {
		t.Errorf("vip image = %q, want ghcr.io/kube-vip/kube-vip:v1.2.3 (the version kube-system/kube-vip-ds runs)", got)
	}
	caps := strOf(list(dig(vip, "securityContext", "capabilities", "add")))
	sort.Strings(caps)
	if strings.Join(caps, ",") != "NET_ADMIN,NET_RAW" {
		t.Errorf("vip capabilities = %v, want NET_ADMIN and NET_RAW", caps)
	}
	// The VIP follows Kamailio: both probes ask Kamailio's own /healthz.
	metrics := str(env["KAMAILIO_METRICS_PORT"]["value"])
	for _, probe := range []string{"livenessProbe", "startupProbe"} {
		if got := str(dig(vip, probe, "httpGet", "path")); got != "/healthz" {
			t.Errorf("vip %s path = %q, want /healthz of the Kamailio beside it", probe, got)
		}
		if got := str(dig(vip, probe, "httpGet", "port")); got != metrics {
			t.Errorf("vip %s port = %q, want Kamailio's metrics port %s", probe, got, metrics)
		}
	}
	live := intOf(t, dig(vip, "livenessProbe", "periodSeconds")) * intOf(t, dig(vip, "livenessProbe", "failureThreshold"))
	if live > 10 {
		t.Errorf("vip liveness takes %ds to hand the VIP over, want at most 10s", live)
	}

	// Its identity: a dedicated ServiceAccount that can only use Leases, its
	// token mounted into the vip container alone.
	spec := podSpec(dep)
	if got := str(dig(spec, "serviceAccountName")); got != "kamailio-vip" {
		t.Errorf("serviceAccountName = %q, want kamailio-vip", got)
	}
	if str(dig(spec, "automountServiceAccountToken")) != "false" {
		t.Error("automountServiceAccountToken must be false: only the vip container gets the token (projected volume)")
	}
	role := kwObject(t, "Role", "kamailio-vip")
	for _, r := range list(dig(role, "rules")) {
		if got := strings.Join(strOf(list(dig(r, "resources"))), ","); got != "leases" {
			t.Errorf("Role kamailio-vip grants %q, want leases only", got)
		}
	}
	kwObject(t, "RoleBinding", "kamailio-vip")
	kwObject(t, "ServiceAccount", "kamailio-vip")
}

// TestKamailioHARBAC fails if the kw manifests need a grant the deployer
// does not have. Sync applies them as the kuvryn-sync-deployer
// ServiceAccount; a missing rule fails the release, not a test, so this is
// where the admin step (deploy/kuvryn-sync/rbac-kamailio-ha.yaml) is kept
// honest. Two checks: every kind in kw/resources.yaml is covered with the
// verbs Sync needs (create and update), and every Role in it grants only
// rules the deployer holds itself (Kubernetes refuses to create or bind a
// Role that would escalate).
func TestKamailioHARBAC(t *testing.T) {
	type rule struct{ group, resource, verb string }
	have := map[rule]bool{}
	add := func(groups, resources, verbs []string) {
		for _, g := range groups {
			for _, r := range resources {
				for _, v := range verbs {
					have[rule{g, r, v}] = true
				}
			}
		}
	}
	rw := []string{"get", "list", "watch", "create", "update", "patch"}
	// The original bootstrap Role (README, "Bootstrap").
	add([]string{""}, []string{"configmaps", "services", "secrets", "serviceaccounts"}, rw)
	add([]string{"apps"}, []string{"deployments"}, rw)
	add([]string{"networking.k8s.io"}, []string{"ingresses"}, rw)
	add([]string{"rbac.authorization.k8s.io"}, []string{"roles", "rolebindings"}, []string{"get", "list", "watch", "create"})

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "rbac-kamailio-ha.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range decodeAll(t, string(raw)) {
		if str(o["kind"]) != "Role" {
			continue
		}
		for _, r := range list(o["rules"]) {
			add(strOf(list(dig(r, "apiGroups"))), strOf(list(dig(r, "resources"))), strOf(list(dig(r, "verbs"))))
		}
	}

	resourceOf := map[string][2]string{
		"ConfigMap": {"", "configmaps"}, "Deployment": {"apps", "deployments"}, "Ingress": {"networking.k8s.io", "ingresses"},
		"Service": {"", "services"}, "Secret": {"", "secrets"}, "ServiceAccount": {"", "serviceaccounts"},
		"Role": {"rbac.authorization.k8s.io", "roles"}, "RoleBinding": {"rbac.authorization.k8s.io", "rolebindings"},
		"PodDisruptionBudget": {"policy", "poddisruptionbudgets"},
	}
	raw, err = os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range decodeAll(t, string(raw)) {
		kind := str(o["kind"])
		seen[kind] = true
		if kind != "Role" {
			continue
		}
		for _, r := range list(o["rules"]) {
			for _, g := range strOf(list(dig(r, "apiGroups"))) {
				for _, res := range strOf(list(dig(r, "resources"))) {
					for _, v := range strOf(list(dig(r, "verbs"))) {
						if !have[rule{g, res, v}] {
							t.Errorf("Role %s grants %s on %s/%s, which the deployer does not hold: Kubernetes refuses the escalation; add it to rbac-kamailio-ha.yaml", dig(o, "metadata", "name"), v, g, res)
						}
					}
				}
			}
		}
	}
	for kind := range seen {
		res, known := resourceOf[kind]
		if !known {
			t.Errorf("kw manifests carry kind %s: add it to this test's resource table and say how the deployer is granted it", kind)
			continue
		}
		for _, v := range []string{"get", "create", "update", "patch"} {
			if !have[rule{res[0], res[1], v}] {
				t.Errorf("kw manifests carry %s but no Role grants the deployer %s on %s/%s", kind, v, res[0], res[1])
			}
		}
	}
}
