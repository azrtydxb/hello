package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The coturn invariants of clients-webrtc-mobile-video S-21 to S-23, checked
// on the rendered chart and on the entrypoint that writes coturn's
// configuration. Nothing is started.

var coturnSet = []string{
	"--set", "coturn.enabled=true",
	"--set", "coturn.allowedPeers={192.168.10.11,192.168.10.12-192.168.10.13}",
	"--set", "coturn.servers[0].name=1", "--set", "coturn.servers[0].externalIP=192.168.10.121",
	"--set", "coturn.servers[1].name=2", "--set", "coturn.servers[1].externalIP=192.168.10.122",
}

// TestChartCoturnOffByDefault fails if the default render carries a coturn
// object or a TURN setting, so an install that does not want TURN gets none.
func TestChartCoturnOffByDefault(t *testing.T) {
	for _, o := range evalRender(t) {
		if strings.Contains(str(dig(o, "metadata", "name")), "coturn") {
			t.Errorf("default render has %s/%s", o["kind"], dig(o, "metadata", "name"))
		}
	}
	env := envOf(containerNamed(t, findObj(t, evalRender(t), "Deployment", "hello-control"), false, "hello-control"))
	for _, name := range []string{"HELLO_TURN_URLS", "HELLO_TURN_SECRET"} {
		if _, ok := env[name]; ok {
			t.Errorf("hello-control sets %s with coturn off", name)
		}
	}
}

// TestChartCoturn fails if coturn is not one single-replica Deployment per
// address (a TURN allocation lives on one server, so replicas behind one
// address would break relayed media), is not replaced old-first, takes its
// shared secret from anywhere but the credentials Secret, lets clients relay
// to any address but the named peers, or does not hand hello-control the
// matching TURN URIs and secret.
func TestChartCoturn(t *testing.T) {
	objs := haRender(t, coturnSet...)
	var deployments []object
	for _, o := range objs {
		if str(o["kind"]) == "Deployment" && strings.Contains(str(dig(o, "metadata", "name")), "-coturn-") {
			deployments = append(deployments, o)
		}
	}
	if len(deployments) != 2 {
		t.Fatalf("coturn Deployments = %d, want one per server (2)", len(deployments))
	}
	addrs := map[string]bool{}
	for _, d := range deployments {
		name := str(dig(d, "metadata", "name"))
		if intOf(t, dig(d, "spec", "replicas")) != 1 {
			t.Errorf("%s: replicas = %v, want 1", name, dig(d, "spec", "replicas"))
		}
		if str(dig(d, "spec", "strategy", "type")) != "Recreate" {
			t.Errorf("%s: strategy %v, want Recreate", name, dig(d, "spec", "strategy", "type"))
		}
		if str(dig(podSpec(d), "hostNetwork")) != "true" {
			t.Errorf("%s: not on the host network; relayed media would leave from a pod address", name)
		}
		c := containerNamed(t, d, false, "coturn")
		env := envOf(c)
		if got := str(dig(env["TURN_SECRET"], "valueFrom", "secretKeyRef", "key")); got != "turnSecret" {
			t.Errorf("%s: TURN_SECRET comes from key %q, want turnSecret of the credentials Secret", name, got)
		}
		if got := str(dig(env["TURN_SECRET"], "valueFrom", "secretKeyRef", "name")); got != "hello-credentials" {
			t.Errorf("%s: TURN_SECRET from Secret %q, want hello-credentials", name, got)
		}
		if env["TURN_SECRET"]["value"] != nil {
			t.Errorf("%s: TURN_SECRET is a literal", name)
		}
		if got := str(env["TURN_ALLOWED_PEERS"]["value"]); got != "192.168.10.11,192.168.10.12-192.168.10.13" {
			t.Errorf("%s: TURN_ALLOWED_PEERS = %q, want exactly the configured peers", name, got)
		}
		if got := str(env["TURN_REALM"]["value"]); got != "hello.example.com" {
			t.Errorf("%s: realm = %q, want the SIP domain", name, got)
		}
		addrs[str(env["TURN_EXTERNAL_IP"]["value"])] = true
		if str(dig(c, "securityContext", "readOnlyRootFilesystem")) != "true" || str(dig(c, "securityContext", "allowPrivilegeEscalation")) != "false" {
			t.Errorf("%s: container is not locked down", name)
		}
		if str(dig(podSpec(d), "securityContext", "runAsNonRoot")) != "true" {
			t.Errorf("%s: runs as root", name)
		}
	}
	if !addrs["192.168.10.121"] || !addrs["192.168.10.122"] {
		t.Errorf("external addresses = %v, want both servers", addrs)
	}

	control := envOf(containerNamed(t, findObj(t, objs, "Deployment", "hello-control"), false, "hello-control"))
	urls := str(control["HELLO_TURN_URLS"]["value"])
	for _, want := range []string{"turn:192.168.10.121:3478?transport=udp", "turn:192.168.10.121:3478?transport=tcp", "turn:192.168.10.122:3478?transport=udp"} {
		if !strings.Contains(urls, want) {
			t.Errorf("HELLO_TURN_URLS = %q lacks %s", urls, want)
		}
	}
	if strings.Contains(urls, "turns:") {
		t.Errorf("HELLO_TURN_URLS offers turns: with TLS off: %q", urls)
	}
	if got := str(dig(control["HELLO_TURN_SECRET"], "valueFrom", "secretKeyRef", "key")); got != "turnSecret" {
		t.Errorf("hello-control HELLO_TURN_SECRET from key %q, want turnSecret (the same value coturn holds)", got)
	}
	// The secret is required, not optional: a missing key must stop the pod
	// rather than start control with TURN URIs it cannot sign for.
	if dig(control["HELLO_TURN_SECRET"], "valueFrom", "secretKeyRef", "optional") == true {
		t.Error("HELLO_TURN_SECRET is optional with coturn on")
	}
}

