package crm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// directFake is a connector that signs in with credentials, not OAuth. It
// embeds the Connector interface, not the fake, so no OAuth method is
// promoted.
type directFake struct {
	Connector
	auth    func(Config) (Tokens, error)
	logErr  error
	authned int
}

func (d *directFake) Authenticate(_ context.Context, cfg Config) (Tokens, error) {
	d.authned++
	return d.auth(cfg)
}

func (d *directFake) SecretBoundKeys() []string { return []string{"clientId", "host"} }

func (d *directFake) LogCall(ctx context.Context, c Conn, l CallLog) (string, error) {
	if d.logErr != nil {
		return "", d.logErr
	}
	return d.Connector.LogCall(ctx, c, l)
}

func directRig(t *testing.T) (*rig, *directFake) {
	t.Helper()
	r := newRig(t)
	d := &directFake{Connector: r.f}
	d.auth = func(cfg Config) (Tokens, error) {
		if cfg.ClientSecret != "good" && cfg.ClientSecret != "secret" {
			return Tokens{}, &APIError{Status: 401, Msg: "refused the credentials"}
		}
		return Tokens{Access: cfg.ClientSecret, Account: "acme"}, nil
	}
	r.tokens.Providers = func(kind string) (Connector, bool) { return d, kind == "fake" }
	r.f.owners["anna@example.com"] = "o1"
	return r, d
}

func TestConnectDirectStoresTheCredentialAsTheConnection(t *testing.T) {
	r, d := directRig(t)
	ctx := context.Background()
	if err := r.svc.Disconnect(ctx, "a", r.conn.ID); err != nil {
		t.Fatal(err)
	}
	url, err := r.svc.Connect(ctx, "admin", r.conn.ID)
	if err != nil || url != "" {
		t.Fatalf("connect = %q %v", url, err)
	}
	cr, tok, err := r.repo.OrgConnection(ctx, r.conn.ID)
	if err != nil || cr.Status != StatusConnected || tok.Access != "secret" || tok.Account != "acme" || !tok.ExpiresAt.IsZero() {
		t.Fatalf("connection = %+v %+v %v", cr, tok, err)
	}
	v, err := r.svc.Get(ctx, r.conn.ID)
	if err != nil || v.Auth != "direct" || v.Connection == nil || v.Connection.ExternalAccount != "acme" {
		t.Fatalf("view = %+v %v", v, err)
	}
	// The OAuth callback is not for a direct connector.
	if _, err := r.svc.Callback(ctx, "x", "y"); !errors.Is(err, ErrBadState) {
		t.Fatalf("callback = %v", err)
	}
	// Refused credentials: a validation error on the secret, no connection.
	if err := r.svc.Disconnect(ctx, "a", r.conn.ID); err != nil {
		t.Fatal(err)
	}
	d.auth = func(Config) (Tokens, error) { return Tokens{}, &APIError{Status: 401, Msg: "refused the credentials"} }
	_, err = r.svc.Connect(ctx, "admin", r.conn.ID)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields["clientSecret"] == "" {
		t.Fatalf("refused = %v", err)
	}
	if _, _, err := r.repo.OrgConnection(ctx, r.conn.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused credential left a connection")
	}
}

func TestDirectUpdateAsksForTheSecretWhenTheDestinationChanges(t *testing.T) {
	r, d := directRig(t)
	ctx := context.Background()
	host := "other.example.com"
	_, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{Config: map[string]string{"host": host}})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields["clientSecret"] == "" {
		t.Fatalf("host change without secret = %v", err)
	}
	// A new secret is proved before it replaces the stored one.
	bad := "wrong"
	if _, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{ClientSecret: &bad}); !errors.As(err, &ve) {
		t.Fatalf("bad secret = %v", err)
	}
	if s, _ := r.repo.ClientSecret(ctx, r.conn.ID); s != "secret" {
		t.Fatalf("a refused secret replaced the stored one: %q", s)
	}
	good := "good"
	if _, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{ClientSecret: &good, Config: map[string]string{"host": host}}); err != nil {
		t.Fatal(err)
	}
	if _, tok, _ := r.repo.OrgConnection(ctx, r.conn.ID); tok.Access != "good" {
		t.Fatalf("connection token = %q", tok.Access)
	}
	if d.authned != 2 {
		t.Fatalf("authenticated %d times", d.authned)
	}
}

func TestNoTargetSkipsTheCallWithoutSpendingAnAttempt(t *testing.T) {
	r, d := directRig(t)
	d.logErr = fmt.Errorf("%w: no contact has this number", ErrNoTarget)
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogSkipped || row.Attempts != 0 || row.LastError == "" {
		t.Fatalf("row = %+v", row)
	}
	// Retry puts it back; with a target it logs.
	d.logErr = nil
	if _, err := r.svc.RetryLog(context.Background(), "a", row.ID); err != nil {
		t.Fatal(err)
	}
	r.worker.Process(context.Background())
	if row, _ = r.repo.Log(r.conn.ID, "c1"); row.Status != LogLogged {
		t.Fatalf("after retry = %+v", row)
	}
}

func TestExpiringClientCredentialTokenIsRequestedAgain(t *testing.T) {
	r, d := directRig(t)
	n := 0
	d.auth = func(Config) (Tokens, error) {
		n++
		return Tokens{Access: fmt.Sprintf("cc-%d", n), ExpiresAt: r.now.Add(time.Hour)}, nil
	}
	if _, err := r.repo.SaveOrgConnection(context.Background(), r.conn.ID, Tokens{Access: "cc-0", ExpiresAt: r.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour - 30*time.Second)
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	if r.f.tokens[0] != "cc-1" {
		t.Fatalf("the call used %q, want a fresh token", r.f.tokens[0])
	}
}

func TestRefusedStaticCredentialMarksNeedsReauth(t *testing.T) {
	r, d := directRig(t)
	d.auth = func(Config) (Tokens, error) { return Tokens{}, &APIError{Status: 401, Msg: "refused"} }
	r.f.authFail = map[string]bool{"acc1": true}
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	cr, _, _ := r.repo.OrgConnection(context.Background(), r.conn.ID)
	if cr.Status != StatusNeedsReauth || len(r.notes) != 1 {
		t.Fatalf("status = %s notes = %v", cr.Status, r.notes)
	}
}
