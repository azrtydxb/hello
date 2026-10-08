package assistant

import (
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/prometheus/client_golang/prometheus"
)

// TestToolCallMetricRegisteredOnce fails if the assistant registers
// hello_ai_tool_calls_total beside the AI service's own (the process
// would not start: duplicate registration) or if ToolCalls is not what
// counts a call.
func TestToolCallMetricRegisteredOnce(t *testing.T) {
	reg := prometheus.NewRegistry()
	own := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hello_ai_tool_calls_total", Help: "x"}, []string{"operation", "result"})
	reg.MustRegister(own)
	r := newRig(t)
	cfg := r.a.cfg
	cfg.Registerer = reg
	cfg.ToolCalls = func(op, result string) { own.WithLabelValues(op, result).Inc() }
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New beside the service's own metric: %v", err)
	}
	ts, _ := a.newToolset(auth.Agent{UserID: 7, TaskID: "t1"}, func(error) {})
	ts.count("listExtensions", "ok")
	mfs, _ := reg.Gather()
	if len(mfs) != 1 || mfs[0].GetMetric()[0].GetCounter().GetValue() != 1 {
		t.Fatalf("the service's counter did not move: %v", mfs)
	}
}
