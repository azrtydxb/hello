package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepo(t *testing.T, rel ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestKamailioTLSHopIsOptIn guards the TLS hop from Kamailio to hello-sip
// (clients-webrtc-mobile-video S-12): everything it adds sits under
// #!ifdef HELLO_TLS_PORT (kw does not set KAMAILIO_HELLO_TLS_PORT, so its
// Kamailio keeps routing exactly as before), the hop socket is vetted like the
// UDP hello socket, and only an encrypted phone leg is sent over it.
func TestKamailioTLSHopIsOptIn(t *testing.T) {
	cfg := readRepo(t, "deploy", "kamailio", "kamailio.cfg")
	for _, want := range []string{
		`listen=tls:LISTEN_IP:HELLO_TLS_PORT name "hello_tls"`,
		`modparam("tls", "config", "/etc/kamailio/tls-hop.cfg")`,
		"setflag(FLT_HOP_TLS);",
		"route[HELLO_TLS_DST] {",
		`$fsn = "hello_tls";`,
		`if ($pr == "tls" || $pr == "wss" || $pr == "ws") {`,
		"$var(hp) = HELLO_SIP_TLS_PORT;",
	} {
		i := strings.Index(cfg, want)
		if i < 0 {
			t.Errorf("kamailio.cfg has no %q", want)
			continue
		}
		before := cfg[:i]
		if open, closed := strings.LastIndex(before, "#!ifdef HELLO_TLS_PORT"), strings.LastIndex(before, "#!endif"); open < 0 || closed > open {
			t.Errorf("%q is not inside #!ifdef HELLO_TLS_PORT", want)
		}
	}
	// Every use of the flag and of the destination route is guarded too.
	for _, want := range []string{"route(HELLO_TLS_DST);", "route(PICK_SET);", "route(PICK_SET_FAIL);", "isflagset(FLT_HOP_TLS)", "$var(sec) == 1"} {
		for i, off := 0, 0; ; off = i + len(want) {
			j := strings.Index(cfg[off:], want)
			if j < 0 {
				break
			}
			i = off + j
			before := cfg[:i]
			if open, closed := strings.LastIndex(before, "#!ifdef HELLO_TLS_PORT"), strings.LastIndex(before, "#!endif"); open < 0 || closed > open {
				t.Errorf("%q at offset %d is not inside #!ifdef HELLO_TLS_PORT", want, i)
			}
		}
	}
	if !strings.Contains(cfg, "$Rn == \"hello_tls\") {\n\t\t# Received on the Hello-facing socket") {
		t.Error("request_route does not vet what arrives on hello_tls like the hello socket")
	}
	if body, _ := kamailioRoute(cfg, "EDGE_SIDE"); strings.Count(body, "$var(sec) = 0;") != 1 {
		t.Error("route[EDGE_SIDE] does not start $var(sec) at 0")
	}
	// The dispatcher's probes stay on the UDP socket.
	if !strings.Contains(cfg, `modparam("dispatcher", "ds_default_sockname", "hello")`) {
		t.Error("the dispatcher no longer probes from the UDP hello socket")
	}
	// Off, the dispatcher set stays the literal 1 everywhere it was.
	if !strings.Contains(cfg, "#!else\n#!define HELLO_SET \"1\"") || strings.Count(cfg, "ds_select_dst(HELLO_SET,") != 4 || strings.Contains(cfg, `ds_select_dst("1"`) {
		t.Error("ds_select_dst must take HELLO_SET (1 without the hop, the PICK_SET result with it) at its four places")
	}
	// An encrypted leg is balanced over the nodes' TLS addresses (set 2),
	// which are probed over TLS, so a node whose TLS listener is down is skipped.
	if body, _ := kamailioRoute(cfg, "PICK_SET"); !strings.Contains(body, "$var(dset) = 2;") {
		t.Error("route[PICK_SET] does not pick dispatcher set 2 for an encrypted leg")
	}
	if body, _ := kamailioRoute(cfg, "PICK_SET_FAIL"); !strings.Contains(body, "isflagset(FLT_HOP_TLS)") {
		t.Error("route[PICK_SET_FAIL] does not follow the message flag in a failure route")
	}
	if body, _ := kamailioRoute(cfg, "TO_HELLO"); !strings.Contains(body, "setflag(FLT_HOP_TLS)") {
		t.Error("route[TO_HELLO] does not send an encrypted phone leg over the TLS hop")
	}
}

// TestChartKamailioTLSHopConfigMatchesSource fails if the chart's copy of
// tls-hop.cfg, or the kw ConfigMap's, differs from deploy/kamailio/tls-hop.cfg,
// or if the hop's client side stops verifying Hello's certificate.
func TestChartKamailioTLSHopConfigMatchesSource(t *testing.T) {
	src := readRepo(t, "deploy", "kamailio", "tls-hop.cfg")
	for _, want := range []string{"method = TLSv1.2+", "ca_list = /etc/hello-tls/ca.crt"} {
		if strings.Count(src, want) < 2 {
			t.Errorf("tls-hop.cfg: want %q in both the server and the client section", want)
		}
	}
	client := src[strings.Index(src, "[client:default]"):]
	if !strings.Contains(client, "verify_certificate = yes") || !strings.Contains(client, "certificate = /etc/hello-tls/tls.crt") {
		t.Error("tls-hop.cfg: the client section must verify the node's certificate and present Kamailio's own")
	}
	for _, bad := range []string{"SSLv", "TLSv1.0", "TLSv1.1"} {
		if strings.Contains(src, bad) {
			t.Errorf("tls-hop.cfg allows a protocol below TLS 1.2 (%q)", bad)
		}
	}
	copyOf, err := os.ReadFile(filepath.Join(chartDir(t), "files", "tls-hop.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(src), copyOf) {
		t.Fatal("deploy/helm/hello/files/tls-hop.cfg differs from deploy/kamailio/tls-hop.cfg; copy the source over it")
	}
	if cm := findObj(t, evalRender(t), "ConfigMap", "hello-kamailio"); str(dig(cm, "data", "tls-hop.cfg")) != src {
		t.Error("the rendered hello-kamailio ConfigMap's tls-hop.cfg differs from the source")
	}
	if kw := find(t, kwManifests(t), "ConfigMap", "hello-kamailio"); kw.Data["tls-hop.cfg"] != src {
		t.Error("the kw hello-kamailio ConfigMap's tls-hop.cfg differs from deploy/kamailio/tls-hop.cfg")
	}
}

// TestChartTLSHop fails if the chart turns the hop on by default, renders it
// without its prerequisites, or does not wire both ends when enabled.
func TestChartTLSHop(t *testing.T) {
	envNames := func(c object) map[string]object { return envOf(c) }
	off := evalRender(t)
	if _, ok := envNames(containerNamed(t, findObj(t, off, "Deployment", "hello-kamailio"), false, "kamailio"))["KAMAILIO_HELLO_TLS_PORT"]; ok {
		t.Error("the TLS hop is on by default")
	}
	sipOff := containerNamed(t, findObj(t, off, "StatefulSet", "hello-sip"), false, "hello-sip")
	if _, ok := envNames(sipOff)["HELLO_SIP_TLS_ADDR"]; ok {
		t.Error("hello-sip takes TLS by default")
	}

	eval := valuesFile(t, "values-eval.yaml")
	if _, err := helmTemplate(t, "-f", eval, "--set", "kamailio.helloTLS.enabled=true"); err == nil || !strings.Contains(err.Error(), "sip.tls.enabled") {
		t.Errorf("the hop without sip.tls: want a failure naming sip.tls.enabled, got %v", err)
	}
	if _, err := helmTemplate(t, "-f", eval, "--set", "kamailio.helloTLS.enabled=true", "--set", "sip.tls.enabled=true", "--set", "sip.tls.secretName=s"); err == nil || !strings.Contains(err.Error(), "kamailio.tls.secretName") {
		t.Errorf("the hop without Kamailio's certificate: want a failure naming kamailio.tls.secretName, got %v", err)
	}
	if _, err := helmTemplate(t, "-f", eval, "--set", "sip.tls.enabled=true"); err == nil || !strings.Contains(err.Error(), "sip.tls.secretName") {
		t.Errorf("sip.tls without a Secret: want a failure naming sip.tls.secretName, got %v", err)
	}

	// The TLS set of the dispatcher list exists only with the hop.
	if cm := findObj(t, off, "ConfigMap", "hello-kamailio"); strings.Contains(str(dig(cm, "data", "dispatcher.list")), "\n2 sip:") {
		t.Error("the default dispatcher list has a TLS set")
	}
	objs := render(t, "-f", eval, "--set", "kamailio.helloTLS.enabled=true", "--set", "sip.tls.enabled=true", "--set", "sip.tls.secretName=hello-sip-tls",
		"--set", "kamailio.tls.secretName=hello-kamailio-tls")
	if cm := findObj(t, objs, "ConfigMap", "hello-kamailio"); strings.Count(str(dig(cm, "data", "dispatcher.list")), ":5061;transport=tls 0 0 duid=") != 2 {
		t.Errorf("the dispatcher list does not hold the two nodes' TLS addresses as set 2:\n%s", str(dig(cm, "data", "dispatcher.list")))
	}
	kam := envNames(containerNamed(t, findObj(t, objs, "Deployment", "hello-kamailio"), false, "kamailio"))
	if str(kam["KAMAILIO_HELLO_TLS_PORT"]["value"]) != "5073" || str(kam["KAMAILIO_HELLO_SIP_TLS_PORT"]["value"]) != "5061" {
		t.Errorf("kamailio env = %v", kam)
	}
	sip := containerNamed(t, findObj(t, objs, "StatefulSet", "hello-sip"), false, "hello-sip")
	se := envNames(sip)
	for k, want := range map[string]string{"HELLO_SIP_TLS_ADDR": ":5061", "HELLO_SIP_TLS_CERT": "/etc/hello-sip-tls/tls.crt", "HELLO_SIP_TLS_KEY": "/etc/hello-sip-tls/tls.key", "HELLO_SIP_TLS_CA": "/etc/hello-sip-tls/ca.crt"} {
		if got := str(se[k]["value"]); got != want {
			t.Errorf("hello-sip %s = %q, want %q", k, got, want)
		}
	}
	mounted := false
	for _, m := range list(dig(sip, "volumeMounts")) {
		if str(dig(m, "mountPath")) == "/etc/hello-sip-tls" && str(dig(m, "readOnly")) == "true" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("hello-sip's certificate Secret is not mounted read-only")
	}
}

// TestLabTLSHop fails if the lab does not turn the hop on at both ends.
func TestLabTLSHop(t *testing.T) {
	c := readRepo(t, "deploy", "docker-compose", "compose.yaml")
	for _, want := range []string{"KAMAILIO_HELLO_TLS_PORT:", "HELLO_SIP_TLS_ADDR:", "HELLO_SIP_TLS_CA:", "clientAuth"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose.yaml does not contain %q", want)
		}
	}
	if l := readRepo(t, "deploy", "kamailio", "dispatcher.list"); strings.Count(l, "\n2 sip:") != 2 {
		t.Error("the lab dispatcher.list does not hold the two nodes' TLS addresses as set 2")
	}
}
