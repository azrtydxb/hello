package sip

import (
	"testing"

	"github.com/azrtydxb/hello/internal/media"
)

// TestCallQualityStats fails if the CDR numbers are not packets and lost
// summed over both directions and the larger jitter, or if a call without a
// relay snapshot reports quality instead of null.
func TestCallQualityStats(t *testing.T) {
	c := &call{}
	if _, _, _, ok := c.qualityStats(); ok {
		t.Fatal("a call without a snapshot has quality")
	}
	c.quality = &media.RelayStats{Directions: map[string]media.DirectionStats{
		"a>b": {Packets: 600, Lost: 4, JitterMs: 3.5},
		"b>a": {Packets: 400, Lost: 6, JitterMs: 9.25},
	}}
	p, l, j, ok := c.qualityStats()
	if !ok || p != 1000 || l != 10 || j != 9.25 {
		t.Fatalf("got %d %d %v %v", p, l, j, ok)
	}
}
