package sip

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Call results for hello_calls_total.
const (
	ResultAnswered    = "answered"
	ResultCancelled   = "cancelled"
	ResultBusy        = "busy"
	ResultNoAnswer    = "no_answer"
	ResultNotFound    = "not_found"
	ResultUnavailable = "unavailable"
	ResultFailed      = "failed"
	// ResultBlocked is a call a trunk's screening turned away (pbx-gaps S-7).
	ResultBlocked = "blocked"
)

// Metrics are the SIP node's Prometheus series.
type Metrics struct {
	// Registrations is the number of unexpired bindings in the cluster whose
	// last REGISTER was handled by this node (spec §8 receiving node).
	// Summing it across nodes gives the cluster's binding count.
	Registrations prometheus.Gauge
	// ActiveCalls is the number of calls whose dialogs this node owns.
	ActiveCalls prometheus.Gauge
	Calls       *prometheus.CounterVec // result
	Requests    *prometheus.CounterVec // method
	// Responses counts responses Hello sends (not the 100 Trying and CANCEL
	// handling the transaction layer generates on its own), by status code.
	Responses *prometheus.CounterVec // code

	// Trunks (spec S-14). Status, registration, latency and active calls
	// come from the shared trunk state, so every node reports the same
	// values; TrunkCalls counts this node's trunk attempts by result.
	TrunkStatus         *prometheus.GaugeVec   // trunk, destination: 1 up, 0 down
	TrunkRegistered     *prometheus.GaugeVec   // trunk
	TrunkOptionsLatency *prometheus.GaugeVec   // trunk, destination: last OPTIONS round trip
	TrunkCalls          *prometheus.CounterVec // trunk, result
	TrunkActiveCalls    *prometheus.GaugeVec   // trunk: cluster-wide
	TrunkSlotOvercommit *prometheus.CounterVec // trunk: lost slots re-added over max_calls
	RouteDecision       prometheus.Histogram

	// Phase 4 (spec S-13). VoicemailStorage is what this node stored
	// (cluster-wide totals come from MinIO via hello-control); VoicemailEmail
	// is incremented by the SMTP worker in hello-control through this shared
	// registry. PresenceSubscriptions counts this node's live dialog
	// subscriptions; HoldActive the calls currently held on this node.
	VoicemailMessages     *prometheus.CounterVec // result
	VoicemailStorage      prometheus.Gauge
	VoicemailEmail        *prometheus.CounterVec // result
	Transfers             *prometheus.CounterVec // kind, result
	Parks                 *prometheus.CounterVec // kind, result
	Forwarded             *prometheus.CounterVec // kind
	GroupCalls            *prometheus.CounterVec // group, strategy, result
	PresenceSubscriptions prometheus.Gauge
	// MWINotify counts message-summary NOTIFYs by kind and result (S-1).
	MWINotify  *prometheus.CounterVec // kind, result
	HoldActive prometheus.Gauge
	// CheckSync counts check-sync NOTIFYs by event and result (S-5).
	CheckSync *prometheus.CounterVec // event, result
	// MessagesIn counts SIP MESSAGE requests by source (phone, trunk) and
	// outcome; MessageDeliveries counts each stored message's delivery by
	// route (phone, trunk) and result. Bodies are never labels or logged.
	MessagesIn        *prometheus.CounterVec // source, outcome
	MessageDeliveries *prometheus.CounterVec // via, result

	// Phase E (spec P-12, P-13): server pages and intercom calls.
	PagingTotal   *prometheus.CounterVec // result
	PagingLegs    *prometheus.CounterVec // result
	IntercomTotal *prometheus.CounterVec // result
	// IVRTotal counts IVR menu events: entered, option, direct_dial,
	// timeout, invalid, dial, dial_failed, connected and exit_<kind>.
	IVRTotal *prometheus.CounterVec // event
	// Call queues (migration 00040). QueueWaiting is the callers waiting in
	// queues this node holds (sum it across nodes); QueueWait is the time
	// callers waited by outcome (answered, abandoned, timeout, full,
	// no_agents, breakout); QueueAgents the agents by state, published by
	// one node at a time.
	QueueWaiting   *prometheus.GaugeVec     // queue
	QueueWait      *prometheus.HistogramVec // queue, outcome
	QueueAbandoned *prometheus.CounterVec   // queue
	QueueCalls     *prometheus.CounterVec   // queue, event
	QueueAgents    *prometheus.GaugeVec     // queue, state
	// TimeConditionTotal counts time condition decisions: open, closed,
	// holiday, forced_open, forced_closed, loop, missing and toggled.
	TimeConditionTotal *prometheus.CounterVec // result
	// BlockedCalls counts inbound trunk calls turned away before routing,
	// by reason: blocklist, anonymous or country (pbx-gaps S-7, S-8).
	BlockedCalls *prometheus.CounterVec // reason

	// Phase 7 (in-call HA, spec S-13). DialogReplicated counts replication
	// writes by result; DialogTakeovers the calls this node took over;
	// ZombieCalls the calls no survivor could save.
	DialogReplicated *prometheus.CounterVec // result
	DialogTakeovers  prometheus.Counter
	ZombieCalls      prometheus.Counter

	// Conf is the server conference series (spec S-34).
	Conf *confMetrics
}

