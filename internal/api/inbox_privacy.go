package api

// The messaging inbox's reports and privacy tooling (spec messaging-channels
// phase 8): reports and their targets, the export of one customer (a data
// subject access request), the redaction of one message, the provider-side
// deletions behind an erase or a redaction, and the supervisor-view audit
// report. None of it logs content or a number.

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// InboxPrivacyStore is the persistence of the reports and privacy tools;
// *store.Store implements it. It is read from Config.Inbox, so an inbox
// store without it answers 503 on these routes.
type InboxPrivacyStore interface {
	GetInboxReport(ctx context.Context, f store.InboxReportFilter) (store.InboxReport, error)
	GetInboxSLA(ctx context.Context) (store.InboxSLA, error)
	PutInboxSLA(ctx context.Context, actor string, t store.InboxSLA) (store.InboxSLA, error)
	ExportPeer(ctx context.Context, actor, peerKey string, notes bool) (store.PeerExport, error)
	RedactConversationMessage(ctx context.Context, actor string, convID, msgID int64) (store.RedactReport, error)
	ProviderReport(ctx context.Context, actor, batch string, providers []string) ([]store.EraseProvider, error)
	RedactionCounts(ctx context.Context, batch string) ([]store.RedactionCount, error)
	SupervisorViews(ctx context.Context, f store.SupervisorViewFilter) (store.SupervisorViewReport, error)
}

var _ InboxPrivacyStore = (*store.Store)(nil)

// MsgRedactor deletes the queued messages of one erase or redaction at their
// carriers while the request waits; *messaging.Redactor implements it.
type MsgRedactor interface {
	RunBatch(ctx context.Context, batch string, budget time.Duration)
}

// redactBudget is how long an erase or redaction waits for the carriers;
// what is left is retried in the background.
var redactBudget = 20 * time.Second

func (s *server) privacyStore(w http.ResponseWriter) (InboxPrivacyStore, bool) {
	if s.Inbox == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the messaging inbox is not configured")
		return nil, false
	}
	ps, ok := s.Inbox.(InboxPrivacyStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the messaging reports are not available")
		return nil, false
	}
	return ps, true
}

// providerReport runs a batch of provider-side deletions for at most
// redactBudget and reports it per provider.
func (s *server) providerReport(ctx context.Context, ps InboxPrivacyStore, actor, batch string, providers []string) ([]store.EraseProvider, error) {
	if batch != "" && s.MsgRedactor != nil {
		s.MsgRedactor.RunBatch(ctx, batch, redactBudget)
	}
	return ps.ProviderReport(ctx, actor, batch, providers)
}

// ---------------------------------------------------------------------------
// Reports.

// maxReportDays bounds a report's period.
const maxReportDays = 366

// reportFilter reads from, to, groupBy and timeZone. The period defaults to
// the last 7 days.
func (s *server) reportFilter(w http.ResponseWriter, r *http.Request) (store.InboxReportFilter, bool) {
	q := r.URL.Query()
	now := time.Now().UTC()
	f := store.InboxReportFilter{To: now, From: now.AddDate(0, 0, -7), GroupBy: q.Get("groupBy")}
	var bad []routing.FieldError
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad = append(bad, routing.FieldError{Path: p.name, Message: "must be an RFC 3339 time"})
				continue
			}
			*p.dst = t.UTC()
		}
	}
	switch f.GroupBy {
	case store.ReportByNone, store.ReportByChannel, store.ReportByQueue, store.ReportByAgent, store.ReportByDay:
	default:
		bad = append(bad, routing.FieldError{Path: "groupBy", Message: "must be channel, queue, agent or day"})
	}
	zone := q.Get("timeZone")
	if zone == "" {
		zone = s.DefaultTimeZone
	}
	loc := time.UTC
	if zone != "" {
		l, err := time.LoadLocation(zone)
		if err != nil {
			bad = append(bad, routing.FieldError{Path: "timeZone", Message: "must be an IANA time zone"})
		} else {
			loc = l
		}
	}
	f.Location = loc
	if len(bad) == 0 && !f.From.Before(f.To) {
		bad = append(bad, routing.FieldError{Path: "from", Message: "must be before to"})
	}
	if len(bad) == 0 && f.To.Sub(f.From) > maxReportDays*24*time.Hour {
		bad = append(bad, routing.FieldError{Path: "from", Message: fmt.Sprintf("the period is at most %d days", maxReportDays)})
	}
	if len(bad) > 0 {
		writeFields(w, bad)
		return f, false
	}
	return f, true
}

