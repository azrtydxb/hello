package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/crm"
	_ "github.com/azrtydxb/hello/internal/crm/bitrix24" // registers the Bitrix24 connector
	_ "github.com/azrtydxb/hello/internal/crm/hubspot"  // registers the HubSpot connector
	_ "github.com/azrtydxb/hello/internal/crm/odoo"     // registers the Odoo connector
	"github.com/azrtydxb/hello/internal/crm/pipedrive"
	_ "github.com/azrtydxb/hello/internal/crm/zoho" // registers the Zoho CRM connector
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/safehttp"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

// crmWiring is hello-control's side of the CRM connectors (spec
// integrations I-47): the event hook that queues calls, the worker that
// writes them, the hourly link renewal and the administration service.
type crmWiring struct {
	Service *crm.Service
	Hook    events.TxHook
	worker  *crm.Worker
	renewer *crm.Renewer
}

// newCRM builds the CRM services. Recording links (spec I-33, I-53) come
// from the recording-link service of integrations phase P4; until it is
// wired as Links, connectors never send a recording to a CRM.
func newCRM(cfg config.Control, st *store.Store, db *sql.DB, box *secret.Box, vk *api.LazyValkey, reg prometheus.Registerer, links crm.LinkMinter, audio crm.AudioSource, log *slog.Logger) *crmWiring {
	repo := &crm.PG{DB: db, Box: box}
	// A self-hosted Odoo or Bitrix24 sits on a private address, so the CRM
	// client may reach the same private ranges webhooks may.
	hc := safehttp.NewClient(safehttp.Options{Timeout: 15 * time.Second, Allow: cfg.IntegrationsAllowedCIDRs})
	pipedrive.MaxUploadBytes = int64(cfg.CRMUploadMaxMB) << 20
	providers := crm.DefaultProvider(hc)
	metrics := crm.NewMetrics(reg)
	clog := log.With("component", "crm")
	tokens := &crm.TokenManager{Repo: repo, Providers: providers, Log: clog,
		Notify: crmReauthNotifier(cfg, st, repo, clog)}
	lookup := &crm.Lookup{Repo: repo, Tokens: tokens, Cache: crm.ValkeyCache{Client: vk.Client}, Log: clog}
	hook := &crm.Hook{Repo: repo, Providers: providers}
	return &crmWiring{
		Service: &crm.Service{Repo: repo, Tokens: tokens, Lookup: lookup, Links: links, PublicURL: cfg.AI.PublicURL, Log: clog},
		Hook:    hook.Handle,
		worker:  &crm.Worker{Repo: repo, Tokens: tokens, Links: links, Audio: audio, Metrics: metrics, Log: clog},
		renewer: &crm.Renewer{Repo: repo, Log: clog},
	}
}

// Run starts the worker and the renewer; it returns when ctx ends.
func (w *crmWiring) Run(ctx context.Context) {
	go w.renewer.Run(ctx)
	w.worker.Run(ctx)
}

// crmReauthNotifier e-mails the administrators that a CRM connection needs
// signing in again. Without SMTP settings the notice is only the banner in
// the console.
func crmReauthNotifier(cfg config.Control, st *store.Store, repo crm.Repo, log *slog.Logger) crm.ReauthNotifier {
	return func(ctx context.Context, c crm.ConnectorRow, reason string) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		s, source, err := api.EffectiveSMTP(ctx, st, envSMTP(cfg))
		if err != nil || source == api.SMTPNone || s.Validate() != nil {
			return
		}
		to, err := repo.AdminEmails(ctx)
		if err != nil {
			log.Warn("crm: reading the administrators' addresses", "error", err)
			return
		}
		link := strings.TrimRight(cfg.AI.PublicURL, "/") + "/integrations/crm"
		for _, addr := range to {
			msg := crmReauthMessage(s.From, addr, c, reason, link, time.Now())
			if msg == nil {
				continue
			}
			if err := mailer.Deliver(ctx, s, addr, msg); err != nil {
				log.Warn("crm: sending the re-authorisation notice", "error", err)
			}
		}
	}
}

// crmReauthMessage renders the notice; nil for an address that cannot be
// used in a header.
func crmReauthMessage(from, to string, c crm.ConnectorRow, reason, link string, now time.Time) []byte {
	if !mailer.ValidAddress(from) || !mailer.ValidAddress(to) {
		return nil
	}
	name := crm.DisplayName(c.Name)
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: Hello: the %s connection needs signing in again\r\nDate: %s\r\nMIME-Version: 1.0\r\n",
		from, to, name, now.Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "The CRM connection %q (%s) in your Hello PBX stopped working: %s.\r\n\r\n", name, c.Kind, crm.DisplayName(reason))
	b.WriteString("No call is lost. Hello keeps the calls waiting and writes them to the CRM once an administrator signs in again")
	if link != "" && strings.HasPrefix(link, "http") {
		fmt.Fprintf(&b, ": %s\r\n", link)
	} else {
		b.WriteString(" (Integrations, CRM).\r\n")
	}
	return b.Bytes()
}
