package deploy

import (
	"strings"
	"testing"
)

// TestChartWebRTCMedia fails if the chart turns WebRTC media, video or video recording settings
// on by default (a default install must render exactly what it did before), or
// if turning them on does not reach hello-sip's environment.
func TestChartWebRTCMedia(t *testing.T) {
	off, err := helmTemplate(t, "-f", valuesFile(t, "values-eval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HELLO_MEDIA_WEBRTC_ENABLED", "HELLO_MEDIA_WEBRTC_PORT", "HELLO_VIDEO_ENABLED", "HELLO_VIDEO_MAX_KBPS", "HELLO_VIDEO_MAX_SESSIONS",
		"HELLO_RECORDING_VIDEO", "HELLO_RECORDING_VIDEO_SEGMENT", "HELLO_RECORDING_VIDEO_SEGMENT_BYTES", "HELLO_RECORDING_VIDEO_QUOTA_GB"} {
		if strings.Contains(off, name) {
			t.Errorf("the default render sets %s", name)
		}
	}
	on, err := helmTemplate(t, "-f", valuesFile(t, "values-eval.yaml"), "--set", "sip.webrtc.enabled=true", "--set", "sip.webrtc.publicIp=203.0.113.5",
		"--set", "sip.webrtc.allowCleartextHop=true", "--set", "sip.video.maxKbps=800", "--set", "sip.video.enabled=false",
		"--set", "sip.recordingVideo.enabled=false", "--set", "sip.recordingVideo.segment=5s", "--set", "sip.recordingVideo.quotaGB=50")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: HELLO_MEDIA_WEBRTC_ENABLED\n              value: \"true\"",
		"name: HELLO_MEDIA_WEBRTC_PUBLIC_IP\n              value: \"203.0.113.5\"",
		"name: HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP\n              value: \"true\"",
		"name: HELLO_VIDEO_MAX_KBPS\n              value: \"800\"",
		"name: HELLO_VIDEO_ENABLED\n              value: \"false\"",
		"name: HELLO_RECORDING_VIDEO\n              value: \"false\"",
		"name: HELLO_RECORDING_VIDEO_SEGMENT\n              value: \"5s\"",
		"name: HELLO_RECORDING_VIDEO_QUOTA_GB\n              value: \"50\"",
	} {
		if !strings.Contains(on, want) {
			t.Errorf("the rendered chart lacks %q", want)
		}
	}
}
