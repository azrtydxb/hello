package crm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

type fakeAudio struct {
	err    error
	opened int
	size   int64
}

func (a *fakeAudio) Open(_ context.Context, id int64) (RecordingFile, error) {
	a.opened++
	if a.err != nil {
		return RecordingFile{}, a.err
	}
	return RecordingFile{Body: io.NopCloser(strings.NewReader("RIFF")), Size: a.size, Name: "r.wav"}, nil
}

// updateFake records the audio an upload connector is handed.
type uploadFake struct {
	*fake
	got  []string
	skip bool
}

func (u *uploadFake) UpdateCallLog(_ context.Context, _ Conn, _ string, l CallLog) error {
	if l.Recording != nil {
		b, _ := io.ReadAll(l.Recording.Body)
		u.got = append(u.got, string(b))
		if u.skip {
			return &SkipError{Reason: "too large"}
		}
	}
	return nil
}

func uploadRig(t *testing.T) (*rig, *uploadFake, *fakeAudio) {
	t.Helper()
	r := newRig(t)
	uf := &uploadFake{fake: r.f}
	uf.caps = Capabilities{Lookup: true, LogCalls: true, Recordings: true, RecordingUpload: true}
	prov := func(kind string) (Connector, bool) { return uf, kind == "fake" }
	r.tokens.Providers = prov
	r.worker.Tokens = r.tokens
	a := &fakeAudio{size: 4}
	r.worker.Audio = a
	r.f.owners["anna@example.com"] = "o1"
	return r, uf, a
}

func recordingReady(callID string, rec int64) events.Entry {
	return events.Entry{Cursor: "1-4", Event: events.New(events.CallRecordingReady, events.Data{CallID: callID, Direction: "inbound", Extension: "201", RecordingID: rec}, "42", time.Now())}
}

func TestUploadConnectorGetsTheAudioAfterTheCallIsKept(t *testing.T) {
	r, uf, a := uploadRig(t)
	ctx := context.Background()
	r.feed(t, recordingReady("c1", 42))
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(ctx) // creates the call and keeps its reference
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogUpdate || row.ExternalID != "ref-1" || len(r.f.logs) != 1 || r.f.logs[0].RecordingURL != "" || len(r.links.minted) != 0 {
		t.Fatalf("after create: row=%+v logs=%d minted=%v", row, len(r.f.logs), r.links.minted)
	}
	r.worker.Process(ctx) // uploads
	row, _ = r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogLogged || len(uf.got) != 1 || uf.got[0] != "RIFF" || a.opened != 1 || row.LinkExpiresAt != nil || len(r.f.logs) != 1 {
		t.Fatalf("after upload: row=%+v got=%v opened=%d", row, uf.got, a.opened)
	}
	if n := r.worker.Process(ctx); n != 0 {
		t.Fatalf("a settled upload ran again (%d)", n)
	}
	if n, _ := r.repo.MarkRenewals(ctx, r.now.Add(300*24*time.Hour), RenewWindow, RenewMaxAge*10); n != 0 {
		t.Fatal("an uploaded recording is queued for renewal")
	}
}

func TestUploadSkippedAndGoneKeepTheCallLogged(t *testing.T) {
	r, uf, a := uploadRig(t)
	ctx := context.Background()
	uf.skip = true
	r.feed(t, completed("c1", "inbound", "201"))
	r.feed(t, recordingReady("c1", 42))
	r.worker.Process(ctx)
	r.worker.Process(ctx)
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogLogged || !strings.Contains(row.LastError, "too large") {
		t.Fatalf("skipped: %+v", row)
	}
	a.err = ErrAudioGone
	r.feed(t, completed("c2", "inbound", "201"))
	r.feed(t, recordingReady("c2", 43))
	r.worker.Process(ctx)
	r.worker.Process(ctx)
	row, _ = r.repo.Log(r.conn.ID, "c2")
	if row.Status != LogLogged || !strings.Contains(row.LastError, "no longer exists") {
		t.Fatalf("gone: %+v", row)
	}
}

