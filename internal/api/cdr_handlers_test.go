package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// TestNote fails if an answered call that failed over gets no note, if the
// note names the wrong response or trunk, or if a failed or plain call
// gets one.
func TestNote(t *testing.T) {
	trace := routing.Trace{
		{N: 1, Text: `Route "UAE Mobile" matched (regex ^05[0-9]{8}$)`},
		{N: 2, Text: "carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable"},
		{N: 3, Text: "Failover permitted for 503"},
		{N: 4, Text: "carrier-backup -> 200 OK"},
	}
	ok := store.CDR{FinalStatus: 200, Trunk: "carrier-backup"}
	if got, want := note(ok, trace), "carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable; failed over to carrier-backup."; got != want {
		t.Fatalf("note = %q, want %q", got, want)
	}
	if got := note(store.CDR{FinalStatus: 503, Trunk: "carrier-backup"}, trace); got != "" {
		t.Fatalf("failed call note = %q, want empty", got)
	}
	if got := note(ok, trace[3:]); got != "" {
		t.Fatalf("no-failover note = %q, want empty", got)
	}
}

// TestCSVSafe fails if a formula survives into a cell, or if a plain
// dialled number is mangled.
func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{
		"+971501234567":   "+971501234567",
		"1001":            "1001",
		"":                "",
		"=HYPERLINK(1)":   "'=HYPERLINK(1)",
		"+1+cmd|' /C'":    "'+1+cmd|' /C'",
		"-2+3":            "'-2+3",
		"@SUM(A1)":        "'@SUM(A1)",
		"carrier-primary": "carrier-primary",
	} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCDRQueryValidation fails if an invalid filter or range reaches the
// store (the stub store panics on any call) instead of answering 400.
func TestCDRQueryValidation(t *testing.T) {
	// Through Handler, so the OpenAPI validator sees each 400 (spec
	// ai-external-access S-3).
	h := Handler(Config{Store: tokenStore{}})
	for _, path := range []string{
		"/api/v1/cdrs?direction=sideways",
		"/api/v1/cdrs?failed=yes",
		"/api/v1/cdrs/export?direction=up",
		"/api/v1/cdrs/concurrency?range=7d",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer anything")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", path, rec.Code, rec.Body)
		}
	}
}

// TestCDRFiltersCountsConcurrencyExport runs the history endpoints against
// PostgreSQL. It fails if a filter keeps the wrong calls, if the counts or
// the concurrency samples miscount, if the export misses rows or a header,
// or if an answered failover call has no note.
func TestCDRFiltersCountsConcurrencyExport(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	// Calls in progress from 3 h to 30 min ago, so every range samples them
	// (the 24h range samples on the hour).
	start := time.Now().UTC().Add(-3 * time.Hour)
	insert := func(corr, dir string, status int, trace string) string {
		t.Helper()
		var id int64
		if err := p.db.QueryRow(`INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, end_time,
			duration_ms, billable_ms, sip_node, final_status, termination_side, failure_reason, direction,
			original_destination, rewritten_destination, route_name, trunk_name, trace)
			VALUES ($1, $1, '=1+1', '0501234567', $2, $3, 1000, 0, 'sip-1', $4, 'system', '', $5,
			'0501234567', '+971501234567', 'UAE Mobile', 'carrier-backup', $6::jsonb) RETURNING id`,
			corr, start, start.Add(150*time.Minute), status, dir, trace).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(id)
	}
	failover := `[{"n":1,"text":"carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable"},{"n":2,"text":"Failover permitted for 503"},{"n":3,"text":"carrier-backup -> 200 OK"}]`
	okOut := insert("c-out-ok", "outbound", 200, failover)
	insert("c-out-fail", "outbound", 503, `[]`)
	insert("c-in-ok", "inbound", 200, `[]`)
	insert("c-int-fail", "internal", 480, `[]`)

	ids := func(path string) string {
		t.Helper()
		var out []string
		for _, it := range c.must(http.StatusOK, "GET", path, nil).json(t)["items"].([]any) {
			out = append(out, it.(map[string]any)["correlationId"].(string))
		}
		return strings.Join(out, ",")
	}
	for path, want := range map[string]string{
		"/api/v1/cdrs":                                "c-int-fail,c-in-ok,c-out-fail,c-out-ok",
		"/api/v1/cdrs?failed=true":                    "c-int-fail,c-out-fail",
		"/api/v1/cdrs?direction=outbound":             "c-out-fail,c-out-ok",
		"/api/v1/cdrs?direction=outbound&failed=true": "c-out-fail",
		"/api/v1/cdrs?direction=inbound&failed=false": "c-in-ok",
		"/api/v1/cdrs?direction=internal&limit=1":     "c-int-fail",
	} {
		if got := ids(path); got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}

	if got := c.must(http.StatusOK, "GET", "/api/v1/cdrs/counts", nil).json(t); got["all"] != float64(4) || got["failed"] != float64(2) {
		t.Fatalf("counts = %v", got)
	}

	for _, rg := range []string{"1h", "6h", "24h"} {
		got := c.must(http.StatusOK, "GET", "/api/v1/cdrs/concurrency?range="+rg, nil).json(t)
		peak, _ := got["peak"].(map[string]any)
		if got["range"] != rg || peak == nil || peak["calls"] != float64(4) {
			t.Fatalf("concurrency %s = %v", rg, got)
		}
		var maxOut float64
		for _, pt := range got["points"].([]any) {
			if o := pt.(map[string]any)["outbound"].(float64); o > maxOut {
				maxOut = o
			}
		}
		if maxOut != 2 {
			t.Fatalf("concurrency %s outbound peak = %v, want 2", rg, maxOut)
		}
	}
	if got := c.must(http.StatusOK, "GET", "/api/v1/cdrs/concurrency", nil).json(t); got["range"] != "6h" || got["stepSeconds"] != float64(900) {
		t.Fatalf("default concurrency = %v", got)
	}

	exp := c.must(http.StatusOK, "GET", "/api/v1/cdrs/export?direction=outbound", nil)
	if ct := exp.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content type = %q", ct)
	}
	rows, err := csv.NewReader(strings.NewReader(string(exp.body))).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][0] != "id" || rows[1][5] != "'=1+1" || rows[1][7] != "+971501234567" {
		t.Fatalf("export = %q (%v)", exp.body, err)
	}

	if d := c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+okOut, nil).json(t); d["note"] != "carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable; failed over to carrier-backup." || d["explanation"] != "" {
		t.Fatalf("failover CDR detail = %v", d)
	}
}

