package crm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// direct is a connector that signs in with client credentials, as
// Salesforce and Dynamics do: it has no OAuth browser flow and no refresh
// token.
type direct struct {
	fake
	server  bool
	auths   atomic.Int32
	authErr error
	valid   func(string) bool
	now     func() time.Time
}

func (d *direct) Kind() string { return "direct" }
func (d *direct) Authenticate(_ context.Context, cfg Config) (Tokens, error) {
	n := d.auths.Add(1)
	if d.authErr != nil {
		return Tokens{}, d.authErr
	}
	return Tokens{Access: "cc" + string(rune('0'+n)), ExpiresAt: d.now().Add(time.Hour), Account: "org-" + cfg.ClientID}, nil
}
func (d *direct) ServerSide(map[string]string) bool { return d.server }
func (d *direct) ValidRef(r string) bool            { return d.valid == nil || d.valid(r) }
func (d *direct) Setup(cfg Config, public string) []SetupItem {
	return []SetupItem{{Name: "url", Label: "URL", Kind: "url", Value: public + "/x/" + cfg.Extra["clientId"]}}
}

type directRig struct {
	*rig
	d *direct
}

func newDirectRig(t *testing.T, server bool) directRig {
	t.Helper()
	r := newRig(t)
	d := &direct{fake: *newFake(), server: server}
	d.now = func() time.Time { return r.now }
	prov := func(kind string) (Connector, bool) { return d, kind == "direct" }
	r.tokens.Providers = prov
	r.hook.Providers = prov
	c, err := r.repo.CreateConnector(context.Background(), ConnectorInput{Kind: "direct", Name: "sf", Enabled: true,
		Config: map[string]string{"clientId": "cid"}, LinkTTLHours: 720, LogMissed: true}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	r.conn = c
	return directRig{r, d}
}

func TestDirectAuthConnectSavesTokensWithoutABrowser(t *testing.T) {
	r := newDirectRig(t, true)
	ctx := context.Background()
	r.svc.PublicURL = "" // the browser flow would need it; client credentials do not
	u, err := r.svc.Connect(ctx, "admin", r.conn.ID)
	if err != nil || u != "" {
		t.Fatalf("connect = %q, %v", u, err)
	}
	v, _ := r.svc.Get(ctx, r.conn.ID)
	if v.Connection == nil || v.Connection.Status != StatusConnected || v.Connection.ExternalAccount != "org-cid" {
		t.Fatalf("connection = %+v", v.Connection)
	}
	// A secret is needed.
	r2 := newDirectRig(t, true)
	c2, _ := r2.repo.CreateConnector(ctx, ConnectorInput{Kind: "direct", Name: "nosecret", Enabled: true, Config: map[string]string{"clientId": "cid"}, LinkTTLHours: 1}, "")
	if _, err := r2.svc.Connect(ctx, "a", c2.ID); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("no secret = %v", err)
	}
	// A refused app is reported with the CRM's reason and saves nothing.
	r.d.authErr = &APIError{Status: 400, Msg: "invalid_client"}
	if err := r.repo.DeleteOrgConnection(ctx, r.conn.ID); err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Connect(ctx, "admin", r.conn.ID)
	var ve *ValidationError
	if !errors.As(err, &ve) || !contains(ve.Fields["clientSecret"], "invalid_client") {
		t.Fatalf("refused = %v", err)
	}
	if v, _ := r.svc.Get(ctx, r.conn.ID); v.Connection != nil {
		t.Fatal("a refused sign-in saved a connection")
	}
}

