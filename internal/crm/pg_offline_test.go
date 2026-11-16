package crm

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/secret"
)

// TestPGStatementsBuild runs every PG method on momo's offline handle, which
// renders the statement and then refuses to run it. It fails if a statement
// does not build (an @name without its argument, an argument no @name uses,
// a malformed literal): those are momo.ErrValidation, while a statement that
// built reaches the handle and gets momo.ErrUnsupported. It needs no
// database, so it runs on every pull request; the DB-backed tests then prove
// what the statements do. The two methods that open a transaction stop at
// BEGIN offline; TestRepoContractPG covers their statements.
func TestPGStatementsBuild(t *testing.T) {
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	p := &PG{DB: momo.NewOffline(dialect.Postgres), Box: box}
	ctx, now := context.Background(), time.Now()
	state, link := "s", now
	calls := map[string]func() error{
		"ListConnectors": func() error { _, err := p.ListConnectors(ctx); return err },
		"GetConnector":   func() error { _, err := p.GetConnector(ctx, 1); return err },
		"DeleteConnector": func() error {
			return p.DeleteConnector(ctx, 1)
		},
		"ClientSecret":       func() error { _, err := p.ClientSecret(ctx, 1); return err },
		"OrgConnection":      func() error { _, _, err := p.OrgConnection(ctx, 1); return err },
		"UserConnection":     func() error { _, _, err := p.UserConnection(ctx, 1, 2); return err },
		"SaveOrgConnection":  func() error { _, err := p.SaveOrgConnection(ctx, 1, Tokens{Access: "a", Refresh: "r"}); return err },
		"SaveUserConnection": func() error { _, err := p.SaveUserConnection(ctx, 1, 2, Tokens{Access: "a"}); return err },
		"DeleteUserConnection": func() error {
			return p.DeleteUserConnection(ctx, 1, 2)
		},
		"UserConnections":     func() error { _, err := p.UserConnections(ctx, 1); return err },
		"UserConnectionsOf":   func() error { _, err := p.UserConnectionsOf(ctx, 2); return err },
		"SetCalendarSettings": func() error { return p.SetCalendarSettings(ctx, 1, true) },
		"SetCalendarState":    func() error { return p.SetCalendarState(ctx, 1, true, now, now, "") },
		"SetConnectionStatus": func() error { return p.SetConnectionStatus(ctx, 1, StatusConnected, "") },
		"DeleteOrgConnection": func() error { return p.DeleteOrgConnection(ctx, 1) },
		"NewState":            func() error { _, err := p.NewState(ctx, 1, "admin", time.Minute); return err },
		"TakeState":           func() error { _, _, err := p.TakeState(ctx, "s"); return err },
		"PurgeStates":         func() error { return p.PurgeStates(ctx, now) },
		"ActiveConnectors":    func() error { _, err := p.ActiveConnectors(ctx, nil); return err },
		"UpsertCall":          func() error { return p.UpsertCall(ctx, nil, 1, LogJob{CallID: "c", Payload: []byte(`{}`)}) },
		"Skip":                func() error { return p.Skip(ctx, nil, 1, LogJob{CallID: "c"}, "why") },
		"AttachRecording":     func() error { return p.AttachRecording(ctx, nil, 1, "c", 7) },
		"AttachEngagement":    func() error { _, err := p.AttachEngagement(ctx, 1, "c", "ref"); return err },
		"ClaimDue":            func() error { _, err := p.ClaimDue(ctx, 10, time.Minute); return err },
		"Finish": func() error {
			return p.Finish(ctx, 1, LogResult{Status: LogLogged, LinkExpiresAt: &link, State: &state})
		},
		"MarkRenewals":     func() error { _, err := p.MarkRenewals(ctx, now, time.Hour, time.Hour); return err },
		"Stats":            func() error { _, err := p.Stats(ctx); return err },
		"AdminEmails":      func() error { _, err := p.AdminEmails(ctx); return err },
		"UserForExtension": func() error { _, _, err := p.UserForExtension(ctx, "100"); return err },
		"Users":            func() error { _, err := p.Users(ctx); return err },
		"Mappings":         func() error { _, err := p.Mappings(ctx, 1); return err },
		"SetMapping":       func() error { return p.SetMapping(ctx, 1, 2, "x", "manual") },
		"DeleteMapping":    func() error { return p.DeleteMapping(ctx, 1, 2) },
		"ListLogs":         func() error { _, err := p.ListLogs(ctx, 1, "", 10, 0); return err },
		"GetLog":           func() error { _, err := p.GetLog(ctx, 1); return err },
		"RetryLog":         func() error { return p.RetryLog(ctx, 1) },
		"CallInvolves":     func() error { _, err := p.CallInvolves(ctx, "c", []string{"100", "200"}); return err },
		"Audit":            func() error { return p.Audit(ctx, "a", "b", "c", "d") },
		"CreateConnector": func() error {
			_, err := p.CreateConnector(ctx, ConnectorInput{Name: "n"}, "secret")
			return err
		},
		"UpdateConnector": func() error {
			_, err := p.UpdateConnector(ctx, 1, ConnectorInput{Name: "n"}, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if errors.Is(err, momo.ErrValidation) || !errors.Is(err, momo.ErrUnsupported) {
				t.Fatalf("err = %v; want the offline handle's momo.ErrUnsupported (the statement must build)", err)
			}
		})
	}
}