// TestCDRVoiceAgentFilterAndReport: the voiceAgent filter keeps only the
// agent's calls (spec voice-agents S-23), and the CDR detail joins the
// agent's call report by correlation id — unreported until the report
// arrives, then outcome, summary, tool calls, tokens and the transcript
// flag. A CDR that was not routed to an agent carries no voiceAgent object.
func TestCDRVoiceAgentFilterAndReport(t *testing.T) {
	p := newP2Env(t, nil, false)
	c := p.login()
	start := time.Now().UTC().Add(-time.Hour)
	insert := func(corr string, agent string) string {
		t.Helper()
		var id int64
		if err := p.db.QueryRow(`INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, end_time,
			duration_ms, billable_ms, sip_node, final_status, termination_side, failure_reason, direction,
			original_destination, rewritten_destination, route_name, trunk_name, trace, voice_agent_name)
			VALUES ($1, $1, '+97150111111', '2000', $2, $3, 1000, 0, 'sip-1', 200, 'system', '', 'inbound',
			'2000', '2000', 'Agent DID', '', '[]'::jsonb, $4) RETURNING id`,
			corr, start, start.Add(time.Minute), agent).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(id)
	}
	va := insert("c-va", "support")
	plain := insert("c-plain", "")

	ids := func(path string) string {
		t.Helper()
		var out []string
		for _, it := range c.must(http.StatusOK, "GET", path, nil).json(t)["items"].([]any) {
			out = append(out, it.(map[string]any)["correlationId"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := ids("/api/v1/cdrs?voiceAgent=support"); got != "c-va" {
		t.Fatalf("voiceAgent filter = %s, want c-va", got)
	}
	if got := ids("/api/v1/cdrs?voiceAgent=other"); got != "" {
		t.Fatalf("voiceAgent filter = %s, want empty", got)
	}
	c.must(http.StatusBadRequest, "GET", "/api/v1/cdrs?voiceAgent=bad name!", nil)

	d := c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+va, nil).json(t)["voiceAgent"].(map[string]any)
	if d["name"] != "support" || d["outcome"] != "unreported" {
		t.Fatalf("voiceAgent before the report = %v", d)
	}
	if _, err := p.db.Exec(`INSERT INTO voice_agent_calls (correlation_id, agent_name, outcome, summary,
		tool_calls, tokens_in, tokens_out, transcript, reported_at)
		VALUES ('c-va', 'support', 'answered', 'renewed the SIM', '[{"tool":"lookup","ok":true}]', 11, 7, 'hello world', now())`); err != nil {
		t.Fatal(err)
	}
	d = c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+va, nil).json(t)["voiceAgent"].(map[string]any)
	if d["name"] != "support" || d["outcome"] != "answered" || d["summary"] != "renewed the SIM" ||
		d["tokensIn"] != float64(11) || d["tokensOut"] != float64(7) || d["transcriptPresent"] != true ||
		len(d["toolCalls"].([]any)) != 1 {
		t.Fatalf("voiceAgent after the report = %v", d)
	}
	if d2 := c.must(http.StatusOK, "GET", "/api/v1/cdrs/"+plain, nil).json(t); d2["voiceAgent"] != nil {
		t.Fatalf("plain CDR carries a voiceAgent object: %v", d2["voiceAgent"])
	}
}
