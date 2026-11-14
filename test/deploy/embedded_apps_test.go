package deploy

import (
	"strings"
	"testing"
)

// TestUIServesEmbeddedCRMApps fails if the Salesforce softphone or the
// Dynamics widget can be framed by any site other than its CRM, can run
// inline script, or can talk to a host other than Hello; or if a sign-in
// callback page can be framed at all.
func TestUIServesEmbeddedCRMApps(t *testing.T) {
	for loc, host := range map[string]string{
		"= /integrations/salesforce/softphone": "https://*.salesforce.com",
		"= /integrations/dynamics/cif":         "https://*.dynamics.com",
	} {
		body := nginxLocation(t, loc)
		if body == "" {
			t.Fatalf("nginx: no location %s", loc)
		}
		csp := cspOf(body)
		for _, want := range []string{"default-src 'none'", "connect-src 'self'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("nginx %s: the policy lacks %q: %s", loc, want, csp)
			}
		}
		fa := strings.TrimPrefix(strings.SplitN(strings.SplitN(csp, "frame-ancestors ", 2)[1], ";", 2)[0], "")
		if !strings.Contains(fa, host) {
			t.Errorf("nginx %s: %s may not frame the page: %s", loc, host, fa)
		}
		for _, h := range strings.Fields(fa) {
			if !strings.HasPrefix(h, "https://*.") {
				t.Errorf("nginx %s: frame-ancestors allows %q", loc, h)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "frame-ancestors *") {
			t.Errorf("nginx %s: the policy is too open: %s", loc, csp)
		}
		if !strings.Contains(body, "try_files /integrations/") {
			t.Errorf("nginx %s: the page is not served from its own file", loc)
		}
	}
	cb := nginxLocation(t, "~ ^/integrations/(salesforce|dynamics)/callback$")
	if cb == "" {
		t.Fatal("nginx: no location for the sign-in callback pages")
	}
	if !strings.Contains(cspOf(cb), "frame-ancestors 'none'") || !strings.Contains(cb, `X-Frame-Options "DENY"`) {
		t.Errorf("nginx: a callback page may be framed: %s", cb)
	}
}

func cspOf(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		if strings.Contains(ln, "Content-Security-Policy") {
			return ln
		}
	}
	return ""
}
