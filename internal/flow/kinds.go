package flow

// This file is the contract between the stored configuration and the
// catalogue: which stored destination kinds exist, which node and input each
// one reaches, which columns hold them, which configuration objects are
// nodes and which are deliberately not. The graph builder reads it, and the
// guards in the tests fail when the database, the OpenAPI document or the
// catalogue drift from it, so a feature merged without canvas support fails
// CI instead of being found later.

// Reach is the node input a stored destination kind leads to.
type Reach struct{ Node, Port string }

// StoredKinds maps every stored destination kind (the values of the
// *_kind CHECK constraints and the matching OpenAPI enums) to the input it
// reaches. Kinds that end the call (NoDestination) are not here.
var StoredKinds = map[string]Reach{
	"extension":       {KindExtension, "ring"},
	"voicemail":       {KindExtension, "voicemail"},
	"voicemail_group": {KindVMGroup, "call"},
	"ring_group":      {KindRingGroup, "ring"},
	"ivr":             {KindIVR, "call"},
	"queue":           {KindQueue, "call"},
	"announcement":    {KindAnnouncement, "call"},
	"paging":          {KindPagingGroup, "call"},
	"conference":      {KindConference, "call"},
	"time_condition":  {KindTimeCond, "call"},
	"external":        {KindExternal, "call"},
	"sip_uri":         {KindSIPURI, "call"},
	"internal":        {KindInternalDID, "call"},
}

// NoDestination lists the stored values that mean "no edge": the call ends
// or the field is unset.
var NoDestination = map[string]bool{"hangup": true, "none": true, "": true}

// KindField is one column that stores a destination kind, with the output
// port its edge is drawn from. Constraint is the CHECK constraint's name.
type KindField struct {
	Table, Constraint string
	Node, Port        string
}

// KindFields lists every column that stores a destination kind. A new one
// (a new object with a "go to" field) must be added here, which makes
// TestCatalogueDrift check it; a CHECK constraint that lists destination
// kinds and is missing here fails that test.
var KindFields = []KindField{
	{"inbound_routes", "inbound_routes_destination_kind_check", KindInboundRoute, "destination"},
	{"ring_groups", "ring_groups_failure_kind_check", KindRingGroup, "noAnswer"},
	{"ivr_menus", "ivr_menus_timeout_kind_check", KindIVR, "timeout"},
	{"ivr_menus", "ivr_menus_invalid_kind_check", KindIVR, "invalid"},
	{"ivr_menu_options", "ivr_menu_options_dest_kind_check", KindIVR, "key"},
	{"time_conditions", "time_conditions_open_kind_check", KindTimeCond, "open"},
	{"time_conditions", "time_conditions_closed_kind_check", KindTimeCond, "closed"},
	{"time_conditions", "time_conditions_holiday_kind_check", KindTimeCond, "holiday"},
	{"queues", "queues_timeout_kind_check", KindQueue, "timeout"},
	{"queues", "queues_full_kind_check", KindQueue, "full"},
	{"queues", "queues_no_agents_kind_check", KindQueue, "noAgents"},
}

// FeatureCodeTargets maps the feature-code actions whose argument names a
// call target to the node kind it names; the canvas draws them as feature
// code nodes with a "target" edge.
var FeatureCodeTargets = map[string]string{
	"announcement":   "announcement",
	"ivr":            "ivr",
	"time_condition": "time_condition",
}