func TestDirectAuthSignsInAgainWhenTheTokenExpires(t *testing.T) {
	r := newDirectRig(t, true)
	ctx := context.Background()
	if _, err := r.svc.Connect(ctx, "a", r.conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, cc, err := r.tokens.Conn(ctx, r.conn); err != nil || cc.AccessToken != "cc1" {
		t.Fatalf("conn = %+v, %v", cc, err)
	}
	if r.d.auths.Load() != 1 {
		t.Fatalf("authorized %d times for a live token", r.d.auths.Load())
	}
	r.now = r.now.Add(2 * time.Hour)
	if _, cc, err := r.tokens.Conn(ctx, r.conn); err != nil || cc.AccessToken != "cc2" {
		t.Fatalf("after expiry = %+v, %v", cc, err)
	}
	// A refused token (401 from the CRM) forces a new sign-in too.
	if cc, err := r.tokens.Refresh(ctx, r.conn); err != nil || cc.AccessToken != "cc3" {
		t.Fatalf("forced = %+v, %v", cc, err)
	}
	// Credentials the CRM no longer accepts: needs_reauth once, jobs pause.
	r.d.authErr = &APIError{Status: 400, Msg: "invalid_client"}
	r.now = r.now.Add(2 * time.Hour)
	if _, _, err := r.tokens.Conn(ctx, r.conn); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("refused credentials = %v", err)
	}
	if len(r.notes) != 1 {
		t.Fatalf("notices = %v", r.notes)
	}
	// A CRM that is down is not a re-authorisation.
	r2 := newDirectRig(t, true)
	_, _ = r2.svc.Connect(ctx, "a", r2.conn.ID)
	r2.d.authErr = &APIError{Status: 503, Retry: true, Msg: "down"}
	r2.now = r2.now.Add(2 * time.Hour)
	if _, _, err := r2.tokens.Conn(ctx, r2.conn); err == nil || errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("outage = %v", err)
	}
	if len(r2.notes) != 0 {
		t.Fatal("an outage paged the administrators")
	}
}

func TestInteractiveOnlyConnectorLogsAndLooksUpNothing(t *testing.T) {
	r := newDirectRig(t, false)
	ctx := context.Background()
	_, _ = r.svc.Connect(ctx, "a", r.conn.ID)
	r.feed(t, completed("c1", "inbound", "201"))
	if rows, _ := r.repo.ListLogs(ctx, r.conn.ID, "", 10, 0); len(rows) != 0 {
		t.Fatalf("an interactive-only connector queued %d jobs", len(rows))
	}
	r.look.Invalidate()
	if c := r.look.Find(ctx, "+3211223344", time.Second); c != nil || r.d.lookups.Load() != 0 {
		t.Fatal("an interactive-only connector was asked for a contact")
	}
	// The softphone's handshake needs no row: there is nothing to deduplicate.
	adopted, err := r.svc.Engagement(ctx, "u", "direct", "c1", "00T000000000001AAA", nil, false)
	if err != nil || !adopted {
		t.Fatalf("handshake = %v, %v", adopted, err)
	}
	if rows, _ := r.repo.ListLogs(ctx, r.conn.ID, "", 10, 0); len(rows) != 0 {
		t.Fatal("the handshake stored a job for an interactive-only connector")
	}
}

func TestServerSideConnectorAdoptsTheSoftphonesRecord(t *testing.T) {
	r := newDirectRig(t, true)
	ctx := context.Background()
	_, _ = r.svc.Connect(ctx, "a", r.conn.ID)
	r.d.owners["anna@example.com"] = "005A"
	r.d.valid = func(s string) bool { return s != "bad id" }
	if _, err := r.svc.Engagement(ctx, "u", "direct", "c1", "bad id", nil, false); err == nil {
		t.Fatal("a malformed record id was accepted")
	}
	if adopted, err := r.svc.Engagement(ctx, "u", "direct", "c1", "00T1", nil, false); err != nil || !adopted {
		t.Fatalf("handshake = %v, %v", adopted, err)
	}
	r.feed(t, completed("c1", "outbound", "201"))
	r.worker.Process(ctx)
	if len(r.d.logs) != 0 || len(r.d.updates) != 1 {
		t.Fatalf("logs = %d, updates = %d: the server must patch the softphone's task", len(r.d.logs), len(r.d.updates))
	}
}

func TestSetupNeedsAPublicURL(t *testing.T) {
	r := newDirectRig(t, true)
	ctx := context.Background()
	items, err := r.svc.Setup(ctx, r.conn.ID)
	if err != nil || len(items) != 1 || items[0].Value != "https://hello.example/x/cid" {
		t.Fatalf("setup = %+v, %v", items, err)
	}
	r.svc.PublicURL = ""
	if _, err := r.svc.Setup(ctx, r.conn.ID); !errors.Is(err, ErrNoPublicURL) {
		t.Fatalf("no public url = %v", err)
	}
	// A connector with nothing to set up has an empty list, not an error.
	r3 := newRig(t)
	if items, err := r3.svc.Setup(ctx, r3.conn.ID); err != nil || items == nil || len(items) != 0 {
		t.Fatalf("plain connector = %v, %v", items, err)
	}
}
