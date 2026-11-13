package directory

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the directory series (spec integrations, Observability). Labels
// are kinds, actions and connector ids: never a name, a credential or a user.
type Metrics struct {
	Runs        *prometheus.CounterVec
	Seconds     *prometheus.HistogramVec
	Changes     *prometheus.CounterVec
	Conflicts   *prometheus.GaugeVec
	LastSuccess *prometheus.GaugeVec
}

// NewMetrics registers hello_directory_sync_runs_total{kind,result},
// hello_directory_sync_duration_seconds{kind},
// hello_directory_changes_total{kind,action}, hello_directory_conflicts{connector_kind}
// and hello_directory_last_success_timestamp_seconds{connector} on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_directory_sync_runs_total", Help: "Directory sync runs, by kind (preview, apply, scheduled) and result."}, []string{"kind", "result"}),
		Seconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "hello_directory_sync_duration_seconds", Help: "Duration of a directory sync run, by kind.",
			Buckets: []float64{.5, 1, 5, 15, 60, 180, 600, 900}}, []string{"kind"}),
		Changes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_directory_changes_total", Help: "Changes of directory sync runs, by run kind and action."}, []string{"kind", "action"}),
		Conflicts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_directory_conflicts", Help: "Conflicts of the latest directory run, by connector kind."}, []string{"connector_kind"}),
		LastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_directory_last_success_timestamp_seconds", Help: "Unix time of a connector's last successful run."}, []string{"connector"}),
	}
	reg.MustRegister(m.Runs, m.Seconds, m.Changes, m.Conflicts, m.LastSuccess)
	return m
}

func (m *Metrics) run(kind, result string, seconds float64) {
	if m == nil {
		return
	}
	m.Runs.WithLabelValues(kind, result).Inc()
	m.Seconds.WithLabelValues(kind).Observe(seconds)
}

func (m *Metrics) changes(kind string, counts map[string]int) {
	if m == nil {
		return
	}
	for _, a := range []string{ActionCreate, ActionUpdate, ActionAdopt, ActionDisable, ActionEnable, ActionConflict, ActionSkip, ActionError} {
		if n := counts[a]; n > 0 {
			m.Changes.WithLabelValues(kind, a).Add(float64(n))
		}
	}
}

func (m *Metrics) conflicts(connectorKind string, n int) {
	if m != nil {
		m.Conflicts.WithLabelValues(connectorKind).Set(float64(n))
	}
}

func (m *Metrics) success(id int64, unix float64) {
	if m != nil {
		m.LastSuccess.WithLabelValues(strconv.FormatInt(id, 10)).Set(unix)
	}
}
