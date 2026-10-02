package telemetry

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactURL(t *testing.T) {
	cases := []string{
		"postgres://hello:s3cret@db:5432/hello?sslmode=disable",
		"postgres://db:5432/hello?user=hello&password=s3cret",
		"postgres://db/hello?sslkey=k.pem&sslpassword=s3cret",
		"host=db user=hello password=s3cret dbname=hello", // keyword/value DSN
		"postgres://hello:s3cret@db:5432/%zz",             // unparseable
	}
	for _, in := range cases {
		var buf bytes.Buffer
		NewLogger(&buf, slog.LevelInfo, "test", "n1").Info("start", "database_url", RedactURL(in))
		if strings.Contains(buf.String(), "s3cret") {
			t.Fatalf("password leaked for %q: %s", in, buf.String())
		}
		if !strings.Contains(buf.String(), "REDACTED") {
			t.Fatalf("no REDACTED marker for %q: %s", in, buf.String())
		}
	}
	if got := RedactURL("postgres://db/hello"); got != "postgres://db/hello" {
		t.Fatalf("URL without secret changed: %s", got)
	}
}
