package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// snippet:begin
// verify returns nil when header (Hello-Signature) matches body, the exact
// bytes received, under secret, and is less than 5 minutes old.
func verify(secret, header string, body []byte, now time.Time) error {
	var ts int64
	var sigs []string
	for _, p := range strings.Split(header, ",") {
		switch k, v, _ := strings.Cut(strings.TrimSpace(p), "="); k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if d := now.Sub(time.Unix(ts, 0)); ts == 0 || d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("stale or missing timestamp")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	want := mac.Sum(nil)
	for _, s := range sigs {
		if got, err := hex.DecodeString(s); err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return errors.New("signature mismatch")
}

// snippet:end

const (
	vecSecret = "whsec_test_secret"
	vecBody   = `{"id":"evt_1"}`
	vecT      = 1700000000
	vecSig    = "248a374f50f943a28b0f6ab50faf9a7e7e29b710fa26df9fb1618b9bf8ea9c9a"
)

var vecHeader = fmt.Sprintf("t=%d,v1=%s", vecT, vecSig)

// TestDocumentedGoVerifier fails if the Go verifier published in
// docs/integrations.md does not accept the known-answer signature and refuse
// a tampered body, a stale timestamp and a wrong secret.
func TestDocumentedGoVerifier(t *testing.T) {
	now := time.Unix(vecT, 0)
	if err := verify(vecSecret, vecHeader, []byte(vecBody), now); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"tampered": verify(vecSecret, vecHeader, []byte(`{"id":"evt_2"}`), now),
		"stale":    verify(vecSecret, vecHeader, []byte(vecBody), now.Add(6*time.Minute)),
		"secret":   verify("other", vecHeader, []byte(vecBody), now),
		"empty":    verify(vecSecret, "", []byte(vecBody), now),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// It agrees with the sender's own verifier on what it signs.
	h := SignatureHeader([]string{"a", "b"}, now, []byte("x"))
	if verify("b", h, []byte("x"), now) != nil || verify("a", h, []byte("x"), now) != nil {
		t.Fatal("the documented verifier rejects a rotation-window signature")
	}
}

// TestDocsPublishTheTestedVerifiers fails if docs/integrations.md does not
// contain, verbatim, the Go, Node and Python verifiers this package tests.
func TestDocsPublishTheTestedVerifiers(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "integrations.md"))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile("verifier_test.go")
	if err != nil {
		t.Fatal(err)
	}
	_, after, _ := strings.Cut(string(self), "// snippet:begin\n")
	goSrc, _, _ := strings.Cut(after, "\n// snippet:end")
	srcs := map[string]string{"Go": strings.TrimSpace(goSrc)}
	for name, file := range map[string]string{"Node": "verify.js", "Python": "verify.py"} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		srcs[name] = strings.TrimSpace(string(b))
	}
	for name, src := range srcs {
		if !strings.Contains(string(doc), src) {
			t.Errorf("docs/integrations.md does not contain the tested %s verifier verbatim", name)
		}
	}
}

func runIf(t *testing.T, bin string, args ...string) string {
	t.Helper()
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("%s is not installed", bin)
	}
	out, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", bin, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestDocumentedNodeVerifier runs the Node verifier from the docs against the
// known-answer signature (skipped without node).
func TestDocumentedNodeVerifier(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "verify.js"))
	if err != nil {
		t.Fatal(err)
	}
	harness := fmt.Sprintf(`
const h = %q, body = Buffer.from(%q), now = %d000;
const results = [];
const attempt = (name, f) => { try { f(); results.push(name + ":ok"); } catch (e) { results.push(name + ":" + e.message); } };
attempt("valid", () => verify(%q, h, body, now));
attempt("tampered", () => verify(%q, h, Buffer.from("{}"), now));
attempt("stale", () => verify(%q, h, body, now + 6 * 60000));
attempt("secret", () => verify("other", h, body, now));
attempt("rotation", () => verify(%q, "t=%d," + "v1=00," + "v1=%s", body, now));
console.log(results.join("|"));
`, vecHeader, vecBody, vecT, vecSecret, vecSecret, vecSecret, vecSecret, vecT, vecSig)
	got := runIf(t, "node", "-e", string(src)+harness)
	want := "valid:ok|tampered:signature mismatch|stale:stale or missing timestamp|secret:signature mismatch|rotation:ok"
	if got != want {
		t.Fatalf("node verifier = %s\nwant %s", got, want)
	}
}

// TestDocumentedPythonVerifier runs the Python verifier from the docs against
// the known-answer signature (skipped without python3).
func TestDocumentedPythonVerifier(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "verify.py"))
	if err != nil {
		t.Fatal(err)
	}
	harness := fmt.Sprintf(`
h, body, now = %q, %q.encode(), %d
def attempt(name, **kw):
    try:
        verify(**kw)
        return name + ":ok"
    except ValueError as e:
        return name + ":" + str(e)
print("|".join([
    attempt("valid", secret=%q, header=h, raw_body=body, now=now),
    attempt("tampered", secret=%q, header=h, raw_body=b"{}", now=now),
    attempt("stale", secret=%q, header=h, raw_body=body, now=now + 360),
    attempt("secret", secret="other", header=h, raw_body=body, now=now),
    attempt("rotation", secret=%q, header="t=%d,v1=00,v1=%s", raw_body=body, now=now),
]))
`, vecHeader, vecBody, vecT, vecSecret, vecSecret, vecSecret, vecSecret, vecT, vecSig)
	got := runIf(t, "python3", "-I", "-c", string(src)+harness)
	want := "valid:ok|tampered:signature mismatch|stale:stale or missing timestamp|secret:signature mismatch|rotation:ok"
	if got != want {
		t.Fatalf("python verifier = %s\nwant %s", got, want)
	}
}
