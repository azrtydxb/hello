package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/mailer"
)

func cidrs(t *testing.T, s string) []netip.Prefix {
	t.Helper()
	p, err := parseCIDRs(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func parseCIDRs(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Fields(s) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// TestValidateURL fails if a URL that reaches an internal address, uses a
// scheme or credentials it must not, or is private without an administrator's
// allow-list is accepted, or if one that is allowed is refused.
func TestValidateURL(t *testing.T) {
	public := &Service{}
	private := &Service{AllowedCIDRs: cidrs(t, "10.0.0.0/8 192.168.50.0/24")}
	type tc struct {
		url     string
		private bool
		svc     *Service
		ok      bool
	}
	for _, c := range []tc{
		{"https://hooks.example.com/in", false, public, true},
		{"https://203.0.113.9:8443/in?x=1", false, public, true},
		{"http://hooks.example.com/in", false, public, false},
		{"ftp://hooks.example.com/in", false, public, false},
		{"https://user:pw@hooks.example.com/in", false, public, false},
		{"https://hooks.example.com/in#frag", false, public, false},
		{"/relative", false, public, false},
		{"", false, public, false},
		{"https://127.0.0.1/in", false, public, false},
		{"https://localhost/in", false, public, false},
		{"https://app.localhost/in", false, public, false},
		{"https://[::1]/in", false, public, false},
		{"https://169.254.169.254/latest/meta-data", false, public, false},
		{"https://[fe80::1]/in", false, public, false},
		{"https://10.1.2.3/in", false, public, false},
		{"https://172.16.0.5/in", false, public, false},
		{"https://192.168.1.1/in", false, public, false},
		{"https://100.64.0.1/in", false, public, false},
		{"https://[::ffff:127.0.0.1]/in", false, public, false},
		{"https://[fd00::1]/in", false, public, false},
		{"https://0.0.0.0/in", false, public, false},
		// A private endpoint is allowed only inside the administrator's ranges.
		{"http://10.1.2.3:8080/in", true, private, true},
		{"https://192.168.50.7/in", true, private, true},
		{"http://192.168.1.1/in", true, private, false},
		{"http://172.16.0.5/in", true, private, false},
		{"http://127.0.0.1/in", true, private, false},
		{"http://169.254.169.254/in", true, private, false},
		{"http://[::1]/in", true, private, false},
		{"http://localhost/in", true, private, false},
		{"http://10.1.2.3/in", true, public, false}, // no allow-list at all
	} {
		err := c.svc.ValidateURL(c.url, c.private)
		if (err == nil) != c.ok {
			t.Errorf("ValidateURL(%q, private=%v) = %v, want ok=%v", c.url, c.private, err, c.ok)
		}
		if err != nil && (strings.Contains(err.Error(), "169.254") || strings.Contains(err.Error(), "10.1.2.3")) {
			t.Errorf("the error for %q names the address: %v", c.url, err)
		}
	}
}

// TestConnectTimeRefusesInternalTargets fails if a delivery reaches a server on
// a loopback address (an endpoint stored with a host that resolves there,
// the DNS-rebinding case, is the same dial) or if the refusal is retried:
// the receiver must see nothing and the delivery must be dead at once.
func TestConnectTimeRefusesInternalTargets(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "allowPrivate"}[private], func(t *testing.T) {
			r := newRig(t, maker{"mem", memEnv})
			// The real SSRF-safe clients, and a loopback receiver stored as if
			// its name had resolved there after the URL check.
			r.svc.PublicClient, r.svc.PrivateClient = nil, nil
			r.svc.AllowedCIDRs = cidrs(t, "10.0.0.0/8")
			ep, _ := r.endpoint(EndpointInput{AllowPrivate: private})
			r.env.emit(t, event(events.CallEnded, "200"))
			r.run()
			d := r.delivery(ep.ID)
			if d.Status != StatusDead || d.LastError != "blocked_address" || d.Attempts != 1 {
				t.Fatalf("delivery to loopback = %+v", d)
			}
			if got := len(r.recv.requests()); got != 0 {
				t.Fatalf("the receiver on loopback saw %d requests", got)
			}
		})
	}
}

