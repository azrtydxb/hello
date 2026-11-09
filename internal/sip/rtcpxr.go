// RTCP-XR collector (spec phone-feature-parity S-36). A phone told to report
// voice quality (the Grandstream VQ RTCP-XR collector keys, provisioned when
// HELLO_PROV_RTCPXR is on) sends SIP PUBLISH with Event: vq-rtcpxr and an
// application/vq-rtcpxr body (RFC 6035). The sender is digest-authenticated
// like any request from a phone. The report is stored by the dialog's
// Call-ID, never by anything the body says about who the caller was; the
// console joins it to the call's CDR. Reports are not logged and are bounded
// per device, so a phone cannot fill the table.
package sip

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/rtcpxr"
	"github.com/emiago/sipgo/sip"
	"github.com/prometheus/client_golang/prometheus"
)

// QualityReports stores the voice-quality reports phones publish; the store
// is PostgreSQL. A nil Deps.QualityReports answers every vq-rtcpxr PUBLISH
// 503.
type QualityReports interface {
	RecordRTCPXR(ctx context.Context, device, extension string, r rtcpxr.Report) error
}

// Outcomes of one PUBLISH, the result label of hello_sip_rtcpxr_reports_total.
const (
	rxStored      = "stored"
	rxMalformed   = "malformed"
	rxBadType     = "unsupported_media_type"
	rxRateLimited = "rate_limited"
	rxUnavailable = "unavailable"
	rxBadEvent    = "bad_event"
)

// rtcpxrPerMinute bounds the reports one device may publish in a minute. A
// call sends a report every few minutes at most, plus one at its end.
const rtcpxrPerMinute = 60

// RTCPXRMetrics counts the reports and the quality they carry.
type RTCPXRMetrics struct {
	Reports *prometheus.CounterVec
	MOSLQ   prometheus.Histogram
	Loss    prometheus.Histogram
	Jitter  prometheus.Histogram
}

func newRTCPXRMetrics(reg prometheus.Registerer) *RTCPXRMetrics {
	m := &RTCPXRMetrics{
		Reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_rtcpxr_reports_total",
			Help: "Phone voice-quality PUBLISH reports (Event: vq-rtcpxr) by result.",
		}, []string{"result"}),
		MOSLQ: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_rtcpxr_mos_lq", Help: "Listening-quality MOS phones reported.",
			Buckets: []float64{1.5, 2, 2.5, 3, 3.5, 3.8, 4, 4.2, 4.4},
		}),
		Loss: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_rtcpxr_loss_percent", Help: "Network packet loss rate phones reported, percent.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20},
		}),
		Jitter: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "hello_rtcpxr_jitter_ms", Help: "Inter-arrival jitter phones reported, milliseconds.",
			Buckets: []float64{1, 2, 5, 10, 20, 40, 80, 160},
		}),
	}
	reg.MustRegister(m.Reports, m.MOSLQ, m.Loss, m.Jitter)
	return m
}

// observe records a stored report's figures. Only a call-end report counts,
// so a call is one sample whatever its interval reports.
func (m *RTCPXRMetrics) observe(r rtcpxr.Report) {
	if !r.Final {
		return
	}
	if r.MOSLQ != nil {
		m.MOSLQ.Observe(*r.MOSLQ)
	}
	if r.LossPct != nil {
		m.Loss.Observe(*r.LossPct)
	}
	if r.JitterMs != nil {
		m.Jitter.Observe(*r.JitterMs)
	}
}

// reportWindow is a per-device fixed-window counter; the zero value works.
type reportWindow struct {
	mu    sync.Mutex
	start time.Time
	n     map[string]int
}

// allow counts one report of device and reports whether it is within the limit.
func (w *reportWindow) allow(device string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil || now.Sub(w.start) >= time.Minute {
		w.start, w.n = now, map[string]int{}
	}
	w.n[device]++
	return w.n[device] <= rtcpxrPerMinute
}

// handlePublish serves a PUBLISH. Only Event: vq-rtcpxr is accepted here.
func (s *Server) handlePublish(req *sip.Request, tx sip.ServerTransaction) {
	count := func(result string) { s.m.RTCPXR.Reports.WithLabelValues(result).Inc() }
	if s.refuseIfNotReady(req, tx) {
		count(rxUnavailable)
		return
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		count(rxUnavailable)
		s.unavailable(tx, req)
		return
	}
	dev, ok := s.verify(req, tx, snap)
	if !ok {
		return
	}
	event := ""
	if ev := req.GetHeader("Event"); ev != nil {
		event = strings.ToLower(strings.TrimSpace(strings.SplitN(ev.Value(), ";", 2)[0]))
	}
	if event != rtcpxr.Event {
		count(rxBadEvent)
		s.respond(tx, req, 489, "Bad Event", sip.NewHeader("Allow-Events", rtcpxr.Event))
		return
	}
	if s.deps.QualityReports == nil {
		count(rxUnavailable)
		s.unavailable(tx, req)
		return
	}
	ct := ""
	if h := req.ContentType(); h != nil {
		ct = strings.ToLower(strings.TrimSpace(strings.SplitN(h.Value(), ";", 2)[0]))
	}
	if ct != rtcpxr.ContentType {
		count(rxBadType)
		s.respond(tx, req, sip.StatusUnsupportedMediaType, "Unsupported Media Type", sip.NewHeader("Accept", rtcpxr.ContentType))
		return
	}
	if !s.rtcpxrRate.allow(dev.Username, time.Now()) {
		count(rxRateLimited)
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable", sip.NewHeader("Retry-After", "60"))
		return
	}
	report, err := rtcpxr.Parse(req.Body())
	if err != nil {
		count(rxMalformed)
		s.log.Info("vq-rtcpxr report refused", "device", dev.Username, "error", err.Error())
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	if err := s.deps.QualityReports.RecordRTCPXR(ctx, dev.Username, dev.Extension, report); err != nil {
		count(rxUnavailable)
		s.log.Warn("vq-rtcpxr report not stored", "device", dev.Username, "error", err)
		s.unavailable(tx, req)
		return
	}
	count(rxStored)
	s.m.RTCPXR.observe(report)
	s.respond(tx, req, sip.StatusOK, "OK")
}
