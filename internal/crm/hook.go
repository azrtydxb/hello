package crm

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/azrtydxb/hello/internal/events"
)

// Hook is the events.TxHook that turns call events into logging jobs. It
// runs in the ingest transaction, so a job exists exactly when its event is
// stored, and a failure rolls the batch back to be retried.
type Hook struct {
	Repo      Repo
	Providers Provider
}

// Handle implements events.TxHook.
func (h *Hook) Handle(ctx context.Context, tx *sql.Tx, e events.Entry) error {
	switch e.Type {
	case events.CallCompleted, events.CallMissed, events.CallRecordingReady:
	default:
		return nil
	}
	d := e.Data
	if d.CallID == "" || d.Direction == "internal" || d.Direction == "" {
		return nil
	}
	rows, err := h.Repo.ActiveConnectors(ctx, tx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		conn, ok := h.Providers(row.Kind)
		if !ok {
			continue
		}
		if !serverSide(conn, row.Config) {
			continue
		}
		caps := conn.Capabilities()
		switch e.Type {
		case events.CallRecordingReady:
			if caps.Recordings && row.Recordings && d.RecordingID != 0 {
				if err := h.Repo.AttachRecording(ctx, tx, row.ID, d.CallID, d.RecordingID); err != nil {
					return err
				}
			}
		default:
			if !caps.LogCalls || (e.Type == events.CallMissed && !row.LogMissed) {
				continue
			}
			payload, err := json.Marshal(d)
			if err != nil {
				return err
			}
			job := LogJob{CallID: d.CallID, Direction: d.Direction, EventType: e.Type, Payload: payload, OwnerExtension: OwnerExtension(d)}
			if job.OwnerExtension == "" && row.FallbackOwner == "" {
				err = h.Repo.Skip(ctx, tx, row.ID, job, "no extension owns this call and the connector has no fallback owner")
			} else {
				err = h.Repo.UpsertCall(ctx, tx, row.ID, job)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// OwnerExtension is the extension a call is logged for: the subject of the
// event when it has one, otherwise the local end of the call.
func OwnerExtension(d events.Data) string {
	if d.Extension != "" {
		return d.Extension
	}
	switch d.Direction {
	case "inbound":
		return d.To.Extension
	case "outbound":
		return d.From.Extension
	}
	return ""
}

// BuildCallLog turns a stored event payload into the call a connector
// writes.
func BuildCallLog(eventType string, d events.Data) CallLog {
	l := CallLog{
		CallID: d.CallID, Direction: d.Direction, Extension: OwnerExtension(d),
		From: d.From.Number, To: d.To.Number,
		StartedAt: d.StartedAt, AnsweredAt: d.AnsweredAt, EndedAt: d.EndedAt,
		Missed: eventType == events.CallMissed,
	}
	if d.Direction == "outbound" {
		l.Number = d.To.Number
	} else {
		l.Number = d.From.Number
	}
	reason := d.Disposition
	if l.Missed {
		reason = d.Reason
	}
	switch strings.ToLower(reason) {
	case "answered":
		l.Outcome = OutcomeAnswered
	case "busy":
		l.Outcome = OutcomeBusy
	case "cancelled":
		l.Outcome = OutcomeCancelled
	case "no_answer", "dnd", "unreachable":
		l.Outcome = OutcomeNoAnswer
	case "":
		l.Outcome = OutcomeAnswered
		if l.Missed || d.AnsweredAt.IsZero() {
			l.Outcome = OutcomeNoAnswer
		}
	default:
		l.Outcome = OutcomeFailed
	}
	switch {
	case l.Outcome == OutcomeAnswered && !d.AnsweredAt.IsZero() && d.EndedAt.After(d.AnsweredAt):
		l.Duration = d.EndedAt.Sub(d.AnsweredAt)
	case l.Outcome == OutcomeAnswered && d.BillableMs > 0:
		l.Duration = msDuration(d.BillableMs)
	default:
		l.Duration = msDuration(d.DurationMs)
	}
	return l
}
