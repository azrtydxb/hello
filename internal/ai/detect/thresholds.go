package detect

import (
	"fmt"
	"strconv"
	"time"
)

// Threshold is one tunable number of a detector, for docs/ai-agent.md.
type Threshold struct {
	Detector string
	Name     string
	Value    string
}

// Thresholds lists every detector threshold with its current value, read
// from the constants the detectors use. docs/ai-agent.md carries the same
// table and TestDocsAIAgent compares them.
func Thresholds() []Threshold {
	d := func(v time.Duration) string { return v.String() }
	n := func(v int) string { return strconv.Itoa(v) }
	return []Threshold{
		{"reg_failures", "window", d(regFailWindow)},
		{"reg_failures", "failed registrations", n(regFailMin)},
		{"auth_bruteforce", "window", d(bruteWindow)},
		{"auth_bruteforce", "distinct usernames from one source", n(bruteUsernames)},
		{"auth_bruteforce", "failures from one source (hello-sip throttle limit)", "10"},
		{"trunk_down", "down for", d(trunkDownFor)},
		{"trunk_asr_drop", "window", d(asrWindow)},
		{"trunk_asr_drop", "baseline", d(asrBaseline)},
		{"trunk_asr_drop", "minimum attempts", n(asrMinAttempts)},
		{"trunk_asr_drop", "status share percent", n(asrStatusShare)},
		{"node_health", "not ready for", d(nodeNotReadyFor)},
		{"node_health", "config revision behind for", d(nodeLagFor)},
		{"node_health", "restart window", d(nodeFlapWindow)},
		{"node_health", "restarts in the window", n(nodeFlapMin)},
		{"trunk_capacity", "percent of channels", n(capacityPercent)},
		{"trunk_capacity", "samples at or over", fmt.Sprintf("%d of %d", capacityHits, capacitySamples)},
		{"call_quality", "window", d(qualityWindow)},
		{"call_quality", "newest calls checked per trunk or node", n(qualityCDRs)},
		{"call_quality", "bad calls", n(qualityBad)},
		{"call_quality", "loss percent", n(qualityLossPercent)},
		{"call_quality", "jitter ms", n(qualityJitterMs)},
		{"config_smells", "device never registered after days", n(deviceNeverRegisteredDays)},
		{"config_smells", "phone never fetched after", d(phoneNeverFetchedAfter)},
		{"config_smells", "phone stale after", d(phoneStaleAfter)},
	}
}
