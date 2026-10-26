package api

import (
	"net/http"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// route is one /api/v1 operation: its pattern, the scope a credential must
// hold and the minimum role its actor must have (spec S-1, S-5, S-23).
// Public routes need neither.
type route struct {
	Method, Pattern string
	Scope           auth.Scope
	Role            auth.Role
	Public          bool
	H               http.HandlerFunc
}

// RouteInfo is a route without its handler, for tests outside the package
// (the OpenAPI sync test, the scope and role enforcement tests).
type RouteInfo struct {
	Method, Pattern string
	Scope           auth.Scope
	Role            auth.Role
	Public          bool
}

// Routes lists every /api/v1 route Handler registers.
func Routes() []RouteInfo {
	rs := (&server{}).routes()
	out := make([]RouteInfo, len(rs))
	for i, r := range rs {
		out[i] = RouteInfo{Method: r.Method, Pattern: r.Pattern, Scope: r.Scope, Role: r.Role, Public: r.Public}
	}
	return out
}

// OpenAPI returns the embedded OpenAPI document.
func OpenAPI() []byte { return openAPI }

// routes is the single route table Handler registers from. The minimum
// role follows the scope: read and session → viewer, write → operator,
// admin and secrets → admin.
func (s *server) routes() []route {
	return []route{
		{"GET", "/api/v1/version", "", "", true, s.version},
		{"GET", "/api/v1/openapi.json", "", "", true, s.openAPI},
		{"POST", "/api/v1/auth/login", "", "", true, s.login},
		{"POST", "/api/v1/auth/logout", auth.ScopeRead, auth.RoleUser, false, s.logout},
		{"GET", "/api/v1/auth/me", auth.ScopeRead, auth.RoleUser, false, s.me},
		{"POST", "/api/v1/auth/password", auth.ScopeSession, auth.RoleUser, false, s.changePassword},

		{"GET", "/api/v1/tokens", auth.ScopeAdmin, auth.RoleAdmin, false, s.listTokens},
		{"POST", "/api/v1/tokens", auth.ScopeAdmin, auth.RoleAdmin, false, s.createToken},
		{"DELETE", "/api/v1/tokens/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteToken},

		{"GET", "/api/v1/extensions", auth.ScopeRead, auth.RoleViewer, false, s.listExtensions},
		{"POST", "/api/v1/extensions", auth.ScopeWrite, auth.RoleOperator, false, s.createExtension},
		{"GET", "/api/v1/extensions/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getExtension},
		{"PATCH", "/api/v1/extensions/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateExtension},
		{"DELETE", "/api/v1/extensions/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteExtension},

		{"GET", "/api/v1/devices", auth.ScopeRead, auth.RoleViewer, false, s.listDevices},
		{"POST", "/api/v1/devices", auth.ScopeWrite, auth.RoleOperator, false, s.createDevice},
		{"GET", "/api/v1/devices/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getDevice},
		{"PATCH", "/api/v1/devices/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateDevice},
		{"DELETE", "/api/v1/devices/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteDevice},
		{"POST", "/api/v1/devices/{id}/rotate-secret", auth.ScopeSecrets, auth.RoleAdmin, false, s.rotateSecret},

		{"GET", "/api/v1/extensions/{id}/voicemail", auth.ScopeRead, auth.RoleViewer, false, s.getVoicemailBox},
		{"PUT", "/api/v1/extensions/{id}/voicemail", auth.ScopeWrite, auth.RoleOperator, false, s.putVoicemailBox},
		{"PUT", "/api/v1/extensions/{id}/voicemail/greeting", auth.ScopeWrite, auth.RoleOperator, false, s.putVoicemailGreeting},
		{"POST", "/api/v1/extensions/{id}/voicemail/greeting/preview", auth.ScopeWrite, auth.RoleOperator, false, s.previewVoicemailGreeting},
		{"DELETE", "/api/v1/extensions/{id}/voicemail/greeting/{slot}", auth.ScopeWrite, auth.RoleOperator, false, s.revertVoicemailGreeting},

		{"GET", "/api/v1/voicemail/boxes", auth.ScopeRead, auth.RoleViewer, false, s.listVoicemailBoxes},
		{"GET", "/api/v1/voicemail/messages", auth.ScopeRead, auth.RoleViewer, false, s.listVoicemailMessages},
		{"POST", "/api/v1/voicemail/messages/{id}/heard", auth.ScopeWrite, auth.RoleOperator, false, s.markMessageHeard},
		{"DELETE", "/api/v1/voicemail/messages/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteMessage},
		{"GET", "/api/v1/voicemail/messages/{id}/audio", auth.ScopeRead, auth.RoleViewer, false, s.messageAudio},

		{"GET", "/api/v1/recordings", auth.ScopeRead, auth.RoleViewer, false, s.listRecordings},
		{"DELETE", "/api/v1/recordings/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteRecording},
		{"GET", "/api/v1/recordings/{id}/audio", auth.ScopeRead, auth.RoleViewer, false, s.recordingAudio},

		{"GET", "/api/v1/announcements", auth.ScopeRead, auth.RoleViewer, false, s.listAnnouncements},
		{"POST", "/api/v1/announcements", auth.ScopeWrite, auth.RoleOperator, false, s.createAnnouncement},
		{"PUT", "/api/v1/announcements/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.replaceAnnouncement},
		{"DELETE", "/api/v1/announcements/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteAnnouncement},
		{"GET", "/api/v1/announcements/{id}/audio", auth.ScopeRead, auth.RoleViewer, false, s.announcementAudio},
		{"POST", "/api/v1/announcements/generate", auth.ScopeWrite, auth.RoleOperator, false, s.createGeneratedAnnouncement},
		{"PATCH", "/api/v1/announcements/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.saveAnnouncementPrompt},
		{"POST", "/api/v1/announcements/{id}/generate", auth.ScopeWrite, auth.RoleOperator, false, s.generateAnnouncement},

		{"GET", "/api/v1/tts/settings", auth.ScopeAdmin, auth.RoleAdmin, false, s.ttsSettings},
		{"PUT", "/api/v1/tts/settings", auth.ScopeAdmin, auth.RoleAdmin, false, s.putTTSSettings},
		{"DELETE", "/api/v1/tts/settings/key", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteTTSKey},
		{"POST", "/api/v1/tts/settings/test", auth.ScopeAdmin, auth.RoleAdmin, false, s.testTTS},
		{"GET", "/api/v1/tts/voices", auth.ScopeWrite, auth.RoleOperator, false, s.ttsVoices},

		{"GET", "/api/v1/ring-groups", auth.ScopeRead, auth.RoleViewer, false, s.listRingGroups},
		{"POST", "/api/v1/ring-groups", auth.ScopeWrite, auth.RoleOperator, false, s.createRingGroup},
		{"GET", "/api/v1/ring-groups/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getRingGroup},
		{"PATCH", "/api/v1/ring-groups/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateRingGroup},
		{"DELETE", "/api/v1/ring-groups/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteRingGroup},

		{"GET", "/api/v1/ivr-menus", auth.ScopeRead, auth.RoleViewer, false, s.listIVRMenus},
		{"POST", "/api/v1/ivr-menus", auth.ScopeWrite, auth.RoleOperator, false, s.createIVRMenu},
		{"GET", "/api/v1/ivr-menus/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getIVRMenu},
		{"PUT", "/api/v1/ivr-menus/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putIVRMenu},
		{"DELETE", "/api/v1/ivr-menus/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteIVRMenu},
		{"GET", "/api/v1/queues", auth.ScopeRead, auth.RoleViewer, false, s.listQueues},
		{"POST", "/api/v1/queues", auth.ScopeWrite, auth.RoleOperator, false, s.createQueue},
		{"GET", "/api/v1/queues/status", auth.ScopeRead, auth.RoleViewer, false, s.listQueueStatus},
		{"GET", "/api/v1/queues/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getQueue},
		{"PUT", "/api/v1/queues/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putQueue},
		{"DELETE", "/api/v1/queues/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteQueue},
		{"GET", "/api/v1/queue-agents", auth.ScopeRead, auth.RoleViewer, false, s.listQueueAgents},
		{"PUT", "/api/v1/queue-agents/{extension}", auth.ScopeWrite, auth.RoleOperator, false, s.setQueueAgent},
		{"GET", "/api/v1/schedules", auth.ScopeRead, auth.RoleViewer, false, s.listSchedules},
		{"POST", "/api/v1/schedules", auth.ScopeWrite, auth.RoleOperator, false, s.createSchedule},
		{"GET", "/api/v1/schedules/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getSchedule},
		{"PUT", "/api/v1/schedules/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putSchedule},
		{"DELETE", "/api/v1/schedules/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteSchedule},
		{"GET", "/api/v1/calendars", auth.ScopeRead, auth.RoleViewer, false, s.listCalendars},
		{"POST", "/api/v1/calendars", auth.ScopeWrite, auth.RoleOperator, false, s.createCalendar},
		{"GET", "/api/v1/calendars/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getCalendar},
		{"PUT", "/api/v1/calendars/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putCalendar},
		{"DELETE", "/api/v1/calendars/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteCalendar},
		{"GET", "/api/v1/time-conditions", auth.ScopeRead, auth.RoleViewer, false, s.listTimeConditions},
		{"POST", "/api/v1/time-conditions", auth.ScopeWrite, auth.RoleOperator, false, s.createTimeCondition},
		{"GET", "/api/v1/time-conditions/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getTimeCondition},
		{"PUT", "/api/v1/time-conditions/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putTimeCondition},
		{"DELETE", "/api/v1/time-conditions/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteTimeCondition},
		{"POST", "/api/v1/calendars/{id}/import", auth.ScopeWrite, auth.RoleOperator, false, s.importCalendar},
		{"PUT", "/api/v1/time-conditions/{id}/override", auth.ScopeWrite, auth.RoleOperator, false, s.putTimeConditionOverride},
		{"GET", "/api/v1/time-conditions/{id}/evaluate", auth.ScopeRead, auth.RoleViewer, false, s.evaluateTimeCondition},
		{"GET", "/api/v1/time-settings", auth.ScopeRead, auth.RoleViewer, false, s.getTimeSettings},
		{"GET", "/api/v1/moh-classes", auth.ScopeRead, auth.RoleViewer, false, s.listMOHClasses},
		{"POST", "/api/v1/moh-classes", auth.ScopeWrite, auth.RoleOperator, false, s.createMOHClass},
		{"GET", "/api/v1/moh-classes/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getMOHClass},
		{"PUT", "/api/v1/moh-classes/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putMOHClass},
		{"DELETE", "/api/v1/moh-classes/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteMOHClass},
		{"GET", "/api/v1/paging-groups", auth.ScopeRead, auth.RoleViewer, false, s.listPagingGroups},
		{"POST", "/api/v1/paging-groups", auth.ScopeAdmin, auth.RoleAdmin, false, s.createPagingGroup},
		{"GET", "/api/v1/paging-groups/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getPagingGroup},
		{"PUT", "/api/v1/paging-groups/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.putPagingGroup},
		{"DELETE", "/api/v1/paging-groups/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deletePagingGroup},
		{"POST", "/api/v1/paging-groups/{id}/test", auth.ScopeAdmin, auth.RoleAdmin, false, s.testPagingGroup},
		{"GET", "/api/v1/conferences", auth.ScopeRead, auth.RoleViewer, false, s.listConferences},
		{"POST", "/api/v1/conferences", auth.ScopeAdmin, auth.RoleAdmin, false, s.createConference},
		{"GET", "/api/v1/conferences/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getConference},
		{"PUT", "/api/v1/conferences/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.putConference},
		{"DELETE", "/api/v1/conferences/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteConference},
		{"POST", "/api/v1/conferences/{id}/control", auth.ScopeWrite, auth.RoleOperator, false, s.controlConference},
		{"GET", "/api/v1/park-lots", auth.ScopeRead, auth.RoleViewer, false, s.listParkLots},
		{"POST", "/api/v1/park-lots", auth.ScopeWrite, auth.RoleOperator, false, s.createParkLot},
		{"POST", "/api/v1/park-lots/announcement/generate", auth.ScopeWrite, auth.RoleOperator, false, s.generateParkAnnouncement},
		{"GET", "/api/v1/park-lots/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getParkLot},
		{"PUT", "/api/v1/park-lots/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putParkLot},
		{"DELETE", "/api/v1/park-lots/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteParkLot},
		{"GET", "/api/v1/parked-calls", auth.ScopeRead, auth.RoleViewer, false, s.listParkedCalls},
		{"GET", "/api/v1/pickup-groups", auth.ScopeRead, auth.RoleViewer, false, s.listPickupGroups},
		{"POST", "/api/v1/pickup-groups", auth.ScopeWrite, auth.RoleOperator, false, s.createPickupGroup},
		{"GET", "/api/v1/pickup-groups/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getPickupGroup},
		{"PUT", "/api/v1/pickup-groups/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.putPickupGroup},
		{"DELETE", "/api/v1/pickup-groups/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deletePickupGroup},
		{"GET", "/api/v1/multicast-channels", auth.ScopeRead, auth.RoleViewer, false, s.listMulticastChannels},
		{"POST", "/api/v1/multicast-channels", auth.ScopeAdmin, auth.RoleAdmin, false, s.createMulticastChannel},
		{"GET", "/api/v1/multicast-channels/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getMulticastChannel},
		{"PUT", "/api/v1/multicast-channels/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.putMulticastChannel},
		{"DELETE", "/api/v1/multicast-channels/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteMulticastChannel},
		{"GET", "/api/v1/feature-codes", auth.ScopeRead, auth.RoleViewer, false, s.listFeatureCodes},
		{"PUT", "/api/v1/feature-codes", auth.ScopeWrite, auth.RoleOperator, false, s.putFeatureCodes},

		{"GET", "/api/v1/presence", auth.ScopeRead, auth.RoleViewer, false, s.presence},

		{"GET", "/api/v1/registrations", auth.ScopeRead, auth.RoleViewer, false, s.registrations},
		{"GET", "/api/v1/calls", auth.ScopeRead, auth.RoleViewer, false, s.calls},
		{"GET", "/api/v1/cdrs", auth.ScopeRead, auth.RoleViewer, false, s.cdrs},
		{"GET", "/api/v1/cdrs/counts", auth.ScopeRead, auth.RoleViewer, false, s.cdrCounts},
		{"GET", "/api/v1/cdrs/concurrency", auth.ScopeRead, auth.RoleViewer, false, s.cdrConcurrency},
		{"GET", "/api/v1/cdrs/export", auth.ScopeRead, auth.RoleViewer, false, s.cdrExport},
		{"GET", "/api/v1/cdrs/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getCDR},

		{"GET", "/api/v1/trunks", auth.ScopeRead, auth.RoleViewer, false, s.listTrunks},
		{"POST", "/api/v1/trunks", auth.ScopeWrite, auth.RoleOperator, false, s.createTrunk},
		{"GET", "/api/v1/trunks/status", auth.ScopeRead, auth.RoleViewer, false, s.trunkStatus},
		{"GET", "/api/v1/trunks/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getTrunk},
		{"PATCH", "/api/v1/trunks/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateTrunk},
		{"DELETE", "/api/v1/trunks/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteTrunk},

		{"GET", "/api/v1/routes/outbound", auth.ScopeRead, auth.RoleViewer, false, s.listOutbound},
		{"POST", "/api/v1/routes/outbound", auth.ScopeWrite, auth.RoleOperator, false, s.createOutbound},
		{"PUT", "/api/v1/routes/outbound/order", auth.ScopeWrite, auth.RoleOperator, false, s.reorder(store.Outbound)},
		{"GET", "/api/v1/routes/outbound/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getOutbound},
		{"PATCH", "/api/v1/routes/outbound/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateOutbound},
		{"DELETE", "/api/v1/routes/outbound/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteOutbound},

		{"GET", "/api/v1/routes/inbound", auth.ScopeRead, auth.RoleViewer, false, s.listInbound},
		{"POST", "/api/v1/routes/inbound", auth.ScopeWrite, auth.RoleOperator, false, s.createInbound},
		{"PUT", "/api/v1/routes/inbound/order", auth.ScopeWrite, auth.RoleOperator, false, s.reorder(store.Inbound)},
		{"GET", "/api/v1/routes/inbound/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getInbound},
		{"PATCH", "/api/v1/routes/inbound/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateInbound},
		{"DELETE", "/api/v1/routes/inbound/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteInbound},

		{"GET", "/api/v1/flow/catalogue", auth.ScopeRead, auth.RoleViewer, false, s.getFlowCatalogue},
		{"GET", "/api/v1/flow/graph", auth.ScopeRead, auth.RoleViewer, false, s.getFlowGraph},
		{"GET", "/api/v1/flow/revision", auth.ScopeRead, auth.RoleViewer, false, s.getFlowRevision},

		{"POST", "/api/v1/routing/test", auth.ScopeWrite, auth.RoleOperator, false, s.routingTest},

		{"GET", "/api/v1/cluster", auth.ScopeRead, auth.RoleViewer, false, s.clusterOverview},
		{"GET", "/api/v1/cluster/nodes", auth.ScopeRead, auth.RoleViewer, false, s.clusterNodes},
		{"POST", "/api/v1/cluster/nodes/{id}/drain", auth.ScopeAdmin, auth.RoleAdmin, false, s.requestDrain},
		{"DELETE", "/api/v1/cluster/nodes/{id}/drain", auth.ScopeAdmin, auth.RoleAdmin, false, s.cancelDrain},

		{"GET", "/api/v1/phones", auth.ScopeRead, auth.RoleViewer, false, s.listPhones},
		{"POST", "/api/v1/phones", auth.ScopeWrite, auth.RoleOperator, false, s.createPhone},
		{"POST", "/api/v1/phones/import", auth.ScopeWrite, auth.RoleOperator, false, s.importPhones},
		{"GET", "/api/v1/phones/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getPhone},
		{"PATCH", "/api/v1/phones/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updatePhone},
		{"DELETE", "/api/v1/phones/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deletePhone},
		{"POST", "/api/v1/phones/{id}/rotate-token", auth.ScopeSecrets, auth.RoleAdmin, false, s.rotatePhoneToken},
		{"POST", "/api/v1/phones/{id}/rearm", auth.ScopeSecrets, auth.RoleAdmin, false, s.rearmPhone},
		{"POST", "/api/v1/phones/{id}/admin-password/reveal", auth.ScopeSecrets, auth.RoleAdmin, false, s.revealAdminPassword},
		{"POST", "/api/v1/phones/{id}/admin-password/rotate", auth.ScopeSecrets, auth.RoleAdmin, false, s.rotateAdminPassword},
		{"GET", "/api/v1/phones/{id}/fetches", auth.ScopeRead, auth.RoleViewer, false, s.phoneFetches},
		{"POST", "/api/v1/phones/resync", auth.ScopeWrite, auth.RoleOperator, false, s.pushBulk(store.PushResync, false)},
		{"POST", "/api/v1/phones/reboot", auth.ScopeWrite, auth.RoleOperator, false, s.pushBulk(store.PushReboot, false)},
		{"POST", "/api/v1/phones/{id}/resync", auth.ScopeWrite, auth.RoleOperator, false, s.pushPhone(store.PushResync)},
		{"POST", "/api/v1/phones/{id}/reboot", auth.ScopeWrite, auth.RoleOperator, false, s.pushPhone(store.PushReboot)},
		{"GET", "/api/v1/phones/{id}/pushes", auth.ScopeRead, auth.RoleViewer, false, s.listPhonePushes},
		{"GET", "/api/v1/phone-pushes", auth.ScopeRead, auth.RoleViewer, false, s.latestPhonePushes},
		{"GET", "/api/v1/phones/{id}/preview", auth.ScopeRead, auth.RoleViewer, false, s.previewPhone},

		{"GET", "/api/v1/onboarding/candidates", auth.ScopeWrite, auth.RoleOperator, false, s.listOnboarding},
		{"POST", "/api/v1/onboarding/candidates/{mac}/adopt", auth.ScopeWrite, auth.RoleOperator, false, s.adoptCandidate},
		{"POST", "/api/v1/onboarding/candidates/{mac}/dismiss", auth.ScopeWrite, auth.RoleOperator, false, s.dismissCandidate},
		{"POST", "/api/v1/onboarding/adopt", auth.ScopeWrite, auth.RoleOperator, false, s.bulkAdopt},
		{"GET", "/api/v1/onboarding/ignored", auth.ScopeWrite, auth.RoleOperator, false, s.listIgnoredMACs},
		{"DELETE", "/api/v1/onboarding/ignored/{mac}", auth.ScopeWrite, auth.RoleOperator, false, s.unignoreMAC},

		{"GET", "/api/v1/phonebooks", auth.ScopeRead, auth.RoleViewer, false, s.listPhonebooks},
		{"GET", "/api/v1/phonebooks/lookup", auth.ScopeAdmin, auth.RoleAdmin, false, s.lookupPhonebook},
		{"POST", "/api/v1/phonebooks", auth.ScopeAdmin, auth.RoleAdmin, false, s.createPhonebook},
		{"GET", "/api/v1/phonebooks/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getPhonebook},
		{"PUT", "/api/v1/phonebooks/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.updatePhonebook},
		{"DELETE", "/api/v1/phonebooks/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deletePhonebook},
		{"GET", "/api/v1/phonebooks/{id}/entries", auth.ScopeRead, auth.RoleViewer, false, s.listPhonebookEntries},
		{"POST", "/api/v1/phonebooks/{id}/entries", auth.ScopeWrite, auth.RoleOperator, false, s.createPhonebookEntry},
		{"PUT", "/api/v1/phonebooks/{id}/entries/{entryId}", auth.ScopeWrite, auth.RoleOperator, false, s.updatePhonebookEntry},
		{"DELETE", "/api/v1/phonebooks/{id}/entries/{entryId}", auth.ScopeWrite, auth.RoleOperator, false, s.deletePhonebookEntry},
		{"POST", "/api/v1/phonebooks/{id}/import", auth.ScopeWrite, auth.RoleOperator, false, s.importPhonebook},
		{"GET", "/api/v1/phonebooks/{id}/export", auth.ScopeWrite, auth.RoleOperator, false, s.exportPhonebook},

		{"GET", "/api/v1/me/contacts", auth.ScopeRead, auth.RoleUser, false, s.listContacts},
		{"POST", "/api/v1/me/contacts", auth.ScopeWrite, auth.RoleUser, false, s.createContact},
		{"POST", "/api/v1/me/contacts/import", auth.ScopeWrite, auth.RoleUser, false, s.importContacts},
		{"GET", "/api/v1/me/contacts/export", auth.ScopeRead, auth.RoleUser, false, s.exportContacts},
		{"PUT", "/api/v1/me/contacts/{id}", auth.ScopeWrite, auth.RoleUser, false, s.updateContact},
		{"DELETE", "/api/v1/me/contacts/{id}", auth.ScopeWrite, auth.RoleUser, false, s.deleteContact},

		// Extension self-service (role user): only the caller's own
		// extensions, never an id of someone else's.
		{"PATCH", "/api/v1/me/profile", auth.ScopeWrite, auth.RoleUser, false, s.updateProfile},
		{"GET", "/api/v1/me/extensions", auth.ScopeRead, auth.RoleUser, false, s.listMyExtensions},
		{"PATCH", "/api/v1/me/extensions/{id}", auth.ScopeWrite, auth.RoleUser, false, s.updateMyExtension},
		{"GET", "/api/v1/me/extensions/{id}/voicemail", auth.ScopeRead, auth.RoleUser, false, s.getMyVoicemail},
		{"PUT", "/api/v1/me/extensions/{id}/voicemail", auth.ScopeWrite, auth.RoleUser, false, s.putMyVoicemail},
		{"GET", "/api/v1/me/extensions/{id}/voicemail/messages", auth.ScopeRead, auth.RoleUser, false, s.listMyMessages},
		{"POST", "/api/v1/me/voicemail/messages/{id}/heard", auth.ScopeWrite, auth.RoleUser, false, s.markMyMessageHeard},
		{"DELETE", "/api/v1/me/voicemail/messages/{id}", auth.ScopeWrite, auth.RoleUser, false, s.deleteMyMessage},
		{"GET", "/api/v1/me/voicemail/messages/{id}/audio", auth.ScopeRead, auth.RoleUser, false, s.myMessageAudio},
		{"GET", "/api/v1/me/calls", auth.ScopeRead, auth.RoleUser, false, s.listMyCalls},

		{"GET", "/api/v1/settings/smtp", auth.ScopeAdmin, auth.RoleAdmin, false, s.getSMTP},
		{"PUT", "/api/v1/settings/smtp", auth.ScopeAdmin, auth.RoleAdmin, false, s.putSMTP},
		{"POST", "/api/v1/settings/smtp/test", auth.ScopeAdmin, auth.RoleAdmin, false, s.testSMTP},

		{"GET", "/api/v1/messages", auth.ScopeAdmin, auth.RoleAdmin, false, s.listMessages},
		{"POST", "/api/v1/messages", auth.ScopeWrite, auth.RoleOperator, false, s.sendMessage},
		{"GET", "/api/v1/messages/conversations", auth.ScopeAdmin, auth.RoleAdmin, false, s.listMessageConversations},
		{"GET", "/api/v1/messages/settings", auth.ScopeAdmin, auth.RoleAdmin, false, s.getMessageSettings},
		{"PUT", "/api/v1/messages/settings", auth.ScopeAdmin, auth.RoleAdmin, false, s.putMessageSettings},
		{"POST", "/api/v1/messages/purge", auth.ScopeAdmin, auth.RoleAdmin, false, s.purgeMessages},
		{"GET", "/api/v1/me/messages", auth.ScopeRead, auth.RoleUser, false, s.listMyTexts},
		{"POST", "/api/v1/me/messages", auth.ScopeWrite, auth.RoleUser, false, s.sendMyText},
		{"GET", "/api/v1/me/messages/conversations", auth.ScopeRead, auth.RoleUser, false, s.listMyTextConversations},

		{"GET", "/api/v1/phone-groups", auth.ScopeRead, auth.RoleViewer, false, s.listPhoneGroups},
		{"POST", "/api/v1/phone-groups", auth.ScopeAdmin, auth.RoleAdmin, false, s.createPhoneGroup},
		{"GET", "/api/v1/phone-groups/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getPhoneGroup},
		{"PUT", "/api/v1/phone-groups/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.updatePhoneGroup},
		{"DELETE", "/api/v1/phone-groups/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deletePhoneGroup},
		{"POST", "/api/v1/phone-groups/{id}/resync", auth.ScopeWrite, auth.RoleOperator, false, s.pushBulk(store.PushResync, true)},
		{"POST", "/api/v1/phone-groups/{id}/reboot", auth.ScopeWrite, auth.RoleOperator, false, s.pushBulk(store.PushReboot, true)},
		{"PUT", "/api/v1/phone-groups/{id}/books", auth.ScopeAdmin, auth.RoleAdmin, false, s.putPhoneGroupBooks},
		{"PUT", "/api/v1/phone-groups/{id}/multicast", auth.ScopeAdmin, auth.RoleAdmin, false, s.putPhoneGroupMulticast},
		{"PUT", "/api/v1/phone-groups/{id}/ptt", auth.ScopeAdmin, auth.RoleAdmin, false, s.putPhoneGroupPTT},

		{"GET", "/api/v1/prov/templates", auth.ScopeRead, auth.RoleViewer, false, s.listTemplates},
		{"POST", "/api/v1/prov/templates", auth.ScopeWrite, auth.RoleOperator, false, s.createTemplate},
		{"POST", "/api/v1/prov/templates/validate", auth.ScopeWrite, auth.RoleOperator, false, s.validateTemplateRoute},
		{"GET", "/api/v1/prov/templates/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getTemplate},
		{"PATCH", "/api/v1/prov/templates/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateTemplate},
		{"DELETE", "/api/v1/prov/templates/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteTemplate},
		{"POST", "/api/v1/prov/templates/{id}/copy", auth.ScopeWrite, auth.RoleOperator, false, s.copyTemplate},

		{"GET", "/api/v1/prov/firmware", auth.ScopeRead, auth.RoleViewer, false, s.listFirmware},
		{"POST", "/api/v1/prov/firmware", auth.ScopeWrite, auth.RoleOperator, false, s.uploadFirmware},
		{"PUT", "/api/v1/prov/firmware/pins", auth.ScopeWrite, auth.RoleOperator, false, s.putFirmwarePins},
		{"DELETE", "/api/v1/prov/firmware/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteFirmware},

		{"GET", "/api/v1/prov/redirect", auth.ScopeAdmin, auth.RoleAdmin, false, s.listRedirect},
		{"PUT", "/api/v1/prov/redirect/{vendor}", auth.ScopeAdmin, auth.RoleAdmin, false, s.putRedirect},
		{"DELETE", "/api/v1/prov/redirect/{vendor}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteRedirect},
		{"POST", "/api/v1/prov/redirect/{vendor}/check", auth.ScopeAdmin, auth.RoleAdmin, false, s.postRedirectCheck},

		{"GET", "/api/v1/prov/settings", auth.ScopeRead, auth.RoleViewer, false, s.provSettings},

		{"GET", "/api/v1/diagnostics/devices/{id}", auth.ScopeRead, auth.RoleViewer, false, s.deviceDiagnostics},
		{"GET", "/api/v1/diagnostics/auth-failures", auth.ScopeRead, auth.RoleViewer, false, s.listAuthFailures},
		{"DELETE", "/api/v1/diagnostics/auth-failures/{ip}", auth.ScopeWrite, auth.RoleOperator, false, s.clearAuthFailures},

		// External AI access (spec ai-external-access). The rows are fixed
		// here; each stream replaces s.pending with its handler.
		{"GET", "/api/v1/ai/settings", auth.ScopeRead, auth.RoleViewer, false, s.aiSettings},
		{"GET", "/api/v1/oauth/requests/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getOAuthRequest},
		{"POST", "/api/v1/oauth/requests/{id}/approve", auth.ScopeSession, auth.RoleViewer, false, s.approveOAuthRequest},
		{"POST", "/api/v1/oauth/requests/{id}/deny", auth.ScopeSession, auth.RoleViewer, false, s.denyOAuthRequest},
		{"GET", "/api/v1/oauth/grants", auth.ScopeRead, auth.RoleViewer, false, s.listGrants},
		{"DELETE", "/api/v1/oauth/grants/{id}", auth.ScopeSession, auth.RoleViewer, false, s.deleteGrant},
		{"GET", "/api/v1/service-accounts", auth.ScopeAdmin, auth.RoleAdmin, false, s.listServiceAccounts},
		{"POST", "/api/v1/service-accounts", auth.ScopeAdmin, auth.RoleAdmin, false, s.createServiceAccount},
		{"GET", "/api/v1/service-accounts/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.getServiceAccount},
		{"PATCH", "/api/v1/service-accounts/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.updateServiceAccount},
		{"DELETE", "/api/v1/service-accounts/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteServiceAccount},
		{"POST", "/api/v1/service-accounts/{id}/secrets", auth.ScopeAdmin, auth.RoleAdmin, false, s.createClientSecret},
		{"DELETE", "/api/v1/service-accounts/{id}/secrets/{secretId}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteClientSecret},
		{"GET", "/api/v1/skills", auth.ScopeRead, auth.RoleViewer, false, s.listSkills},
		{"GET", "/api/v1/skills/{name}/download", auth.ScopeRead, auth.RoleViewer, false, s.downloadSkill},
		{"GET", "/api/v1/users", auth.ScopeAdmin, auth.RoleAdmin, false, s.listUsers},
		{"POST", "/api/v1/users", auth.ScopeAdmin, auth.RoleAdmin, false, s.createUser},
		{"GET", "/api/v1/users/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.getUser},
		{"PATCH", "/api/v1/users/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.updateUser},
		{"DELETE", "/api/v1/users/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.deleteUser},

		// The in-product AI agent (spec ai-agent). The rows are fixed here;
		// each stream replaces s.pending with its handler.
		{"GET", "/api/v1/ai/status", auth.ScopeRead, auth.RoleViewer, false, s.getAIStatus},
		{"GET", "/api/v1/ai/findings", auth.ScopeRead, auth.RoleViewer, false, s.listAIFindings},
		{"GET", "/api/v1/ai/findings/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getAIFinding},
		{"POST", "/api/v1/ai/findings/{id}/acknowledge", auth.ScopeWrite, auth.RoleOperator, false, s.acknowledgeAIFinding},
		{"POST", "/api/v1/ai/findings/{id}/dismiss", auth.ScopeWrite, auth.RoleOperator, false, s.dismissAIFinding},
		{"GET", "/api/v1/ai/proposals", auth.ScopeRead, auth.RoleViewer, false, s.listAIProposals},
		{"GET", "/api/v1/ai/proposals/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getAIProposal},
		{"POST", "/api/v1/ai/proposals/{id}/apply", auth.ScopeWrite, auth.RoleOperator, false, s.applyAIProposal},
		{"POST", "/api/v1/ai/proposals/{id}/dismiss", auth.ScopeWrite, auth.RoleOperator, false, s.dismissAIProposal},
		{"GET", "/api/v1/ai/sessions", auth.ScopeRead, auth.RoleViewer, false, s.listAISessions},
		{"POST", "/api/v1/ai/sessions", auth.ScopeWrite, auth.RoleOperator, false, s.createAISession},
		{"GET", "/api/v1/ai/sessions/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getAISession},
		{"PATCH", "/api/v1/ai/sessions/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateAISession},
		{"DELETE", "/api/v1/ai/sessions/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteAISession},
		{"POST", "/api/v1/ai/sessions/{id}/messages", auth.ScopeWrite, auth.RoleOperator, false, s.postAIMessage},
		{"GET", "/api/v1/ai/tasks/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getAITask},
		{"GET", "/api/v1/ai/agents", auth.ScopeRead, auth.RoleViewer, false, s.listAIAgents},
		{"POST", "/api/v1/ai/agents/{name}/run", auth.ScopeWrite, auth.RoleOperator, false, s.runAIAgent}}
}

func (s *server) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPI)
}

// pending answers a route whose handler has not landed yet.
func (s *server) pending(w http.ResponseWriter, _ *http.Request) {
	auth.WriteError(w, http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