// TestChartClientCredentialsEnv fails if hello-control and hello-sip do not
// both get the web device key (and its previous value) from the credentials
// Secret, as optional references: web devices are off until the key exists.
func TestChartClientCredentialsEnv(t *testing.T) {
	objs := haRender(t, "--set", "sip.maxContacts=12", "--set", "clients.maxDevices=3", "--set", "clients.wssUrl=wss://hello.example.com/ws")
	for name, c := range map[string]object{
		"hello-control": containerNamed(t, findObj(t, objs, "Deployment", "hello-control"), false, "hello-control"),
		"hello-sip":     containerNamed(t, findObj(t, objs, "StatefulSet", "hello-sip"), false, "hello-sip"),
	} {
		env := envOf(c)
		for envName, key := range map[string]string{"HELLO_CLIENT_AUTH_KEY": "clientAuthKey", "HELLO_CLIENT_AUTH_KEY_PREVIOUS": "clientAuthKeyPrevious"} {
			ref := dig(env[envName], "valueFrom", "secretKeyRef")
			if ref == nil || str(dig(ref, "key")) != key {
				t.Errorf("%s: %s = %v, want a reference to %s", name, envName, env[envName], key)
			}
			if dig(ref, "optional") != true {
				t.Errorf("%s: %s must be optional (web devices stay off without a key)", name, envName)
			}
		}
	}
	sip := envOf(containerNamed(t, findObj(t, objs, "StatefulSet", "hello-sip"), false, "hello-sip"))
	if str(sip["HELLO_SIP_MAX_CONTACTS"]["value"]) != "12" {
		t.Errorf("HELLO_SIP_MAX_CONTACTS = %v", sip["HELLO_SIP_MAX_CONTACTS"])
	}
	control := envOf(containerNamed(t, findObj(t, objs, "Deployment", "hello-control"), false, "hello-control"))
	if str(control["HELLO_CLIENT_MAX_DEVICES"]["value"]) != "3" || str(control["HELLO_WSS_URL"]["value"]) != "wss://hello.example.com/ws" {
		t.Errorf("control env = %v %v", control["HELLO_CLIENT_MAX_DEVICES"], control["HELLO_WSS_URL"])
	}
}

