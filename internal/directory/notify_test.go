package directory

import (
	"context"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/mailer"
)

func TestMailNotifier(t *testing.T) {
	var sent []string
	n := MailNotifier(
		func(context.Context) ([]string, error) { return []string{"admin@corp.org", "not an address"}, nil },
		func(context.Context) (mailer.Settings, bool, error) {
			return mailer.Settings{From: "hello@corp.org"}, true, nil
		},
		func(_ context.Context, _ mailer.Settings, to string, msg []byte) error {
			sent = append(sent, to+"\n"+string(msg))
			return nil
		}, nil)
	n(context.Background(), Connector{ID: 4, Name: "Corp AD", Config: []byte(`{"url":"ldaps://dc.internal","bindDn":"cn=svc"}`)}, CodeAuthFailed)
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "admin@corp.org\n") {
		t.Fatalf("sent = %v", sent)
	}
	for _, leak := range []string{"dc.internal", "cn=svc"} {
		if strings.Contains(sent[0], leak) {
			t.Errorf("the message carries %q", leak)
		}
	}
	if !strings.Contains(sent[0], "Corp AD") || !strings.Contains(sent[0], "auth_failed") {
		t.Errorf("message = %s", sent[0])
	}
	// No SMTP: nothing is sent.
	sent = nil
	MailNotifier(func(context.Context) ([]string, error) { return []string{"a@b.org"}, nil },
		func(context.Context) (mailer.Settings, bool, error) { return mailer.Settings{}, false, nil },
		func(context.Context, mailer.Settings, string, []byte) error { sent = append(sent, "x"); return nil }, nil)(context.Background(), Connector{}, "x")
	if len(sent) != 0 {
		t.Fatal("sent without SMTP")
	}
}
