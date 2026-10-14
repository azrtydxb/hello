package deploy

import (
	"strings"
	"testing"
)

// TestUIProxyStreamsAssistantAnswers fails if the UI's nginx can buffer the
// assistant's event stream: the messages route needs a location of its own
// (a regex location, which wins over /api/) that proxies to hello-control
// with response buffering, caching and compression off, X-Accel-Buffering
// set for the ingress controller in front, and a read timeout above the
// model timeout. The ingress already allows 600 s reads.
func TestUIProxyStreamsAssistantAnswers(t *testing.T) {
	body := nginxLocation(t, "~ ^/api/v1/ai/sessions/[^/]+/messages$")
	if body == "" {
		t.Fatal("nginx: no location for the assistant's messages route")
	}
	for _, want := range []string{
		"set $hello_control ${HELLO_CONTROL_UPSTREAM};",
		"proxy_pass $hello_control;",
		"proxy_http_version 1.1;",
		"X-Forwarded-Proto $hello_forwarded_proto",
		"proxy_buffering off;",
		"proxy_cache off;",
		"gzip off;",
		`add_header X-Accel-Buffering "no" always;`,
		"proxy_read_timeout 600s;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("nginx: the messages location lacks %q", want)
		}
	}
}
