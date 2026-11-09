// Package configbatch is the pure part of the configuration batch (spec
// flow-canvas S-13, S-14, S-21): which API operations may be part of a
// batch, how placeholders are filled in from earlier results, and how the
// inverse of an applied batch is built. Running a batch, which needs the
// store and the API handler, is internal/api/config_batch.go.
package configbatch

import (
	"fmt"
	"slices"
	"sort"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/flow"
)

// MaxOps is the most operations one batch may carry (413 beyond it).
const MaxOps = 200

// Info describes a batchable operation: which node it creates, changes or
// deletes, and how its target is read.
type Info struct {
	// Node is the flow node kind of the object the operation is about; ""
	// for an operation on a list (reorder, feature codes).
	Node string
	// Create is a POST that makes the object; its response carries the id.
	Create bool
	// Delete removes the object.
	Delete bool
	// Reorder is a list operation whose body is {"ids": [...]}.
	Reorder bool
	// Read is the GET path of the whole list a list operation changes, read
	// before and after it.
	Read string
}

// IDParam is the path parameter that names the object of an update or
// delete: every batchable object route uses {id}.
const IDParam = "id"

func crud(m map[string]Info, node, create, update, del string) {
	if create != "" {
		m[create] = Info{Node: node, Create: true}
	}
	if update != "" {
		m[update] = Info{Node: node}
	}
	if del != "" {
		m[del] = Info{Node: node, Delete: true}
	}
}

// Ops lists every operation a batch may contain, by operationId. An
// operation that is not here is refused with 400 op_not_batchable:
//
//   - credential operations (rotate and reveal secrets, tokens, users,
//     service accounts, SMTP and TTS settings) and every operation whose
//     response carries an x-hello-secret value (createDevice, createPhone:
//     their response shows a SIP secret or a provisioning URL once);
//   - multipart operations (voicemail greetings, announcement audio,
//     firmware, imports) and every operation that touches MinIO, a speech
//     provider, a phone or a carrier (announcements and their generation,
//     park-lot announcement generation, reboot, resync, test calls);
//   - operations on live state in Valkey (queue agent login, conference
//     control);
//   - operations on objects the canvas does not show (phonebooks, phone
//     groups, provisioning templates, messages, voicemail boxes, users).
//
// TestBatchableOps keeps this list in step with the OpenAPI document.
var Ops = func() map[string]Info {
	m := map[string]Info{}
	crud(m, flow.KindExtension, "createExtension", "updateExtension", "deleteExtension")
	crud(m, flow.KindDevice, "", "updateDevice", "deleteDevice")
	crud(m, flow.KindPhone, "", "updatePhone", "deletePhone")
	crud(m, flow.KindTrunk, "createTrunk", "updateTrunk", "deleteTrunk")
	crud(m, flow.KindRingGroup, "createRingGroup", "updateRingGroup", "deleteRingGroup")
	crud(m, flow.KindInboundRoute, "createInboundRoute", "updateInboundRoute", "deleteInboundRoute")
	crud(m, flow.KindOutboundRoute, "createOutboundRoute", "updateOutboundRoute", "deleteOutboundRoute")
	crud(m, flow.KindIVR, "createIVRMenu", "putIVRMenu", "deleteIVRMenu")
	crud(m, flow.KindTimeCond, "createTimeCondition", "putTimeCondition", "deleteTimeCondition")
	crud(m, flow.KindSchedule, "createSchedule", "putSchedule", "deleteSchedule")
	crud(m, flow.KindHolidayCal, "createCalendar", "putCalendar", "deleteCalendar")
	crud(m, flow.KindMOHClass, "createMOHClass", "putMOHClass", "deleteMOHClass")
	crud(m, flow.KindQueue, "createQueue", "putQueue", "deleteQueue")
	crud(m, flow.KindParkLot, "createParkLot", "putParkLot", "deleteParkLot")
	crud(m, flow.KindPickupGroup, "createPickupGroup", "putPickupGroup", "deletePickupGroup")
	crud(m, flow.KindConference, "createConference", "updateConference", "deleteConference")
	crud(m, flow.KindMulticast, "createMulticastChannel", "putMulticastChannel", "deleteMulticastChannel")
	crud(m, flow.KindPagingGroup, "createPagingGroup", "putPagingGroup", "deletePagingGroup")
	m["reorderInboundRoutes"] = Info{Reorder: true, Read: "/api/v1/routes/inbound"}
	m["reorderOutboundRoutes"] = Info{Reorder: true, Read: "/api/v1/routes/outbound"}
	m["putFeatureCodes"] = Info{Read: "/api/v1/feature-codes"}
	return m
}()

