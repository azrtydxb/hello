package deploy

import (
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

// insideGuard reports whether the first occurrence of want in cfg sits inside
// an open "#!ifdef <guard>" block.
func insideGuard(cfg, want, guard string) (found, inside bool) {
	i := strings.Index(cfg, want)
	if i < 0 {
		return false, false
	}
	before := cfg[:i]
	open := strings.LastIndex(before, "#!ifdef "+guard)
	closed := strings.LastIndex(before, "#!endif")
	return true, open >= 0 && closed < open
}

// TestKamailioWSSIsOptIn guards SIP over WebSocket (clients-webrtc-mobile-
// video S-8 to S-12): everything WSS needs sits under #!ifdef WSS_PORT, so an
// image without the websocket module still starts when KAMAILIO_WSS_PORT is
// unset, and the handshake is the only HTTP the WSS socket answers.
func TestKamailioWSSIsOptIn(t *testing.T) {
	cfg := readRepo(t, "deploy", "kamailio", "kamailio.cfg")
	for _, want := range []string{
		`loadmodule "websocket.so"`,
		`loadmodule "htable.so"`,
		`modparam("websocket", "keepalive_mechanism", 1)`,
		`modparam("htable", "htable"`,
		`listen=tls:LISTEN_IP:WSS_PORT name "wss"`,
		`listen=udp:LISTEN_IP:5072 name "hello_ws"`,
		"route[WSS_HANDSHAKE] {",
		"route(WSS_HANDSHAKE);",
	} {
		found, inside := insideGuard(cfg, want, "WSS_PORT")
		if !found {
			t.Errorf("kamailio.cfg has no %q", want)
		} else if !inside {
			t.Errorf("%q is not inside #!ifdef WSS_PORT", want)
		}
	}

	hs, ok := kamailioRoute(cfg, "WSS_HANDSHAKE")
	if !ok {
		t.Fatal("route[WSS_HANDSHAKE] not found")
	}
	for _, want := range []string{
		`$hu =~ "^/sip/?$"`,   // one path
		`$hdr(Origin)`,        // cross-site WebSocket hijacking
		`KAMAILIO_WS_ORIGINS`, // the allow-list
		"ws_handle_handshake()",
		"KAMAILIO_WSS_TRUSTED_PROXIES", // X-Forwarded-For only from a trusted peer
	} {
		if !strings.Contains(hs, want) {
			t.Errorf("route[WSS_HANDSHAKE] does not contain %q", want)
		}
	}
	if !strings.Contains(hs, `"403", "Forbidden"`) {
		t.Error("route[WSS_HANDSHAKE] does not refuse an origin that is not allowed with 403")
	}
	// An unset allow-list must allow nothing, not everything.
	if !strings.Contains(hs, `$env(KAMAILIO_WS_ORIGINS) != $null`) || !strings.Contains(hs, `$var(allowed) != 1`) {
		t.Error("route[WSS_HANDSHAKE] must refuse every origin when KAMAILIO_WS_ORIGINS is unset")
	}

	// The HTTP event route hands the WSS socket to the handshake and keeps
	// refusing HTTP on every socket but the metrics one.
	ev := cfg[strings.Index(cfg, "event_route[xhttp:request] {"):]
	ev = ev[:strings.Index(ev, "\n}\n")]
	if !strings.Contains(ev, `$Rn == "wss"`) || !strings.Contains(ev, "$Rp != METRICS_PORT") {
		t.Errorf("event_route[xhttp:request] must route the wss socket to the handshake and refuse the rest:\n%s", ev)
	}
	if strings.Index(ev, "WSS_HANDSHAKE") > strings.Index(ev, "$Rp != METRICS_PORT") {
		t.Error("event_route[xhttp:request]: the WSS handshake must be dispatched before the metrics-port refusal")
	}
}

// TestKamailioWSSRouting guards the Contact, Path and keep-alive rules for
// browsers (S-10): the Contact is aliased, no OPTIONS keep-alive is queued,
// the WebSocket connection carries Hello's requests, the instance's own
// hello socket names the Path and nothing rate-limits browsers by the shared
// ingress address.
func TestKamailioWSSRouting(t *testing.T) {
	cfg := readRepo(t, "deploy", "kamailio", "kamailio.cfg")
	reg, _ := kamailioRoute(cfg, "REGISTER")
	i := strings.Index(reg, "if ($var(ws) == 1) {")
	if i < 0 || !strings.Contains(reg[i:], "set_contact_alias();") {
		t.Error("route[REGISTER] does not alias a browser's Contact")
	}
	if j := strings.Index(reg, "setbflag(FLB_NATSIPPING)"); j >= 0 && i >= 0 && j < i {
		t.Error("route[REGISTER] queues the keep-alive before the WebSocket case")
	}
	ws := reg[i : strings.Index(reg[i:], "} else if")+i]
	if strings.Contains(ws, "FLB_NATSIPPING") {
		t.Error("route[REGISTER] puts a WebSocket contact on the OPTIONS keep-alive list; the ping replaces it")
	}
	if body, _ := kamailioRoute(cfg, "PHONES_SOCKET"); !strings.Contains(body, `$dP == "ws" || $dP == "wss"`) {
		t.Error("route[PHONES_SOCKET] does not leave a WebSocket destination to its connection")
	}
	if body, _ := kamailioRoute(cfg, "HELLO_SOCKET"); !strings.Contains(body, `$fsn = "hello_ws"`) {
		t.Error("route[HELLO_SOCKET] does not use the instance's own hello socket for a browser")
	}
	if body, _ := kamailioRoute(cfg, "EDGE_SIDE"); !strings.Contains(body, `$Rn == "wss" || $Rn == "hello_ws"`) {
		t.Error("route[EDGE_SIDE] does not tell a WebSocket family")
	}
	if body, _ := kamailioRoute(cfg, "FROM_PHONE"); !strings.Contains(body, "$var(ws) != 1") {
		t.Error("route[FROM_PHONE] rate-limits WebSocket clients by the ingress's shared address")
	}
	// Kamailio, not the client, says where a browser is.
	if body, _ := kamailioRoute(cfg, "TO_HELLO"); !strings.Contains(body, "$sht(wsclient=>$conid)") {
		t.Error("route[TO_HELLO] does not report a browser's real address in X-Hello-Client")
	}
}

// TestKamailioImageHasWebsocket fails if the image drops the websocket
// package or stops checking it at build time.
func TestKamailioImageHasWebsocket(t *testing.T) {
	df := readRepo(t, "docker", "kamailio", "Dockerfile")
	for _, want := range []string{"kamailio-websocket-modules", `loadmodule "websocket.so"`, "find /usr/lib -name websocket.so"} {
		if !strings.Contains(df, want) {
			t.Errorf("docker/kamailio/Dockerfile does not contain %q", want)
		}
	}
}

// TestLabKamailioWSS fails if the lab does not turn WSS on and publish it
// (the lab test TestLabWSSRegister needs both).
func TestLabKamailioWSS(t *testing.T) {
	c := readRepo(t, "deploy", "docker-compose", "compose.yaml")
	for _, want := range []string{"KAMAILIO_WSS_PORT:", "KAMAILIO_WS_ORIGINS:", "5083:8088/tcp"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose.yaml does not contain %q", want)
		}
	}
}

