package deploy

import (
	"slices"
	"strings"
	"testing"
)

// TestKwBrowsers fails if kw stops serving browsers the way docs/webrtc.md
// describes: Kamailio's WSS listener behind the console's Ingress at /sip,
// the TLS hop to both hello-sip nodes with a dispatcher set 2 entry for every
// set 1 node, certificates of one CA usable as server and client certificates
// that name what each side dials, hello-sip able to read its key, WebRTC media
// on without the cleartext-hop escape, and the softphone told the WSS address.
func TestKwBrowsers(t *testing.T) {
	objs := decodeAll(t, kwRaw(t, false))
	kamD := findObj(t, objs, "Deployment", "kamailio")
	kam := containerNamed(t, kamD, false, "kamailio")
	ke := envOf(kam)
	for k, want := range map[string]string{
		"KAMAILIO_WSS_PORT":           "8088",
		"KAMAILIO_HELLO_TLS_PORT":     "5073",
		"KAMAILIO_HELLO_SIP_TLS_PORT": "5061",
		"KAMAILIO_WS_ORIGINS":         "https://hello.kw.watteel.lab",
	} {
		if got := str(ke[k]["value"]); got != want {
			t.Errorf("kamailio %s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(str(ke["KAMAILIO_WSS_TRUSTED_PROXIES"]["value"]), "^10[.]42[.]") {
		t.Errorf("KAMAILIO_WSS_TRUSTED_PROXIES = %q, want kw's pod CIDR (ingress-nginx)", ke["KAMAILIO_WSS_TRUSTED_PROXIES"]["value"])
	}

	// One Secret on every TLS socket of Kamailio, waited for (not optional).
	var edgeSecret string
	for _, v := range list(dig(podSpec(kamD), "volumes")) {
		if str(dig(v, "name")) == "tls" {
			edgeSecret = str(dig(v, "secret", "secretName"))
			if dig(v, "secret", "optional") == true {
				t.Error("Kamailio's certificate volume is optional: the pod could start before cert-manager issued it")
			}
		}
	}
	nodeSecret := ""
	for _, n := range []string{"hello-sip-1", "hello-sip-2"} {
		d := findObj(t, objs, "Deployment", n)
		se := envOf(containerNamed(t, d, false, "hello-sip"))
		for k, want := range map[string]string{
			"HELLO_SIP_TLS_ADDR": ":5061", "HELLO_SIP_TLS_CERT": "/etc/hello-sip-tls/tls.crt",
			"HELLO_SIP_TLS_KEY": "/etc/hello-sip-tls/tls.key", "HELLO_SIP_TLS_CA": "/etc/hello-sip-tls/ca.crt",
			"HELLO_MEDIA_WEBRTC_ENABLED": "true", "HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP": "false",
		} {
			if got := str(se[k]["value"]); got != want {
				t.Errorf("%s %s = %q, want %q", n, k, got, want)
			}
		}
		if got := str(dig(podSpec(d), "securityContext", "fsGroup")); got != "65532" {
			t.Errorf("%s fsGroup = %q: the image's user cannot read the 0440 key", n, got)
		}
		for _, v := range list(dig(podSpec(d), "volumes")) {
			if str(dig(v, "name")) == "sip-tls" {
				if nodeSecret != "" && nodeSecret != str(dig(v, "secret", "secretName")) {
					t.Errorf("%s mounts %v, the other node %s", n, dig(v, "secret", "secretName"), nodeSecret)
				}
				nodeSecret = str(dig(v, "secret", "secretName"))
			}
		}
	}

	certFor := func(secret string) object {
		for _, o := range objs {
			if str(o["kind"]) == "Certificate" && str(dig(o, "spec", "secretName")) == secret {
				return o
			}
		}
		t.Fatalf("no Certificate issues Secret %q", secret)
		return nil
	}
	strs := func(v any) []string {
		var out []string
		for _, s := range list(v) {
			out = append(out, str(s))
		}
		return out
	}
	edge, node := certFor(edgeSecret), certFor(nodeSecret)
	if str(dig(edge, "spec", "issuerRef", "name")) != str(dig(node, "spec", "issuerRef", "name")) {
		t.Error("Kamailio's and hello-sip's certificates come from different issuers: neither would verify the other")
	}
	for _, c := range []object{edge, node} {
		u := strs(dig(c, "spec", "usages"))
		if !slices.Contains(u, "server auth") || !slices.Contains(u, "client auth") {
			t.Errorf("Certificate %v usages %v: the hop needs server auth and client auth", dig(c, "metadata", "name"), u)
		}
	}
	// hello-sip dials the hello_tls socket at the instance's node address.
	for _, ip := range []string{"192.168.10.101", "192.168.10.102"} {
		if !slices.Contains(strs(dig(edge, "spec", "ipAddresses")), ip) {
			t.Errorf("Kamailio's certificate lacks %s (a Kamailio node address hello-sip dials)", ip)
		}
	}

	// Every set 1 node is in set 2 by the name the node's certificate carries.
	cm := findObj(t, objs, "ConfigMap", "hello-kamailio")
	var set1, set2 []string
	for _, line := range strings.Split(str(dig(cm, "data", "dispatcher.list")), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[1], "sip:") {
			continue
		}
		host := strings.SplitN(strings.TrimPrefix(f[1], "sip:"), ":", 2)[0]
		switch f[0] {
		case "1":
			set1 = append(set1, host)
		case "2":
			set2 = append(set2, host)
			if !strings.HasSuffix(f[1], ":5061;transport=tls") || !strings.Contains(line, "sockname=hello_tls") {
				t.Errorf("set 2 entry %q is not the node's TLS address from hello_tls", line)
			}
			if !slices.Contains(strs(dig(node, "spec", "dnsNames")), host) {
				t.Errorf("hello-sip's certificate lacks %s, which Kamailio dials", host)
			}
		}
	}
	if len(set1) == 0 || !slices.Equal(set1, set2) {
		t.Errorf("dispatcher set 2 %v does not mirror set 1 %v", set2, set1)
	}

	// wss://hello.kw.watteel.lab/sip reaches the WSS port over HTTPS.
	ing := findObj(t, objs, "Ingress", "kamailio-ws")
	if str(dig(ing, "metadata", "annotations", "nginx.ingress.kubernetes.io/backend-protocol")) != "HTTPS" {
		t.Error("the /sip Ingress does not speak TLS to Kamailio")
	}
	if str(dig(ing, "spec", "rules", 0, "host")) != "hello.kw.watteel.lab" || str(dig(ing, "spec", "rules", 0, "http", "paths", 0, "path")) != "/sip" {
		t.Error("the WSS Ingress is not /sip on the console's host")
	}
	if dig(ing, "metadata", "annotations", "cert-manager.io/cluster-issuer") != nil {
		t.Error("the /sip Ingress must not ask for hello-tls too: the console's Ingress owns it")
	}
	svc := str(dig(ing, "spec", "rules", 0, "http", "paths", 0, "backend", "service", "name"))
	s := findObj(t, objs, "Service", svc)
	if str(dig(s, "spec", "ports", 0, "targetPort")) != "wss" || str(dig(s, "spec", "selector", "app.kubernetes.io/component")) != "edge" {
		t.Errorf("Service %s does not forward to Kamailio's wss port", svc)
	}

	ctl := envOf(containerNamed(t, findObj(t, objs, "Deployment", "hello-control"), false, "hello-control"))
	if got := str(ctl["HELLO_WSS_URL"]["value"]); got != "wss://hello.kw.watteel.lab/sip" {
		t.Errorf("HELLO_WSS_URL = %q", got)
	}
}
