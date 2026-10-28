package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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

// TestKwKamailioAdvertisesItsOwnNode guards the no-VIP stage: each instance
// binds and advertises its own node address (the pod IP under hostNetwork),
// so master-11 keeps serving 192.168.10.101 for every provisioned phone and
// master-12 serves 192.168.10.102 for a test phone.
func TestKwKamailioAdvertisesItsOwnNode(t *testing.T) {
	c := containerNamed(t, kwObject(t, "Deployment", "kamailio"), false, "kamailio")
	env := envOf(c)
	for _, name := range []string{"KAMAILIO_LISTEN_IP", "KAMAILIO_PUBLIC_HOST"} {
		if got := str(dig(env[name], "valueFrom", "fieldRef", "fieldPath")); got != "status.podIP" {
			t.Errorf("%s = %v, want the pod IP (each instance advertises its own node address)", name, env[name])
		}
	}
}

// TestKamailioHARBAC fails if the kw manifests carry a kind the original
// deployer Role does not cover and deploy/kuvryn-sync/rbac-kamailio-ha.yaml
// does not grant. Sync applies the manifests as the kuvryn-sync-deployer
// ServiceAccount; a missing rule fails the release, not a test, so this is
// where the admin step is kept honest.
func TestKamailioHARBAC(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "rbac-kamailio-ha.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	granted := map[string]map[string]bool{} // "group/resource" -> verbs
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var d struct {
			Kind  string `yaml:"kind"`
			Rules []struct {
				APIGroups []string `yaml:"apiGroups"`
				Resources []string `yaml:"resources"`
				Verbs     []string `yaml:"verbs"`
			} `yaml:"rules"`
		}
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d.Kind != "Role" {
			continue
		}
		for _, r := range d.Rules {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					k := g + "/" + res
					if granted[k] == nil {
						granted[k] = map[string]bool{}
					}
					for _, v := range r.Verbs {
						granted[k][v] = true
					}
				}
			}
		}
	}
	// What the original deployer Role (bootstrap, README) covers.
	covered := map[string]bool{
		"/configmaps": true, "apps/deployments": true, "networking.k8s.io/ingresses": true,
		"/services": true, "/secrets": true, "/serviceaccounts": true,
		"rbac.authorization.k8s.io/roles": true, "rbac.authorization.k8s.io/rolebindings": true,
	}
	// kind -> "group/resource" for every kind the manifests may carry.
	resourceOf := map[string]string{
		"ConfigMap": "/configmaps", "Deployment": "apps/deployments", "Ingress": "networking.k8s.io/ingresses",
		"Service": "/services", "Secret": "/secrets", "ServiceAccount": "/serviceaccounts",
		"Role": "rbac.authorization.k8s.io/roles", "RoleBinding": "rbac.authorization.k8s.io/rolebindings",
		"PodDisruptionBudget": "policy/poddisruptionbudgets", "Certificate": "cert-manager.io/certificates",
		"Lease": "coordination.k8s.io/leases",
	}
	raw, err = os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range decodeAll(t, string(raw)) {
		seen[str(o["kind"])] = true
	}
	for kind := range seen {
		res, known := resourceOf[kind]
		if !known {
			t.Errorf("kw manifests carry kind %s: add it to this test's resource table and say how the deployer is granted it", kind)
			continue
		}
		if covered[res] {
			continue
		}
		verbs := granted[res]
		for _, v := range []string{"get", "create", "update", "patch"} {
			if !verbs[v] {
				t.Errorf("kw manifests carry %s but rbac-kamailio-ha.yaml does not grant %s on %s", kind, v, res)
			}
		}
	}
}
