package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChartKamailioTLSConfigMatchesSource fails if the chart's copy of
// tls.cfg, or the kw ConfigMap's, differs from deploy/kamailio/tls.cfg, or
// if the source drops TLS 1.2 as the minimum (phone-feature-parity S-14).
func TestChartKamailioTLSConfigMatchesSource(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "tls.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "method = TLSv1.2+") {
		t.Error("tls.cfg must set method = TLSv1.2+ (TLS 1.2 minimum, 1.3 preferred)")
	}
	for _, bad := range []string{"SSLv", "TLSv1.0", "TLSv1.1", "TLSv1 ", "TLSv1\n", "TLSv1+"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("tls.cfg allows a protocol below TLS 1.2 (%q)", bad)
		}
	}
	copyOf, err := os.ReadFile(filepath.Join(chartDir(t), "files", "tls.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, copyOf) {
		t.Fatal("deploy/helm/hello/files/tls.cfg differs from deploy/kamailio/tls.cfg; copy the source over it")
	}
	if cm := findObj(t, evalRender(t), "ConfigMap", "hello-kamailio"); str(dig(cm, "data", "tls.cfg")) != string(src) {
		t.Error("the rendered hello-kamailio ConfigMap's tls.cfg differs from the source")
	}
	kw := find(t, kwManifests(t), "ConfigMap", "hello-kamailio")
	if kw.Data["tls.cfg"] != string(src) {
		t.Error("the kw hello-kamailio ConfigMap's tls.cfg differs from deploy/kamailio/tls.cfg")
	}
}

// TestKamailioTLSIsOptIn fails if the Kamailio config loads the tls module
// or binds a TLS socket without KAMAILIO_TLS_PORT (an image without the
// module would then not start), if the phone transport header is not set
// from the receiving socket and stripped from the phone's own request, or
// if a TLS destination is not sent from the TLS socket.
func TestKamailioTLSIsOptIn(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "kamailio.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	for _, want := range []string{"loadmodule \"tls.so\"", "listen=tls:", "enable_tls=yes", "modparam(\"tls\", \"config\""} {
		i := strings.Index(cfg, want)
		if i < 0 {
			t.Fatalf("kamailio.cfg has no %q", want)
		}
		before := cfg[:i]
		open := strings.LastIndex(before, "#!ifdef TLS_PORT")
		closed := strings.LastIndex(before, "#!endif")
		if open < 0 || closed > open {
			t.Errorf("%q is not inside #!ifdef TLS_PORT", want)
		}
	}
	if !strings.Contains(cfg, `remove_hf("X-Hello-Transport")`) {
		t.Error("route[FROM_PHONE] does not strip a phone-supplied X-Hello-Transport")
	}
	if body, ok := kamailioRoute(cfg, "TO_HELLO"); !ok || !strings.Contains(body, "X-Hello-Transport: $pr") {
		t.Error("route[TO_HELLO] does not tell Hello the phone's transport ($pr) in X-Hello-Transport")
	}
	if body, ok := kamailioRoute(cfg, "PHONES_SOCKET"); !ok || !strings.Contains(body, `$dP == "tls"`) || !strings.Contains(body, "phones_tls") {
		t.Error("route[PHONES_SOCKET] does not send a TLS destination from the phones_tls socket")
	}
	// The tls.cfg paths are the chart's and the lab's mount point.
	tls, _ := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "kamailio", "tls.cfg"))
	if !strings.Contains(string(tls), "/etc/hello-tls/tls.crt") || !strings.Contains(string(tls), "/etc/hello-tls/tls.key") {
		t.Error("tls.cfg does not read /etc/hello-tls/tls.crt and tls.key")
	}
}