// FeatureCodeNoTarget lists the other feature-code actions, each with why it
// has no edge: it changes a setting, or its argument is not a call target.
var FeatureCodeNoTarget = map[string]string{
	"forward_always":      "sets a forwarding number on the caller's own extension",
	"forward_busy":        "sets a forwarding number on the caller's own extension",
	"forward_no_answer":   "sets a forwarding number on the caller's own extension",
	"dnd_on":              "setting on the caller's own extension",
	"dnd_off":             "setting on the caller's own extension",
	"call_waiting_on":     "setting on the caller's own extension",
	"call_waiting_off":    "setting on the caller's own extension",
	"voicemail":           "opens the caller's own voicemail box, or the box of the extension dialled after the code",
	"blind_transfer":      "a transfer key, the target is dialled during the call",
	"attended_transfer":   "a transfer key, the target is dialled during the call",
	"page":                "the target is the paging group's own code, drawn on the paging group",
	"intercom":            "the target extension is dialled after the code",
	"park":                "parks into a slot of a park lot chosen at call time",
	"park_retrieve":       "the slot is dialled after the code",
	"pickup_group":        "picks up in the caller's own pickup group",
	"pickup_directed":     "the extension is dialled after the code",
	"tc_override":         "toggles a time condition chosen by the digits dialled after the code",
	"queue_login":         "acts on the caller's own agent state",
	"queue_logout":        "acts on the caller's own agent state",
	"queue_pause":         "acts on the caller's own agent state; the argument is a pause reason",
	"queue_unpause":       "acts on the caller's own agent state",
	"voicemail_broadcast": "records a message for the voicemail group whose number is dialled after the code",
}

// ResourceNodes maps each configuration object with create, read, update and
// delete routes in the OpenAPI document (the collection path below /api/v1)
// to the node kind that draws it.
var ResourceNodes = map[string]string{
	"trunks":             KindTrunk,
	"routes/inbound":     KindInboundRoute,
	"routes/outbound":    KindOutboundRoute,
	"extensions":         KindExtension,
	"devices":            KindDevice,
	"phones":             KindPhone,
	"ring-groups":        KindRingGroup,
	"ivr-menus":          KindIVR,
	"queues":             KindQueue,
	"announcements":      KindAnnouncement,
	"paging-groups":      KindPagingGroup,
	"multicast-channels": KindMulticast,
	"conferences":        KindConference,
	"park-lots":          KindParkLot,
	"pickup-groups":      KindPickupGroup,
	"voicemail-groups":   KindVMGroup,
	"time-conditions":    KindTimeCond,
	"schedules":          KindSchedule,
	"calendars":          KindHolidayCal,
	"moh-classes":        KindMOHClass,
}

// SingletonNodes are node kinds drawn from configuration that has no
// collection route of its own, with where it is stored.
var SingletonNodes = map[string]string{
	KindFeatureCode: "the feature code table, replaced as a whole by PUT /api/v1/feature-codes",
}

// NonFlowResources lists the objects with CRUD routes that are not part of a
// call's path, each with why. An object not here and not in ResourceNodes
// fails TestResourceCoverage: decide whether it is a node (add it to the
// catalogue, the builder, the resolver and ResourceNodes) or not (add it
// here with the reason).
var NonFlowResources = map[string]string{
	"tokens":                        "API credentials, not call routing",
	"users":                         "console logins, not call routing",
	"service-accounts":              "API credentials, not call routing",
	"service-accounts/{id}/secrets": "API credentials, not call routing",
	"prov/templates":                "phone provisioning templates, not call routing",
	"prov/firmware":                 "phone firmware files, not call routing",
	"phone-groups":                  "provisioning groups of phones (templates, multicast listening, PTT), not call routing",
	"phonebooks":                    "directory data shown on phones, not call routing",
	"phonebooks/{id}/entries":       "directory data shown on phones, not call routing",
	"me/contacts":                   "a user's personal contacts, not call routing",
	"ai/sessions":                   "assistant conversations, not call routing",
	"flow/canvases":                 "named views of the canvas that store only node positions, not call routing",
}

// NotDestinationEnums lists the OpenAPI enums that look like destination
// kinds (they share values with them) but are not stored call destinations,
// each with why. The key is the schema path of the enum. Anything else that
// overlaps fails TestCatalogueDrift until it is classified.
var NotDestinationEnums = map[string]string{
	"/components/schemas/MessageSend/properties/targetKind": "who a text message is addressed to, not where a call goes",
}

// NotDestinationConstraints is NotDestinationEnums for database CHECK
// constraints, keyed by constraint name.
var NotDestinationConstraints = map[string]string{}