// Transfer kinds for hello_transfers_total.
const (
	TransferBlind    = "blind"
	TransferAttended = "attended"
)

// Transfer results.
const (
	TransferAnswered = "answered"
	TransferFailed   = "failed"
)

// Voicemail message results for hello_voicemail_messages_total.
const (
	VMStored  = "stored"
	VMFailed  = "failed"
	VMDiscord = "discarded" // caller hung up, or under one second
)

// Forwarding kinds for hello_forwarded_calls_total.
const (
	ForwardAlways   = "always"
	ForwardBusy     = "busy"
	ForwardNoAnswer = "no_answer"
	ForwardUnreach  = "unreachable"
	ForwardDND      = "dnd"
	ForwardLoop     = "loop"
	// ForwardGroupFailover is a ring group handing the call to an extension
	// or another group when nobody answered.
	ForwardGroupFailover = "group_failover"
)

// Trunk attempt results for hello_trunk_calls_total.
const (
	TrunkAnswered  = "answered"  // the trunk answered the call
	TrunkFailover  = "failover"  // a failover code or no answer: the next one was tried
	TrunkRejected  = "rejected"  // a final response that is not failed over (e.g. 486)
	TrunkCancelled = "cancelled" // the caller hung up during the attempt
	TrunkFull      = "full"      // skipped: concurrency limit reached
)