// TestChartKamailioTLS fails if the chart enables TLS by default, renders
// TLS without a certificate Secret, or does not wire the port, the Secret
// and the environment when enabled.
func TestChartKamailioTLS(t *testing.T) {
	has := func(c object, name string) bool {
		for _, e := range list(dig(c, "env")) {
			if str(dig(e, "name")) == name {
				return true
			}
		}
		return false
	}
	off := containerNamed(t, findObj(t, evalRender(t), "Deployment", "hello-kamailio"), false, "kamailio")
	if has(off, "KAMAILIO_TLS_PORT") {
		t.Error("TLS is on by default; it must be opt-in (an image without the tls module would not start)")
	}

	_, err := helmTemplate(t, "-f", valuesFile(t, "values-eval.yaml"), "--set", "kamailio.tls.enabled=true")
	if err == nil || !strings.Contains(err.Error(), "kamailio.tls.secretName is required") {
		t.Errorf("TLS without a Secret: want a render failure naming kamailio.tls.secretName, got %v", err)
	}

	objs := render(t, "-f", valuesFile(t, "values-eval.yaml"), "--set", "kamailio.tls.enabled=true", "--set", "kamailio.tls.secretName=hello-sip-tls",
		"--set", "kamailio.hostNetwork=false", "--set", "kamailio.service.type=NodePort")
	dep := findObj(t, objs, "Deployment", "hello-kamailio")
	c := containerNamed(t, dep, false, "kamailio")
	if !has(c, "KAMAILIO_TLS_PORT") || !has(c, "KAMAILIO_PUBLIC_TLS_PORT") {
		t.Error("KAMAILIO_TLS_PORT and KAMAILIO_PUBLIC_TLS_PORT are not set when TLS is enabled")
	}
	mounted := false
	for _, m := range list(dig(c, "volumeMounts")) {
		if str(dig(m, "mountPath")) == "/etc/hello-tls" && str(dig(m, "readOnly")) == "true" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("the certificate Secret is not mounted read-only at /etc/hello-tls")
	}
	secret := false
	for _, v := range list(dig(podSpec(dep), "volumes")) {
		if str(dig(v, "secret", "secretName")) == "hello-sip-tls" {
			secret = true
		}
	}
	if !secret {
		t.Error("the pod does not mount Secret hello-sip-tls")
	}
	svc := findObj(t, objs, "Service", "hello-kamailio-phones")
	tcp := false
	for _, p := range list(dig(svc, "spec", "ports")) {
		if str(dig(p, "name")) == "phones-tls" && str(dig(p, "protocol")) == "TCP" && str(dig(p, "port")) == "5061" {
			tcp = true
		}
	}
	if !tcp {
		t.Error("the phones Service does not publish TCP 5061 (phones-tls)")
	}
}

// TestChartProvisioningTLSServer fails if provisioned phones are pointed at
// the TLS registrar without kamailio.tls.enabled, or are not once it is.
func TestChartProvisioningTLSServer(t *testing.T) {
	env := func(objs []object) map[string]string {
		out := map[string]string{}
		for _, d := range objs {
			if str(d["kind"]) != "Deployment" || !strings.Contains(str(dig(d, "metadata", "name")), "control") {
				continue
			}
			for _, c := range list(dig(podSpec(d), "containers")) {
				for _, e := range list(dig(c, "env")) {
					out[str(dig(e, "name"))] = str(dig(e, "value"))
				}
			}
		}
		return out
	}
	base := []string{"-f", valuesFile(t, "values-eval.yaml"), "--set", "control.provisioning.publicUrl=https://p.example.com"}
	if _, ok := env(render(t, base...))["HELLO_PROV_SIP_TLS_SERVER"]; ok {
		t.Error("HELLO_PROV_SIP_TLS_SERVER is set although kamailio.tls is off")
	}
	on := env(render(t, append(base, "--set", "kamailio.tls.enabled=true", "--set", "kamailio.tls.secretName=s", "--set", "kamailio.publicHost=192.0.2.9")...))
	if got := on["HELLO_PROV_SIP_TLS_SERVER"]; got != "192.0.2.9:5061" {
		t.Errorf("HELLO_PROV_SIP_TLS_SERVER = %q, want 192.0.2.9:5061", got)
	}
}