func (s *server) getInboxReport(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	f, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	rep, err := ps.GetInboxReport(r.Context(), f)
	if err != nil {
		s.inboxError(w, "inbox report", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// exportInboxReport is the report as CSV: the totals first, then one line
// per group.
func (s *server) exportInboxReport(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	f, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	rep, err := ps.GetInboxReport(r.Context(), f)
	if err != nil {
		s.inboxError(w, "inbox report", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hello-inbox-report.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"group", "key", "label", "conversations", "inbound_conversations", "outbound_conversations", "resolved",
		"messages_in", "messages_out", "first_response_count", "first_response_avg_s", "first_response_p50_s", "first_response_p90_s",
		"resolution_count", "resolution_avg_s", "resolution_p50_s", "resolution_p90_s", "sla_first_response_breaches",
		"sla_resolution_breaches"})
	sec := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	line := func(group string, x store.InboxReportRow) {
		row := []string{group, x.Key, x.Label, strconv.Itoa(x.Conversations), strconv.Itoa(x.InboundConversations),
			strconv.Itoa(x.OutboundConversations), strconv.Itoa(x.Resolved), strconv.Itoa(x.MessagesIn), strconv.Itoa(x.MessagesOut),
			strconv.Itoa(x.FirstResponse.Count), sec(x.FirstResponse.AvgSeconds), sec(x.FirstResponse.P50Seconds), sec(x.FirstResponse.P90Seconds),
			strconv.Itoa(x.Resolution.Count), sec(x.Resolution.AvgSeconds), sec(x.Resolution.P50Seconds), sec(x.Resolution.P90Seconds),
			strconv.Itoa(x.SLAFirstResponseBreaches), strconv.Itoa(x.SLAResolutionBreaches)}
		for i := range row {
			row[i] = csvSafe(row[i])
		}
		_ = cw.Write(row)
	}
	line("total", rep.Totals)
	for _, x := range rep.Rows {
		line(rep.GroupBy, x)
	}
	cw.Flush()
}

func (s *server) getInboxSLA(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	t, err := ps.GetInboxSLA(r.Context())
	if err != nil {
		s.inboxError(w, "inbox targets", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) putInboxSLA(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	var b struct {
		FirstResponseSeconds *int `json:"firstResponseSeconds"`
		ResolutionSeconds    *int `json:"resolutionSeconds"`
	}
	if !decode(w, r, &b) {
		return
	}
	var bad []routing.FieldError
	if b.FirstResponseSeconds == nil || *b.FirstResponseSeconds < 0 || *b.FirstResponseSeconds > 604800 {
		bad = append(bad, routing.FieldError{Path: "firstResponseSeconds", Message: "required, 0 (off) to 604800 seconds"})
	}
	if b.ResolutionSeconds == nil || *b.ResolutionSeconds < 0 || *b.ResolutionSeconds > 2592000 {
		bad = append(bad, routing.FieldError{Path: "resolutionSeconds", Message: "required, 0 (off) to 2592000 seconds"})
	}
	if len(bad) > 0 {
		writeFields(w, bad)
		return
	}
	t, err := ps.PutInboxSLA(r.Context(), actor(r).String(), store.InboxSLA{FirstResponseSeconds: *b.FirstResponseSeconds,
		ResolutionSeconds: *b.ResolutionSeconds})
	if err != nil {
		s.inboxError(w, "inbox targets", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---------------------------------------------------------------------------
// Export (DSAR).

// exportPeerKey reads the customer of an export or erase: a phone number or
// a chat visitor (visitor:<id>).
func (s *server) exportPeerKey(raw string) (string, bool) {
	if key, ok := chatPeerKey(raw); ok {
		return key, true
	}
	return s.peerKey(raw)
}

// exportMessagingPeer streams a zip of everything the inbox holds of one
// customer: conversations.json (the conversations and the opt-outs), one
// JSON file per message, and a README. It is audited with counts only.
func (s *server) exportMessagingPeer(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	key, ok := s.exportPeerKey(q.Get("peer"))
	if !ok {
		writeFields(w, fieldErrs{{Path: "peer", Message: "must be a phone number or a chat visitor (visitor:<id>)"}})
		return
	}
	notes := false
	switch q.Get("notes") {
	case "", "false":
	case "true":
		notes = true
	default:
		writeFields(w, fieldErrs{{Path: "notes", Message: "must be true or false"}})
		return
	}
	exp, err := ps.ExportPeer(r.Context(), actor(r).String(), key, notes)
	if errors.Is(err, store.ErrExportTooLarge) {
		writeError(w, http.StatusUnprocessableEntity, "too_large",
			fmt.Sprintf("the customer has more than %d messages, more than one export holds", store.MaxExportMessages))
		return
	}
	if err != nil {
		s.inboxError(w, "export", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="hello-inbox-export.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	put := func(name string, v any) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: exp.ExportedAt})
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	readme := "Everything Hello's messaging inbox holds of " + exp.Peer + ", exported " + exp.ExportedAt.Format(time.RFC3339) + ".\n\n" +
		"conversations.json lists the conversations and the opt-out records; messages/<conversation>/<message>.json holds each\n" +
		"message with its times and delivery state. A redacted message has an empty body and a redactedAt time.\n" +
		"The carrier (the SMS provider or SIP trunk) keeps its own records under its own terms.\n"
	f, err := zw.CreateHeader(&zip.FileHeader{Name: "README.txt", Method: zip.Deflate, Modified: exp.ExportedAt})
	if err == nil {
		_, err = f.Write([]byte(readme))
	}
	if err == nil {
		err = put("conversations.json", exp)
	}
	for _, c := range exp.Conversations {
		for _, m := range c.Messages {
			if err != nil {
				break
			}
			err = put("messages/"+strconv.FormatInt(c.ID, 10)+"/"+strconv.FormatInt(m.ID, 10)+".json", m)
		}
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		// The headers are out: the client sees a broken archive.
		s.Log.Warn("inbox export: writing the archive", "error", err)
	}
}

// ---------------------------------------------------------------------------
// Redaction.

func (s *server) redactInboxMessage(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	conv, ok := pathID(w, r)
	if !ok {
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("messageId"), 10, 64)
	if err != nil || msgID <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	who := actor(r).String()
	rep, err := ps.RedactConversationMessage(r.Context(), who, conv, msgID)
	switch {
	case errors.Is(err, store.ErrNotRedactable):
		writeError(w, http.StatusConflict, "not_redactable", "a system line has no customer text to redact")
		return
	case err != nil:
		s.inboxError(w, "message", err)
		return
	}
	if rep.Batch != "" {
		if rep.Providers, err = s.providerReport(r.Context(), ps, who, rep.Batch, nil); err != nil {
			s.internal(w, "redact: provider report", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, rep)
}

// listMessagingRedactions counts the provider-side deletions of the last 30
// days (and every one still being retried), by provider and state.
func (s *server) listMessagingRedactions(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	cs, err := ps.RedactionCounts(r.Context(), "")
	if err != nil {
		s.internal(w, "redaction counts", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

// ---------------------------------------------------------------------------
// The supervisor-view audit report.

func (s *server) listSupervisorViews(w http.ResponseWriter, r *http.Request) {
	ps, ok := s.privacyStore(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	f := store.SupervisorViewFilter{To: now, From: now.AddDate(0, 0, -30), Limit: 100, Actor: strings.TrimSpace(q.Get("actor"))}
	var bad []routing.FieldError
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad = append(bad, routing.FieldError{Path: p.name, Message: "must be an RFC 3339 time"})
				continue
			}
			*p.dst = t.UTC()
		}
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			bad = append(bad, routing.FieldError{Path: "before", Message: "must be a positive id"})
		}
		f.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			bad = append(bad, routing.FieldError{Path: "limit", Message: "must be 1 to 500"})
		}
		f.Limit = n
	}
	if len(f.Actor) > 200 {
		bad = append(bad, routing.FieldError{Path: "actor", Message: "too long"})
	}
	if len(bad) == 0 && !f.From.Before(f.To) {
		bad = append(bad, routing.FieldError{Path: "from", Message: "must be before to"})
	}
	if len(bad) > 0 {
		writeFields(w, bad)
		return
	}
	rep, err := ps.SupervisorViews(r.Context(), f)
	if err != nil {
		s.internal(w, "supervisor views", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
