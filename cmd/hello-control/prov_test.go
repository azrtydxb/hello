package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/azrtydxb/hello/internal/prov"
)

type fakeCounter struct {
	staleBefore           time.Time
	never, fetched, stale int
	err                   error
}

func (f *fakeCounter) PhoneFetchStates(_ context.Context, staleBefore time.Time) (int, int, int, error) {
	f.staleBefore = staleBefore
	return f.never, f.fetched, f.stale, f.err
}

func gaugeValue(t *testing.T, m *prov.Metrics, state string) float64 {
	t.Helper()
	var d dto.Metric
	if err := m.Phones.WithLabelValues(state).Write(&d); err != nil {
		t.Fatal(err)
	}
	return d.GetGauge().GetValue()
}

// TestSetPhoneStates fails if hello_prov_phones does not carry each fetch
// state's count, if "stale" is not measured from twice the re-check
// interval, or if a failed count overwrites the last published values.
func TestSetPhoneStates(t *testing.T) {
	m := prov.NewMetrics(prometheus.NewRegistry())
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c := &fakeCounter{never: 3, fetched: 5, stale: 2}
	if err := setPhoneStates(context.Background(), c, m, 24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-48 * time.Hour); !c.staleBefore.Equal(want) {
		t.Fatalf("stale bound = %v, want %v", c.staleBefore, want)
	}
	for state, want := range map[string]float64{"never_fetched": 3, "fetched": 5, "stale": 2} {
		if got := gaugeValue(t, m, state); got != want {
			t.Errorf("hello_prov_phones{state=%q} = %v, want %v", state, got, want)
		}
	}
	failing := &fakeCounter{err: errors.New("database down")}
	if err := setPhoneStates(context.Background(), failing, m, 24*time.Hour, now); err == nil {
		t.Fatal("a failed count returned no error")
	}
	if got := gaugeValue(t, m, "fetched"); got != 5 {
		t.Fatalf("a failed count changed hello_prov_phones{state=\"fetched\"} to %v", got)
	}
}
