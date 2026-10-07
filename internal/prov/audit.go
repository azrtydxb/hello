package prov

import (
	"context"
	"log/slog"
	"time"
)

// Audit batching (spec S-14): rows are written off the request path, every
// second or every AuditBatch rows; a full queue or a failed insert drops
// rows and counts them, never delaying or failing a response.
const (
	AuditBatch = 200
	auditQueue = 10000
	auditEvery = time.Second
)

// Audit is the buffered fetch-audit writer.
type Audit struct {
	ins interface {
		InsertFetches(ctx context.Context, rows []FetchRecord) error
	}
	m     *Metrics
	log   *slog.Logger
	queue chan FetchRecord
	every time.Duration
}

// NewAudit returns a writer inserting through s; call Run to start it.
func NewAudit(s Store, m *Metrics, log *slog.Logger) *Audit {
	return &Audit{ins: s, m: m, log: log, queue: make(chan FetchRecord, auditQueue), every: auditEvery}
}

// Record queues one row without blocking; its path is redacted again and
// its User-Agent cut to 256 bytes, whatever the caller did.
func (a *Audit) Record(r FetchRecord) {
	r.PathRedacted = RedactPath(r.PathRedacted)
	r.UserAgent = truncateUA(r.UserAgent)
	select {
	case a.queue <- r:
	default:
		a.m.AuditDropped.Inc()
	}
}

// Run writes queued rows until ctx ends, then flushes what is queued.
func (a *Audit) Run(ctx context.Context) {
	t := time.NewTicker(a.every)
	defer t.Stop()
	batch := make([]FetchRecord, 0, AuditBatch)
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case r := <-a.queue:
					batch = append(batch, r)
					if len(batch) == AuditBatch {
						batch = a.write(batch)
					}
				default:
					a.write(batch)
					return
				}
			}
		case r := <-a.queue:
			batch = append(batch, r)
			if len(batch) == AuditBatch {
				batch = a.write(batch)
			}
		case <-t.C:
			batch = a.write(batch)
		}
	}
}

// write inserts batch and returns it emptied; a failure drops the rows.
func (a *Audit) write(batch []FetchRecord) []FetchRecord {
	if len(batch) == 0 {
		return batch
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.ins.InsertFetches(ctx, batch); err != nil {
		a.m.AuditDropped.Add(float64(len(batch)))
		a.log.Warn("provisioning fetch audit rows dropped", "rows", len(batch), "error", err)
	}
	return batch[:0]
}

// truncateUA cuts a User-Agent to 256 bytes on a rune boundary.
func truncateUA(ua string) string {
	if len(ua) <= 256 {
		return ua
	}
	cut := 256
	for cut > 0 && ua[cut]&0xC0 == 0x80 {
		cut--
	}
	return ua[:cut]
}
