package deploy

import (
	"strings"
	"testing"
)

// TestChartFax fails if the default render carries any fax object or variable
// (fax is off until enabled), if enabling it does not deploy hello-fax as a
// cluster-internal Service with the pod IP as its media host, or if
// hello-control and hello-sip are not told where the engine is.
func TestChartFax(t *testing.T) {
	off := evalRender(t)
	if hasObj(off, "Deployment", "hello-fax") || hasObj(off, "Service", "hello-fax") {
		t.Fatal("the default render deploys hello-fax")
	}
	for _, o := range off {
		if k := str(o["kind"]); k == "Deployment" || k == "StatefulSet" {
			for _, c := range list(dig(podSpec(o), "containers")) {
				if _, ok := envOf(c.(object))["HELLO_FAX_ENABLED"]; ok {
					t.Errorf("%s/%s sets HELLO_FAX_ENABLED with fax off", k, dig(o, "metadata", "name"))
				}
			}
		}
	}

	on := evalRender(t, "--set", "fax.enabled=true")
	fax := findObj(t, on, "Deployment", "hello-fax")
	c := containerNamed(t, fax, false, "hello-fax")
	if !strings.HasSuffix(str(c["image"]), "/hello-fax:main") {
		t.Errorf("image = %s, want hello-fax", c["image"])
	}
	env := envOf(c)
	if str(dig(env["HELLO_FAX_MEDIA_HOST"], "valueFrom", "fieldRef", "fieldPath")) != "status.podIP" {
		t.Errorf("HELLO_FAX_MEDIA_HOST = %v, want the pod IP", env["HELLO_FAX_MEDIA_HOST"])
	}
	if str(env["HELLO_FAX_RTP_PORT_MIN"]["value"]) != "40000" || str(env["HELLO_FAX_RTP_PORT_MAX"]["value"]) != "40999" {
		t.Errorf("RTP range = %v-%v", env["HELLO_FAX_RTP_PORT_MIN"], env["HELLO_FAX_RTP_PORT_MAX"])
	}
	svc := findObj(t, on, "Service", "hello-fax")
	if str(dig(svc, "spec", "type")) != "ClusterIP" {
		t.Errorf("service type = %v, want ClusterIP", dig(svc, "spec", "type"))
	}
	for _, o := range on {
		if str(o["kind"]) == "Ingress" && strings.Contains(str(dig(o, "metadata", "name")), "fax") {
			t.Error("hello-fax is exposed through an Ingress")
		}
	}
	for _, who := range []struct{ kind, name, container string }{
		{"Deployment", "hello-control", "hello-control"}, {"StatefulSet", "hello-sip", "hello-sip"},
	} {
		e := envOf(containerNamed(t, findObj(t, on, who.kind, who.name), false, who.container))
		if str(e["HELLO_FAX_ENABLED"]["value"]) != "true" || !strings.Contains(str(e["HELLO_FAX_ADDR"]["value"]), "hello-fax.hello-test.svc") || !strings.HasSuffix(str(e["HELLO_FAX_ADDR"]["value"]), ":8083") {
			t.Errorf("%s: HELLO_FAX_ENABLED=%v HELLO_FAX_ADDR=%v", who.name, e["HELLO_FAX_ENABLED"], e["HELLO_FAX_ADDR"])
		}
	}
}
