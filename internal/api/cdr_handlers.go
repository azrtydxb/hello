package api

import (
	"encoding/csv"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// cdrFilter reads the direction and failed query parameters shared by the
// CDR list and export, answering 400 itself when one is invalid.
func cdrFilter(w http.ResponseWriter, r *http.Request) (store.CDRFilter, bool) {
	q := r.URL.Query()
	var f store.CDRFilter
	switch d := q.Get("direction"); d {
	case "", "internal", "inbound", "outbound":
		f.Direction = d
	default:
		badRequest(w, "direction must be internal, inbound or outbound")
		return f, false
	}
	switch q.Get("failed") {
	case "", "false":
	case "true":
		f.Failed = true
	default:
		badRequest(w, `failed must be "true" or "false"`)
		return f, false
	}
	return f, true
}

// cdrCounts is GET /api/v1/cdrs/counts: how many CDRs exist and how many
// failed (final status outside 2xx).
func (s *server) cdrCounts(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.CountCDRs(r.Context())
	if err != nil {
		s.internal(w, "count cdrs", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// concurrencyRanges are the windows GET /api/v1/cdrs/concurrency serves,
// with the sampling step of each.
var concurrencyRanges = map[string]struct{ span, step time.Duration }{
	"1h":  {time.Hour, 5 * time.Minute},
	"6h":  {6 * time.Hour, 15 * time.Minute},
	"24h": {24 * time.Hour, time.Hour},
}

type concurrencyPeak struct {
	Calls int64     `json:"calls"`
	At    time.Time `json:"at"`
}

// cdrConcurrency is GET /api/v1/cdrs/concurrency?range=1h|6h|24h: the
// number of recorded calls in progress at each step of the window, by
// direction, and the peak. The window ends at the last whole step.
func (s *server) cdrConcurrency(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "6h"
	}
	rg, ok := concurrencyRanges[name]
	if !ok {
		badRequest(w, "range must be 1h, 6h or 24h")
		return
	}
	to := time.Now().UTC().Truncate(rg.step)
	pts, err := s.Store.CDRConcurrency(r.Context(), to.Add(-rg.span), to, rg.step)
	if err != nil {
		s.internal(w, "cdr concurrency", err)
		return
	}
	var peak *concurrencyPeak
	for _, p := range pts {
		if n := p.Inbound + p.Outbound + p.Internal; n > 0 && (peak == nil || n > peak.Calls) {
			peak = &concurrencyPeak{Calls: n, At: p.At}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range": name, "stepSeconds": int64(rg.step / time.Second), "points": pts, "peak": peak,
	})
}

// maxCDRExport bounds one CSV export.
const maxCDRExport = 10000

// cdrExport is GET /api/v1/cdrs/export: the CDRs matching the list's
// filters as CSV, newest first, at most maxCDRExport rows.
func (s *server) cdrExport(w http.ResponseWriter, r *http.Request) {
	f, ok := cdrFilter(w, r)
	if !ok {
		return
	}
	cs, _, err := s.Store.ListCDRs(r.Context(), f, 0, maxCDRExport)
	if err != nil {
		s.internal(w, "export cdrs", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hello-call-history.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "start_time", "answer_time", "end_time", "direction", "source", "original_destination",
		"rewritten_destination", "route", "trunk", "final_status", "failure_reason", "termination_side", "duration_ms",
		"billable_ms", "sip_node", "media_mode", "sip_call_id", "correlation_id"})
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	for _, c := range cs {
		start, end := c.StartTime, c.EndTime
		row := []string{strconv.FormatInt(c.ID, 10), ts(&start), ts(c.AnswerTime), ts(&end), c.Direction, c.Source,
			c.OriginalDestination, c.RewrittenDestination, c.Route, c.Trunk, strconv.Itoa(c.FinalStatus), c.FailureReason,
			c.TerminationSide, strconv.FormatInt(c.DurationMs, 10), strconv.FormatInt(c.BillableMs, 10), c.SIPNode,
			c.MediaMode, c.SIPCallID, c.CorrelationID}
		for i := range row {
			row[i] = csvSafe(row[i])
		}
		_ = cw.Write(row)
	}
	cw.Flush()
}

// phoneLike is a value a spreadsheet may show as-is although it starts
// with + or -: a dialled number.
var phoneLike = regexp.MustCompile(`^[+-][0-9]*$`)

// csvSafe defuses spreadsheet formulas: caller IDs and dialled numbers come
// from the network, so a cell that a spreadsheet would evaluate gets a
// leading apostrophe. Plain numbers such as +971501234567 are kept.
func csvSafe(v string) string {
	if v == "" || phoneLike.MatchString(v) {
		return v
	}
	if strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// note explains an answered call whose trace shows a failover: the trunk
// response that caused it and the trunk that answered. Empty otherwise.
func note(c store.CDR, trace routing.Trace) string {
	if c.FinalStatus < 200 || c.FinalStatus >= 300 {
		return ""
	}
	prev := ""
	for _, st := range trace {
		if prev != "" && strings.HasPrefix(st.Text, "Failover permitted for ") {
			n := prev + "; failed over"
			if c.Trunk != "" {
				n += " to " + c.Trunk
			}
			return n + "."
		}
		prev = st.Text
	}
	return ""
}
