package sidebar

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func read(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestZendeskAppHasTheManifestAtTheRootAndHelloURL(t *testing.T) {
	name, data, err := Build("zendesk", "https://hello.example.com/")
	if err != nil || name != "hello-zendesk-app.zip" {
		t.Fatalf("build = %q %v", name, err)
	}
	f := read(t, data)
	for _, want := range []string{"manifest.json", "assets/iframe.html", "assets/main.js", "translations/en.json"} {
		if _, ok := f[want]; !ok {
			t.Errorf("missing %s (have %d files)", want, len(f))
		}
	}
	js := f["assets/main.js"]
	if !strings.Contains(js, `var HELLO_URL = "https://hello.example.com";`) || strings.Contains(js, placeholder) {
		t.Fatalf("address not set: %s", js[:200])
	}
	if strings.Contains(js, "innerHTML") {
		t.Fatal("the app must not write HTML")
	}
}

func TestFreshdeskAppAndDeterminism(t *testing.T) {
	_, a, err := Build("freshdesk", "https://hello.example.com/base")
	if err != nil {
		t.Fatal(err)
	}
	_, b, _ := Build("freshdesk", "https://hello.example.com/base")
	if !bytes.Equal(a, b) {
		t.Fatal("the package is not reproducible")
	}
	f := read(t, a)
	for _, want := range []string{"manifest.json", "app/index.html", "app/scripts/app.js", "app/icon.svg", "config/iparams.json", "config/requests.json"} {
		if _, ok := f[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(f["app/scripts/app.js"], `"https://hello.example.com/base"`) {
		t.Fatal("address not set")
	}
	if !strings.Contains(f["config/iparams.json"], `"secure": true`) {
		t.Fatal("the API key parameter must be secure")
	}
}

func TestAddressIsValidatedAndEscaped(t *testing.T) {
	for _, bad := range []string{"", "http://hello.example.com", "https://", "javascript:alert(1)", "https://u:p@hello.example.com"} {
		if _, _, err := Build("zendesk", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// A hostile path cannot leave the string literal or the script block.
	_, data, err := Build("zendesk", `https://hello.example.com/</script><script>x`)
	if err != nil {
		t.Fatal(err)
	}
	js := read(t, data)["assets/main.js"]
	if strings.Contains(js, "</script>") {
		t.Fatalf("script break-out: %s", js[:300])
	}
	if _, _, err := Build("hubspot", "https://hello.example.com"); !errors.Is(err, ErrNoApp) {
		t.Fatalf("hubspot = %v", err)
	}
}