func TestUploadFailureRetriesAsAnUpdateNeverACreate(t *testing.T) {
	r, _, a := uploadRig(t)
	ctx := context.Background()
	r.feed(t, completed("c1", "inbound", "201"))
	r.feed(t, recordingReady("c1", 42))
	r.worker.Process(ctx)
	a.err = errors.New("storage down")
	r.worker.Process(ctx)
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogUpdate || row.Attempts != 1 || row.ExternalID != "ref-1" {
		t.Fatalf("row = %+v", row)
	}
	a.err = nil
	r.now = r.now.Add(time.Hour)
	r.worker.Process(ctx)
	row, _ = r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogLogged || len(r.f.logs) != 1 {
		t.Fatalf("row = %+v logs=%d", row, len(r.f.logs))
	}
}

func TestCopyConnectorGetsAShortLinkThatIsNotRenewed(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.f.caps.RecordingCopy = true
	r.f.owners["anna@example.com"] = "o1"
	r.feed(t, recordingReady("c1", 42))
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(ctx)
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if len(r.f.logs) != 1 || r.f.logs[0].RecordingURL == "" || row.LinkExpiresAt != nil {
		t.Fatalf("row=%+v logs=%+v", row, r.f.logs)
	}
}

type staticFake struct{ *fake }

func (s staticFake) Authenticate(_ context.Context, c Config) (Tokens, error) {
	if c.ClientSecret != "good-key" {
		return Tokens{}, errors.New("refused")
	}
	return Tokens{Access: "credential", Account: "7"}, nil
}
func (s staticFake) SecretBoundKeys() []string { return []string{"clientId", "url"} }

func TestCredentialConnectorConnectsWithoutABrowser(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	sf := staticFake{r.f}
	prov := func(kind string) (Connector, bool) { return sf, kind == "fake" }
	r.tokens.Providers = prov
	c, err := r.repo.CreateConnector(ctx, ConnectorInput{Kind: "fake", Name: "odoo", Enabled: true, Config: map[string]string{"clientId": "login", "url": "https://a.example"}, LinkTTLHours: 24}, "bad-key")
	if err != nil {
		t.Fatal(err)
	}
	r.svc.PublicURL = "" // no HELLO_PUBLIC_URL needed
	var ce *ConnectError
	if _, err := r.svc.Connect(ctx, "admin", c.ID); !errors.As(err, &ce) {
		t.Fatalf("bad key = %v", err)
	}
	if _, err := r.svc.Update(ctx, "admin", c.ID, Settings{ClientSecret: ptr("good-key")}); err != nil {
		t.Fatal(err)
	}
	if u, err := r.svc.Connect(ctx, "admin", c.ID); err != nil || u != "" {
		t.Fatalf("connect = %q, %v", u, err)
	}
	v, _ := r.svc.Get(ctx, c.ID)
	if v.Auth != "credential" || v.RedirectURI != "" || v.Connection == nil || v.Connection.ExternalAccount != "7" {
		t.Fatalf("view = %+v", v)
	}
	// The secret is the access token, read from the stored client secret.
	_, cc, err := r.tokens.Conn(ctx, c)
	if err != nil || cc.AccessToken != "good-key" || cc.ExternalAcct != "7" {
		t.Fatalf("conn = %+v, %v", cc, err)
	}
	// Moving the server sends the key elsewhere: it must be re-entered.
	_, err = r.svc.Update(ctx, "admin", c.ID, Settings{Config: map[string]string{"url": "https://evil.example"}})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields["clientSecret"] == "" {
		t.Fatalf("url change without the secret = %v", err)
	}
	if _, err := r.svc.Update(ctx, "admin", c.ID, Settings{Config: map[string]string{"url": "https://b.example"}, ClientSecret: ptr("good-key")}); err != nil {
		t.Fatal(err)
	}
}
