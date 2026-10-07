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
		{"POST", "/api/v1/auth/logout", auth.ScopeRead, auth.RoleViewer, false, s.logout},
		{"GET", "/api/v1/auth/me", auth.ScopeRead, auth.RoleViewer, false, s.me},

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

		{"GET", "/api/v1/ring-groups", auth.ScopeRead, auth.RoleViewer, false, s.listRingGroups},
		{"POST", "/api/v1/ring-groups", auth.ScopeWrite, auth.RoleOperator, false, s.createRingGroup},
		{"GET", "/api/v1/ring-groups/{id}", auth.ScopeRead, auth.RoleViewer, false, s.getRingGroup},
		{"PATCH", "/api/v1/ring-groups/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.updateRingGroup},
		{"DELETE", "/api/v1/ring-groups/{id}", auth.ScopeWrite, auth.RoleOperator, false, s.deleteRingGroup},

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
		{"GET", "/api/v1/phones/{id}/preview", auth.ScopeRead, auth.RoleViewer, false, s.previewPhone},

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
		{"GET", "/api/v1/ai/settings", auth.ScopeRead, auth.RoleViewer, false, s.pending},
		{"GET", "/api/v1/oauth/requests/{id}", auth.ScopeRead, auth.RoleViewer, false, s.pending},
		{"POST", "/api/v1/oauth/requests/{id}/approve", auth.ScopeSession, auth.RoleViewer, false, s.pending},
		{"POST", "/api/v1/oauth/requests/{id}/deny", auth.ScopeSession, auth.RoleViewer, false, s.pending},
		{"GET", "/api/v1/oauth/grants", auth.ScopeRead, auth.RoleViewer, false, s.pending},
		{"DELETE", "/api/v1/oauth/grants/{id}", auth.ScopeSession, auth.RoleViewer, false, s.pending},
		{"GET", "/api/v1/service-accounts", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"POST", "/api/v1/service-accounts", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"GET", "/api/v1/service-accounts/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"PATCH", "/api/v1/service-accounts/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"DELETE", "/api/v1/service-accounts/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"POST", "/api/v1/service-accounts/{id}/secrets", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"DELETE", "/api/v1/service-accounts/{id}/secrets/{secretId}", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"GET", "/api/v1/skills", auth.ScopeRead, auth.RoleViewer, false, s.pending},
		{"GET", "/api/v1/skills/{name}/download", auth.ScopeRead, auth.RoleViewer, false, s.pending},
		{"GET", "/api/v1/users", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
		{"PATCH", "/api/v1/users/{id}", auth.ScopeAdmin, auth.RoleAdmin, false, s.pending},
	}
}

func (s *server) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPI)
}

// pending answers a route whose handler has not landed yet.
func (s *server) pending(w http.ResponseWriter, _ *http.Request) {
	auth.WriteError(w, http.StatusNotImplemented, "not_implemented", "not implemented yet")
}