// NewMetrics registers the SIP series on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Registrations: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_sip_registrations",
			Help: "Unexpired registration bindings whose last REGISTER this node handled.",
		}),
		ActiveCalls: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_active_calls",
			Help: "Calls whose dialogs this node owns (ringing or connected).",
		}),
		Calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_calls_total",
			Help: "Finished call attempts by result.",
		}, []string{"result"}),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_requests_total",
			Help: "SIP requests received, by method.",
		}, []string{"method"}),
		Responses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_responses_total",
			Help: "SIP responses sent by Hello, by status code.",
		}, []string{"code"}),
	}
	m.TrunkStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_status", Help: "Trunk destination health from OPTIONS: 1 up, 0 down.",
	}, []string{"trunk", "destination"})
	m.TrunkRegistered = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_registered", Help: "1 when the trunk's registration is active.",
	}, []string{"trunk"})
	m.TrunkOptionsLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_options_latency_seconds", Help: "Round trip of the last OPTIONS to the destination.",
	}, []string{"trunk", "destination"})
	m.TrunkCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_trunk_calls_total", Help: "Trunk call attempts by result.",
	}, []string{"trunk", "result"})
	m.TrunkActiveCalls = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_trunk_active_calls", Help: "Calls holding a slot on the trunk, cluster-wide.",
	}, []string{"trunk"})
	m.TrunkSlotOvercommit = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_trunk_slot_overcommit_total",
		Help: "Established calls whose lost trunk slot was re-added although the trunk was full.",
	}, []string{"trunk"})
	m.RouteDecision = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "hello_route_decision_seconds", Help: "Time to take a routing decision.",
		Buckets: []float64{.00001, .00005, .0001, .00025, .0005, .001, .0025, .005, .01},
	})
	m.VoicemailMessages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_voicemail_messages_total", Help: "Voicemail messages by result (stored, failed, discarded).",
	}, []string{"result"})
	m.VoicemailStorage = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hello_voicemail_storage_bytes", Help: "Voicemail audio bytes this node stored (cluster total lives in MinIO).",
	})
	m.VoicemailEmail = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_voicemail_email_total", Help: "Voicemail email deliveries by result (sent, failed); incremented by hello-control's worker.",
	}, []string{"result"})
	m.Transfers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_transfers_total", Help: "Blind and attended transfers by kind and result.",
	}, []string{"kind", "result"})
	m.Parks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_park_total", Help: "Call park and pickup operations by kind (park, retrieve, return, pickup) and result.",
	}, []string{"kind", "result"})
	m.Forwarded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_forwarded_calls_total", Help: "Forwarded call attempts by forwarding kind.",
	}, []string{"kind"})
	m.GroupCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_group_calls_total", Help: "Group call attempts by group, strategy and result.",
	}, []string{"group", "strategy", "result"})
	m.PresenceSubscriptions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hello_presence_subscriptions", Help: "Live dialog (BLF) subscriptions served by this node.",
	})
	m.MWINotify = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_mwi_notify_total", Help: "Message-summary NOTIFYs by kind (initial, refresh, change, terminate, unsolicited) and result.",
	}, []string{"kind", "result"})
	m.CheckSync = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_checksync_total", Help: "Check-sync pushes by event and result (ok, not_registered, timeout, auth_failed, failed).",
	}, []string{"event", "result"})
	m.MessagesIn = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_sip_messages_total",
		Help: "SIP MESSAGE requests by source (phone, trunk) and outcome (accepted, delivered, relayed, rejected_*, unavailable).",
	}, []string{"source", "outcome"})
	m.MessageDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_sip_message_deliveries_total",
		Help: "Stored message deliveries by route (phone, trunk) and result (delivered, not_registered, retry, failed).",
	}, []string{"via", "result"})
	m.HoldActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hello_hold_active", Help: "Calls currently held on this node (either leg sendonly).",
	})
	m.PagingTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_paging_total", Help: "Server pages by result (answered, no_answer, busy, denied, capacity, failed).",
	}, []string{"result"})
	m.IVRTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_ivr_total", Help: "IVR menu events (entered, option, direct_dial, timeout, invalid, dial, dial_failed, connected, exit_<kind>).",
	}, []string{"event"})
	m.QueueWaiting = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_queue_waiting", Help: "Callers waiting in a queue whose dialogs this node holds; sum across nodes.",
	}, []string{"queue"})
	m.QueueWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "hello_queue_wait_seconds", Help: "Time callers waited in a queue, by outcome (answered, abandoned, timeout, full, no_agents, breakout).",
		Buckets: []float64{1, 5, 10, 20, 30, 60, 120, 300, 600, 1800},
	}, []string{"queue", "outcome"})
	m.QueueAbandoned = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_queue_abandoned_total", Help: "Callers who hung up while waiting in a queue.",
	}, []string{"queue"})
	m.QueueCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_queue_calls_total", Help: "Queue call events (entered, answered, abandoned, exit_<reason>).",
	}, []string{"queue", "event"})
	m.QueueAgents = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hello_queue_agents", Help: "Agents per queue by state (available, on_call, ringing, dnd, wrapping, paused, logged_out); reported by one node at a time.",
	}, []string{"queue", "state"})
	m.TimeConditionTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_time_condition_total", Help: "Time condition decisions (open, closed, holiday, forced_open, forced_closed, loop, missing, toggled).",
	}, []string{"result"})
	m.BlockedCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_blocked_calls_total", Help: "Inbound trunk calls turned away before routing, by reason (blocklist, anonymous, country).",
	}, []string{"reason"})
	m.PagingLegs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_paging_legs_total", Help: "Page member legs by result (answered, busy, dnd, no_answer, no_registration).",
	}, []string{"result"})
	m.IntercomTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_intercom_total", Help: "Intercom calls by result (auto_answer, ring, denied, failed).",
	}, []string{"result"})
	m.DialogReplicated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_dialog_replicated_total", Help: "Dialog replication writes by result (ok, failed).",
	}, []string{"result"})
	m.DialogTakeovers = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hello_dialog_takeovers_total", Help: "Calls taken over from a dead node by this node.",
	})
	m.ZombieCalls = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hello_zombie_calls_total", Help: "Calls lost with no recovery possible (unreplicated or failed takeover).",
	})
	reg.MustRegister(m.Registrations, m.ActiveCalls, m.Calls, m.Requests, m.Responses,
		m.TrunkStatus, m.TrunkRegistered, m.TrunkOptionsLatency, m.TrunkCalls, m.TrunkActiveCalls, m.TrunkSlotOvercommit, m.RouteDecision,
		m.VoicemailMessages, m.VoicemailStorage, m.VoicemailEmail, m.Transfers, m.Parks, m.Forwarded, m.GroupCalls,
		m.PresenceSubscriptions, m.MWINotify, m.CheckSync, m.MessagesIn, m.MessageDeliveries, m.HoldActive, m.PagingTotal, m.PagingLegs, m.IntercomTotal, m.IVRTotal, m.TimeConditionTotal, m.BlockedCalls,
		m.QueueWaiting, m.QueueWait, m.QueueAbandoned, m.QueueCalls, m.QueueAgents,
		m.DialogReplicated, m.DialogTakeovers, m.ZombieCalls)
	m.Conf = newConfMetrics(reg)
	return m
}

var knownMethods = map[string]bool{
	"REGISTER": true, "INVITE": true, "ACK": true, "BYE": true, "CANCEL": true,
	"OPTIONS": true, "UPDATE": true, "PRACK": true, "INFO": true, "MESSAGE": true,
	"SUBSCRIBE": true, "NOTIFY": true, "REFER": true, "PUBLISH": true,
}

// methodLabel bounds the label's cardinality.
func methodLabel(m string) string {
	if knownMethods[m] {
		return m
	}
	return "other"
}

func (m *Metrics) request(method string) { m.Requests.WithLabelValues(methodLabel(method)).Inc() }

func (m *Metrics) response(code int) { m.Responses.WithLabelValues(strconv.Itoa(code)).Inc() }