// TestChartCoturnRefusals fails if the chart renders a coturn that is open
// (no peers named), has no server, a malformed address or peer, or TLS
// without a certificate.
func TestChartCoturnRefusals(t *testing.T) {
	good := coturnSet
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no servers": {[]string{"--set", "coturn.enabled=true", "--set", "coturn.allowedPeers={10.0.0.1}"}, "coturn.servers is required"},
		"no peers": {[]string{"--set", "coturn.enabled=true", "--set", "coturn.servers[0].name=1", "--set", "coturn.servers[0].externalIP=192.168.10.121"},
			"coturn.allowedPeers is required"},
		"a CIDR peer":      {append(append([]string{}, good...), "--set", "coturn.allowedPeers={10.0.0.0/8}"), "must be an IPv4 address or an a-b range"},
		"bad address":      {append(append([]string{}, good...), "--set", "coturn.servers[0].externalIP=turn.example.com"), "externalIP must be an IPv4 address"},
		"tls no cert":      {append(append([]string{}, good...), "--set", "coturn.tls.enabled=true"), "coturn.tls.secretName is required"},
		"huge relay range": {append(append([]string{}, good...), "--set", "coturn.hostNetwork=false"), "at most 200"},
	} {
		_, err := helmTemplate(t, append([]string{"-f", valuesFile(t, "values-ha.yaml")}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}

// TestChartCoturnService fails if, without hostNetwork, each
// server is not its own LoadBalancer Service owning its address and listing
// the relay ports, so a relay's source address is the one clients were told.
func TestChartCoturnService(t *testing.T) {
	objs := haRender(t, append(append([]string{}, coturnSet...), "--set", "coturn.hostNetwork=false", "--set", "coturn.relayPortMax=49160", "--set", "coturn.tls.enabled=true", "--set", "coturn.tls.secretName=coturn-tls")...)
	for i, ip := range []string{"192.168.10.121", "192.168.10.122"} {
		svc := findObj(t, objs, "Service", "hello-coturn-"+string(rune('1'+i)))
		if str(dig(svc, "spec", "type")) != "LoadBalancer" || str(dig(svc, "spec", "loadBalancerIP")) != ip {
			t.Errorf("service %d: type %v ip %v, want a LoadBalancer on %s", i+1, dig(svc, "spec", "type"), dig(svc, "spec", "loadBalancerIP"), ip)
		}
		ports := map[string]bool{}
		for _, p := range list(dig(svc, "spec", "ports")) {
			ports[str(dig(p, "protocol"))+"/"+str(dig(p, "port"))] = true
		}
		for _, want := range []string{"UDP/3478", "TCP/3478", "TCP/5349", "UDP/49152", "UDP/49160"} {
			if !ports[want] {
				t.Errorf("service %d lacks port %s", i+1, want)
			}
		}
		if ports["UDP/49161"] {
			t.Errorf("service %d exposes a relay port above the range", i+1)
		}
	}
}

// TestCoturnEntrypointConfig runs docker/coturn/entrypoint.sh with the
// binary replaced and fails if the configuration it writes is not the TURN
// REST API mode with the shared secret, does not deny every private range
// while allowing only the named peers, lets a relay go over TCP, or lets the
// refusal of a missing setting pass.
func TestCoturnEntrypointConfig(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	script := filepath.Join(repoRoot(t), "docker", "coturn", "entrypoint.sh")
	run := func(extra ...string) (string, error) {
		conf := filepath.Join(t.TempDir(), "turnserver.conf")
		cmd := exec.Command(sh, script)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "TURN_CONF=" + conf, "TURN_BIN=true"}, extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), err
		}
		b, err := os.ReadFile(conf)
		return string(b), err
	}
	base := []string{"TURN_SECRET=test-shared-secret", "TURN_REALM=hello.example.com", "TURN_EXTERNAL_IP=192.168.10.121",
		"TURN_ALLOWED_PEERS=192.168.10.11,192.168.10.12-192.168.10.13"}
	conf, err := run(base...)
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, conf)
	}
	has := func(c, line string) bool {
		for _, l := range strings.Split(c, "\n") {
			if l == line {
				return true
			}
		}
		return false
	}
	for _, line := range []string{"use-auth-secret", "static-auth-secret=test-shared-secret", "realm=hello.example.com", "external-ip=192.168.10.121",
		"fingerprint", "no-tcp-relay", "no-multicast-peers", "no-cli", "min-port=49152", "max-port=49999", "listening-port=3478",
		"allowed-peer-ip=192.168.10.11", "allowed-peer-ip=192.168.10.12-192.168.10.13", "user-quota=12", "total-quota=1200", "no-tls"} {
		if !has(conf, line) {
			t.Errorf("config lacks %q", line)
		}
	}
	for _, r := range []string{"10.0.0.0-10.255.255.255", "172.16.0.0-172.31.255.255", "192.168.0.0-192.168.255.255", "127.0.0.0-127.255.255.255", "169.254.0.0-169.254.255.255", "::1"} {
		if !has(conf, "denied-peer-ip="+r) {
			t.Errorf("config does not deny %s", r)
		}
	}
	// Only the named peers are allowed; nothing is allowed by default.
	allowed := regexp.MustCompile(`(?m)^allowed-peer-ip=.*$`).FindAllString(conf, -1)
	if len(allowed) != 2 {
		t.Errorf("allowed-peer-ip lines = %v, want exactly the two peers", allowed)
	}
	if strings.Contains(conf, "no-auth") || strings.Contains(conf, "lt-cred-mech") || strings.Contains(conf, "user=") {
		t.Errorf("config enables another authentication mode:\n%s", conf)
	}
	// The file holds the secret: it must be owner-only.
	kept := filepath.Join(t.TempDir(), "kept.conf")
	cmd := exec.Command(sh, script)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "TURN_CONF=" + kept, "TURN_BIN=true"}, base...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if fi, err := os.Stat(kept); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("config file mode = %v (%v), want owner only", fi, err)
	}

	tlsConf, err := run(append(base, "TURN_TLS_CERT=/c/tls.crt", "TURN_TLS_KEY=/c/tls.key", "TURN_PROMETHEUS=true")...)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"tls-listening-port=5349", "cert=/c/tls.crt", "pkey=/c/tls.key", "no-tlsv1", "no-tlsv1_1", "prometheus"} {
		if !has(tlsConf, line) {
			t.Errorf("TLS config lacks %q", line)
		}
	}
	if has(tlsConf, "no-tls") {
		t.Error("TLS config still says no-tls")
	}

	for _, missing := range []string{"TURN_SECRET", "TURN_REALM", "TURN_EXTERNAL_IP"} {
		var env []string
		for _, e := range base {
			if !strings.HasPrefix(e, missing+"=") {
				env = append(env, e)
			}
		}
		if out, err := run(env...); err == nil {
			t.Errorf("entrypoint started without %s: %s", missing, out)
		}
	}
}
