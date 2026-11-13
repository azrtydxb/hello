package deploy

import (
	"strings"
	"testing"
)

// TestChartWebRTCMedia fails if the chart turns WebRTC media or video settings
// on by default (a default install must render exactly what it did before), or
// if turning them on does not reach hello-sip's environment.
func TestChartWebRTCMedia(t *testing.T) {
	off, err := helmTemplate(t, "-f", valuesFile(t, "values-eval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HELLO_MEDIA_WEBRTC_ENABLED", "HELLO_MEDIA_WEBRTC_PORT", "HELLO_VIDEO_ENABLED", "HELLO_VIDEO_MAX_KBPS", "HELLO_VIDEO_MAX_SESSIONS"} {
		if strings.Contains(off, name) {
			t.Errorf("the default render sets %s", name)
		}
	}
	on, err := helmTemplate(t, "-f", valuesFile(t, "values-eval.yaml"), "--set", "sip.webrtc.enabled=true", "--set", "sip.webrtc.publicIp=203.0.113.5",
		"--set", "sip.webrtc.allowCleartextHop=true", "--set", "sip.video.maxKbps=800", "--set", "sip.video.enabled=false")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: HELLO_MEDIA_WEBRTC_ENABLED\n              value: \"true\"",
		"name: HELLO_MEDIA_WEBRTC_PUBLIC_IP\n              value: \"203.0.113.5\"",
		"name: HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP\n              value: \"true\"",
		"name: HELLO_VIDEO_MAX_KBPS\n              value: \"800\"",
		"name: HELLO_VIDEO_ENABLED\n              value: \"false\"",
	} {
		if !strings.Contains(on, want) {
			t.Errorf("the rendered chart lacks %q", want)
		}
	}
}