// TestRedirectsAreNotFollowed fails if a delivery follows a redirect to
// another URL: the redirect answer is a permanent failure.
func TestRedirectsAreNotFollowed(t *testing.T) {
	r := newRig(t, maker{"mem", memEnv})
	other := newReceiver(t)
	r.recv.respond(func(int, *http.Request) (int, map[string]string, string) {
		return 307, map[string]string{"Location": other.srv.URL + "/steal"}, ""
	})
	ep, _ := r.endpoint(EndpointInput{})
	r.env.emit(t, event(events.CallEnded, "200"))
	r.run()
	d := r.delivery(ep.ID)
	if d.Status != StatusDead || !strings.Contains(d.LastError, "redirect_not_followed") || len(other.requests()) != 0 {
		t.Fatalf("delivery = %+v, redirect target saw %d requests", d, len(other.requests()))
	}
}

// TestCreateValidation fails if an endpoint with a bad name, URL, event type
// or extension is accepted, or if a rejection does not name the field.
func TestCreateValidation(t *testing.T) {
	r := newRig(t, maker{"mem", memEnv})
	r.svc.skipURLCheck = false
	ctx := context.Background()
	ok := EndpointInput{Name: "crm", URL: "https://hooks.example.com/in", Enabled: true}
	if _, _, _, err := r.svc.Create(ctx, "u", ok); err != nil {
		t.Fatal(err)
	}
	for field, mutate := range map[string]func(*EndpointInput){
		"name":       func(in *EndpointInput) { in.Name = " " },
		"url":        func(in *EndpointInput) { in.URL = "http://hooks.example.com/in" },
		"events":     func(in *EndpointInput) { in.Events = []string{"call.nope"} },
		"extensions": func(in *EndpointInput) { in.Extensions = []string{"abc"} },
	} {
		in := ok
		in.URL = "https://other.example.com/in"
		mutate(&in)
		_, _, _, err := r.svc.Create(ctx, "u", in)
		var v *ValidationError
		if !errors.As(err, &v) || !errors.Is(err, ErrInvalid) || len(v.Fields) == 0 || v.Fields[0].Path != field {
			t.Errorf("%s: Create = %v", field, err)
		}
	}
}

// TestMailNotifier fails if the disabled-endpoint e-mail is not sent to every
// administrator, names the endpoint's URL, or is sent without SMTP.
func TestMailNotifier(t *testing.T) {
	var sent []string
	var bodies []string
	n := MailNotifier(
		func(context.Context) ([]string, error) { return []string{"a@example.com", "b@example.com"}, nil },
		func(context.Context) (mailer.Settings, bool, error) {
			return mailer.Settings{Host: "smtp.example.com", Port: 25, Security: mailer.SecurityNone, From: "pbx@example.com"}, true, nil
		},
		func(_ context.Context, _ mailer.Settings, to string, msg []byte) error {
			sent = append(sent, to)
			bodies = append(bodies, string(msg))
			return nil
		}, nil)
	n(context.Background(), Endpoint{ID: 7, Name: "CRM sync", URL: "https://hooks.example.com/secret-token"}, ReasonFailures)
	if len(sent) != 2 || sent[0] != "a@example.com" {
		t.Fatalf("sent to %v", sent)
	}
	for _, b := range bodies {
		if !strings.Contains(b, "CRM sync") || strings.Contains(b, "secret-token") || strings.Contains(b, "hooks.example.com") ||
			!strings.Contains(b, "50 deliveries in a row failed") {
			t.Fatalf("message = %s", b)
		}
	}
	off := MailNotifier(nil, func(context.Context) (mailer.Settings, bool, error) { return mailer.Settings{}, false, nil }, nil, nil)
	off(context.Background(), Endpoint{ID: 1}, ReasonGone) // must not panic on nil admins: SMTP is off
}

// TestSignatureWindow fails if the rotation window is not 24 hours.
func TestSignatureWindow(t *testing.T) {
	if SecretWindow != 24*time.Hour {
		t.Fatal(SecretWindow)
	}
}
