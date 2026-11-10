package deploy

import "testing"

// TestKwSyslog fails if the kw manifest does not expose the phone syslog sink
// as a UDP NodePort that keeps the phone's source address, or does not point
// hello-control's listener and advertised host at it.
func TestKwSyslog(t *testing.T) {
	docs := kwDocs(t)
	svc := findDoc(docs, "Service", "hello-syslog")
	if svc == nil {
		t.Fatal("no hello-syslog Service")
	}
	if at(svc, "spec", "type") != "NodePort" || at(svc, "spec", "externalTrafficPolicy") != "Local" {
		t.Error("hello-syslog is not a NodePort with externalTrafficPolicy Local; the phone's address is how a line is attributed")
	}
	if at(svc, "spec", "ports", 0, "protocol") != "UDP" || at(svc, "spec", "ports", 0, "targetPort") != "syslog" || at(svc, "spec", "ports", 0, "nodePort") != 30514 {
		t.Error("hello-syslog port is not UDP 30514 -> syslog")
	}
	if at(svc, "spec", "selector", "app.kubernetes.io/component") != "control" {
		t.Error("hello-syslog does not select hello-control")
	}
	c := controlContainer(t, docs)
	var port bool
	ports, _ := c["ports"].([]any)
	for _, p := range ports {
		port = port || (at(p, "name") == "syslog" && at(p, "containerPort") == 5514 && at(p, "protocol") == "UDP")
	}
	if !port {
		t.Error("hello-control has no UDP syslog containerPort 5514")
	}
	if got := envValue(c, "HELLO_SYSLOG_UDP_ADDR"); got != ":5514" {
		t.Errorf("HELLO_SYSLOG_UDP_ADDR = %q", got)
	}
	if got := envValue(c, "HELLO_SYSLOG_HOST"); got != "192.168.10.101:30514" {
		t.Errorf("HELLO_SYSLOG_HOST = %q", got)
	}
}