// TestChartKamailioWSS fails if the chart enables WSS by default, renders it
// without a certificate or an origin, or does not wire the port, the origins,
// the Service and the /sip Ingress when enabled.
func TestChartKamailioWSS(t *testing.T) {
	has := func(c object, name string) bool {
		_, ok := envOf(c)[name]
		return ok
	}
	off := evalRender(t)
	if has(containerNamed(t, findObj(t, off, "Deployment", "hello-kamailio"), false, "kamailio"), "KAMAILIO_WSS_PORT") {
		t.Error("WSS is on by default; it must be opt-in (an image without the websocket module would not start)")
	}
	if hasObj(off, "Service", "hello-kamailio-ws") || hasObj(off, "Ingress", "hello-kamailio-ws") {
		t.Error("the default render has WSS objects")
	}

	eval := valuesFile(t, "values-eval.yaml")
	_, err := helmTemplate(t, "-f", eval, "--set", "kamailio.wss.enabled=true")
	if err == nil || !strings.Contains(err.Error(), "kamailio.wss.secretName") {
		t.Errorf("WSS without a certificate: want a render failure naming kamailio.wss.secretName, got %v", err)
	}
	_, err = helmTemplate(t, "-f", eval, "--set", "kamailio.wss.enabled=true", "--set", "kamailio.wss.secretName=hello-sip-tls")
	if err == nil || !strings.Contains(err.Error(), "kamailio.wss.origins") {
		t.Errorf("WSS without an origin: want a render failure naming kamailio.wss.origins, got %v", err)
	}

	objs := render(t, "-f", eval, "--set", "kamailio.wss.enabled=true", "--set", "kamailio.wss.secretName=hello-sip-tls",
		"--set", "global.publicUrl=https://hello.kw.watteel.lab/ui/", "--set", "kamailio.wss.trustedProxies=^10[.]42[.]",
		"--set", "ui.ingress.enabled=true", "--set", "ui.ingress.host=hello.kw.watteel.lab", "--set", "ui.ingress.tlsSecretName=hello-tls")
	c := containerNamed(t, findObj(t, objs, "Deployment", "hello-kamailio"), false, "kamailio")
	env := envOf(c)
	if str(env["KAMAILIO_WSS_PORT"]["value"]) != "8088" {
		t.Errorf("KAMAILIO_WSS_PORT = %q, want 8088", str(env["KAMAILIO_WSS_PORT"]["value"]))
	}
	if got := str(env["KAMAILIO_WS_ORIGINS"]["value"]); got != "https://hello.kw.watteel.lab" {
		t.Errorf("KAMAILIO_WS_ORIGINS = %q, want the console's origin (scheme and host of global.publicUrl)", got)
	}
	if got := str(env["KAMAILIO_WSS_TRUSTED_PROXIES"]["value"]); got != `^10[.]42[.]` {
		t.Errorf("KAMAILIO_WSS_TRUSTED_PROXIES = %q", got)
	}
	mounted := false
	for _, m := range list(dig(c, "volumeMounts")) {
		if str(dig(m, "mountPath")) == "/etc/hello-tls" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("the certificate Secret is not mounted at /etc/hello-tls with WSS on")
	}
	svc := findObj(t, objs, "Service", "hello-kamailio-ws")
	if str(dig(svc, "spec", "type")) != "ClusterIP" || str(dig(svc, "spec", "ports", 0, "port")) != "8088" {
		t.Errorf("Service hello-kamailio-ws = %v, want ClusterIP on 8088", dig(svc, "spec"))
	}
	ing := findObj(t, objs, "Ingress", "hello-kamailio-ws")
	if str(dig(ing, "metadata", "annotations", "nginx.ingress.kubernetes.io/backend-protocol")) != "HTTPS" {
		t.Error("the WSS Ingress does not speak HTTPS to Kamailio")
	}
	if str(dig(ing, "spec", "rules", 0, "http", "paths", 0, "path")) != "/sip" {
		t.Error("the WSS Ingress does not publish /sip")
	}
	ctl := containerNamed(t, findObj(t, objs, "Deployment", "hello-control"), false, "hello-control")
	if got := str(envOf(ctl)["HELLO_WSS_URL"]["value"]); got != "wss://hello.kw.watteel.lab/sip" {
		t.Errorf("HELLO_WSS_URL = %q, want wss://hello.kw.watteel.lab/sip", got)
	}
}
