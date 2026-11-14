package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kwFile(t *testing.T, name string) []object {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kuvryn-sync", "kw", name))
	if err != nil {
		t.Fatal(err)
	}
	return decodeAll(t, string(raw))
}

func secretRef(c object, env string) (string, string) {
	e := envOf(c)[env]
	return str(dig(e, "valueFrom", "secretKeyRef", "name")), str(dig(e, "valueFrom", "secretKeyRef", "key"))
}

// TestKwCoturn fails if the kw coturn servers are not one host-networked
// Deployment per node address on master-11 and master-12 with their own
// address as the relay address, the 3478, 5349 and relay ports the softphone
// is told about, the secret from the hello-coturn Secret and never a
// literal, or if hello-control is not told the same addresses.
func TestKwCoturn(t *testing.T) {
	objs := kwFile(t, "coturn.yaml")
	var urls []string
	for _, srv := range []struct{ name, node, ip string }{
		{"coturn-1", "master-11", "192.168.10.101"}, {"coturn-2", "master-12", "192.168.10.102"},
	} {
		d := findObj(t, objs, "Deployment", srv.name)
		if dig(d, "spec", "template", "spec", "hostNetwork") != true {
			t.Errorf("%s: not host-networked", srv.name)
		}
		if str(dig(d, "spec", "strategy", "type")) != "Recreate" || str(dig(d, "spec", "replicas")) != "1" {
			t.Errorf("%s: want one replica, Recreate", srv.name)
		}
		if str(dig(d, "spec", "template", "spec", "nodeSelector", "kubernetes.io/hostname")) != srv.node {
			t.Errorf("%s: not pinned to %s", srv.name, srv.node)
		}
		c := containerNamed(t, d, false, "coturn")
		env := envOf(c)
		if str(env["TURN_EXTERNAL_IP"]["value"]) != srv.ip {
			t.Errorf("%s: relay address %v, want %s", srv.name, env["TURN_EXTERNAL_IP"]["value"], srv.ip)
		}
		for k, v := range map[string]string{"TURN_PORT": "3478", "TURN_TLS_PORT": "5349", "TURN_MIN_PORT": "49152", "TURN_MAX_PORT": "49999"} {
			if str(env[k]["value"]) != v {
				t.Errorf("%s: %s = %v, want %s", srv.name, k, env[k]["value"], v)
			}
		}
		if n, k := secretRef(c, "TURN_SECRET"); n != "hello-coturn" || k != "turnSecret" {
			t.Errorf("%s: TURN_SECRET comes from %s/%s", srv.name, n, k)
		}
		if peers := str(env["TURN_ALLOWED_PEERS"]["value"]); peers == "" || strings.Contains(peers, "0.0.0.0") {
			t.Errorf("%s: allowed peers %q", srv.name, peers)
		}
		for _, port := range []string{"3478", "5349"} {
			urls = append(urls, srv.ip+":"+port)
		}
	}
	if len(findAll(objs, "Certificate", "hello-coturn-tls")) != 1 {
		t.Error("no hello-coturn-tls Certificate")
	}
	ctl := containerNamed(t, kwObject(t, "Deployment", "hello-control"), false, "hello-control")
	turn := str(envOf(ctl)["HELLO_TURN_URLS"]["value"])
	for _, u := range urls {
		if !strings.Contains(turn, u) {
			t.Errorf("HELLO_TURN_URLS lacks %s", u)
		}
	}
	if n, k := secretRef(ctl, "HELLO_TURN_SECRET"); n != "hello-coturn" || k != "turnSecret" {
		t.Errorf("hello-control HELLO_TURN_SECRET comes from %s/%s", n, k)
	}
	for _, who := range []struct{ name, container string }{{"hello-control", "hello-control"}, {"hello-sip-1", "hello-sip"}, {"hello-sip-2", "hello-sip"}} {
		c := containerNamed(t, kwObject(t, "Deployment", who.name), false, who.container)
		if n, k := secretRef(c, "HELLO_CLIENT_AUTH_KEY"); n != "hello-client-auth" || k != "clientAuthKey" {
			t.Errorf("%s: HELLO_CLIENT_AUTH_KEY comes from %s/%s", who.name, n, k)
		}
	}
	if str(envOf(ctl)["HELLO_DOOR_CAMERA_ALLOW"]["value"]) == "" {
		t.Error("hello-control has no HELLO_DOOR_CAMERA_ALLOW")
	}
}

// TestKwFax fails if hello-fax is exposed outside the cluster, is not given
// the pod IP as its media host, or if hello-sip and hello-control are not told
// its Service address with the token from the hello-fax Secret.
func TestKwFax(t *testing.T) {
	objs := kwFile(t, "fax.yaml")
	d := findObj(t, objs, "Deployment", "hello-fax")
	c := containerNamed(t, d, false, "hello-fax")
	if str(dig(envOf(c)["HELLO_FAX_MEDIA_HOST"], "valueFrom", "fieldRef", "fieldPath")) != "status.podIP" {
		t.Error("HELLO_FAX_MEDIA_HOST is not the pod IP")
	}
	if n, k := secretRef(c, "HELLO_FAX_TOKEN"); n != "hello-fax" || k != "faxToken" {
		t.Errorf("hello-fax token comes from %s/%s", n, k)
	}
	if str(dig(findObj(t, objs, "Service", "hello-fax"), "spec", "type")) != "ClusterIP" {
		t.Error("the hello-fax Service is not ClusterIP")
	}
	for _, who := range []string{"hello-sip-1", "hello-sip-2", "hello-control"} {
		cn := who
		if strings.HasPrefix(who, "hello-sip") {
			cn = "hello-sip"
		}
		e := containerNamed(t, kwObject(t, "Deployment", who), false, cn)
		env := envOf(e)
		if str(env["HELLO_FAX_ENABLED"]["value"]) != "true" || str(env["HELLO_FAX_ADDR"]["value"]) != "hello-fax.hello.svc.cluster.local:8083" {
			t.Errorf("%s: fax env %v %v", who, env["HELLO_FAX_ENABLED"], env["HELLO_FAX_ADDR"])
		}
		if n, k := secretRef(e, "HELLO_FAX_TOKEN"); n != "hello-fax" || k != "faxToken" {
			t.Errorf("%s: HELLO_FAX_TOKEN comes from %s/%s", who, n, k)
		}
	}
}

func findAll(objs []object, kind, name string) []object {
	var out []object
	for _, o := range objs {
		if str(o["kind"]) == kind && str(dig(o, "metadata", "name")) == name {
			out = append(out, o)
		}
	}
	return out
}