// Refused lists the operations a canvas might expect in a batch but that are
// refused, each with the reason the error message gives.
var Refused = map[string]string{
	"createDevice": "its response carries the device's SIP secret, shown once",
	"createPhone":  "its response carries the phone's provisioning URL, shown once",
}

// Creators and Deleters find, by node kind, the operation that makes and the
// one that removes an object, for the inverse of a batch.
var (
	Creators = byKind(func(i Info) bool { return i.Create })
	Deleters = byKind(func(i Info) bool { return i.Delete })
)

func byKind(want func(Info) bool) map[string]string {
	out := map[string]string{}
	for id, i := range Ops {
		if i.Node != "" && want(i) {
			out[i.Node] = id
		}
	}
	return out
}

// Lookup returns the batchable operation id's table entry and its API
// description, or an error saying why it is not batchable. A batchable
// operation never returns a secret, takes multipart input or needs a
// credential scope (secrets, session).
func Lookup(spec map[string]apispec.Operation, id string) (Info, apispec.Operation, error) {
	if why, ok := Refused[id]; ok {
		return Info{}, apispec.Operation{}, fmt.Errorf("%s cannot be part of a batch: %s", id, why)
	}
	info, ok := Ops[id]
	if !ok {
		return Info{}, apispec.Operation{}, fmt.Errorf("%s cannot be part of a batch", id)
	}
	op, ok := spec[id]
	if !ok {
		return Info{}, apispec.Operation{}, fmt.Errorf("%s is not in the API document", id)
	}
	if err := Batchable(op); err != nil {
		return Info{}, apispec.Operation{}, fmt.Errorf("%s cannot be part of a batch: %w", id, err)
	}
	return info, op, nil
}

// Batchable reports why an operation may not be replayed inside a batch: a
// credential scope (secrets, session), a secret in the response, or a
// request that is not JSON.
func Batchable(op apispec.Operation) error {
	switch {
	case op.Scope != auth.ScopeWrite && op.Scope != auth.ScopeAdmin:
		// An admin-scoped object write (a paging group) is allowed: the
		// operation's own route still demands that scope and role. The
		// secrets and session scopes are credential operations.
		return fmt.Errorf("it needs the %q scope", op.Scope)
	case len(op.Secrets) > 0:
		return fmt.Errorf("its response carries a secret")
	}
	if op.Body != nil {
		for _, ct := range op.Body.ContentTypes {
			if ct != "application/json" {
				return fmt.Errorf("it takes %s input", ct)
			}
		}
	}
	return nil
}

// Sorted returns the batchable operation ids in order, for documentation and
// tests.
func Sorted() []string {
	ids := make([]string, 0, len(Ops))
	for id := range Ops {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return slices.Clone(ids)
}

// NoBatchOps lists the catalogue's stored node kinds that have no object
// operation in a batch, each with the reason. TestBatchCoverage fails when a
// node kind is neither covered by Ops nor listed here, so a feature merged
// with a canvas node but no batch support fails CI.
var NoBatchOps = map[string]string{
	flow.KindAnnouncement: "audio upload and speech generation touch object storage and a speech provider, which do not roll back; announcements are managed on the Announcements page",
	flow.KindFeatureCode:  "the codes are one list, replaced as a whole by putFeatureCodes (a list operation)",
}
