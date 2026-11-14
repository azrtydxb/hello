// Package scim is Hello's SCIM 2.0 service provider (RFC 7643 and 7644; spec
// integrations I-23 to I-25). Identity providers (Entra ID provisioning,
// Okta, JumpCloud, Google) push users and groups to /scim/v2 with a bearer
// token a scim directory connector issued. The pushed resources are kept as
// sent, and every write runs through the directory engine (internal/directory)
// with the connector's mappings, group rules and mode, so a SCIM user becomes
// a Hello user, extension link and ring-group member exactly like a synced
// one. Hello never deletes a user: a SCIM DELETE and active false disable it.
package scim

import (
	"strings"
)

// Schema URNs.
const (
	SchemaUser       = "urn:ietf:params:scim:schemas:core:2.0:User"
	SchemaGroup      = "urn:ietf:params:scim:schemas:core:2.0:Group"
	SchemaEnterprise = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"
	// SchemaPBX is Hello's extension: the extension number of the user (I-24).
	SchemaPBX = "urn:hello:params:scim:schemas:extension:pbx:2.0:User"

	schemaList      = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaPatch     = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaError     = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaSPConfig  = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	schemaResType   = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	schemaSchemaDef = "urn:ietf:params:scim:schemas:core:2.0:Schema"
)

// Resource types.
const (
	TypeUser  = "User"
	TypeGroup = "Group"
)

// Attribute types (RFC 7643 section 2.3).
const (
	tString    = "string"
	tBoolean   = "boolean"
	tComplex   = "complex"
	tReference = "reference"
	tDateTime  = "dateTime"
)

// attr is one attribute definition. The same table serves /Schemas and the
// normalisation of what an identity provider sends: names are matched
// case-insensitively (RFC 7643 section 2.1) and stored in their canonical
// case, values are coerced to the type (the string booleans "True" and
// "False" that Entra sends become booleans), and unknown attributes are
// dropped.
type attr struct {
	Name        string
	Type        string
	Multi       bool
	Sub         []attr
	Mutability  string // readWrite (default), readOnly, writeOnly, immutable
	Returned    string // default (default), always, never, request
	Uniqueness  string // none (default), server
	CaseExact   bool
	Required    bool
	Description string
}

func (a attr) mutability() string {
	if a.Mutability == "" {
		return "readWrite"
	}
	return a.Mutability
}

func (a attr) returned() string {
	if a.Returned == "" {
		return "default"
	}
	return a.Returned
}

func (a attr) uniqueness() string {
	if a.Uniqueness == "" {
		return "none"
	}
	return a.Uniqueness
}

func sub(a []attr, name string) *attr {
	for i := range a {
		if strings.EqualFold(a[i].Name, name) {
			return &a[i]
		}
	}
	return nil
}

func str(name, desc string) attr { return attr{Name: name, Type: tString, Description: desc} }

// multiValued is the usual value/display/type/primary sub-attribute set.
func multiValued(name, desc string) attr {
	return attr{Name: name, Type: tComplex, Multi: true, Description: desc, Sub: []attr{
		str("value", "The value."),
		str("display", "A human-readable name."),
		str("type", "A label, for example work or home."),
		{Name: "primary", Type: tBoolean, Description: "The primary value."},
	}}
}

var userAttrs = []attr{
	{Name: "userName", Type: tString, Required: true, Uniqueness: "server",
		Description: "The unique identifier the user signs in with; mapped to the Hello username by default."},
	{Name: "name", Type: tComplex, Description: "The components of the user's name.", Sub: []attr{
		str("formatted", "The full name."), str("familyName", "The family name."), str("givenName", "The given name."),
		str("middleName", "The middle name."), str("honorificPrefix", "The honorific prefix."), str("honorificSuffix", "The honorific suffix."),
	}},
	str("displayName", "The name shown for the user; mapped to the Hello display name by default."),
	str("nickName", "A casual name."),
	{Name: "profileUrl", Type: tReference, Description: "A profile page."},
	str("title", "The job title."),
	str("userType", "The relation to the organisation."),
	str("preferredLanguage", "The preferred language."),
	str("locale", "The locale."),
	str("timezone", "The time zone (IANA)."),
	{Name: "active", Type: tBoolean, Description: "false disables the Hello user (it is never deleted)."},
	{Name: "password", Type: tString, Mutability: "writeOnly", Returned: "never",
		Description: "Ignored: a provisioned user signs in through its identity provider and has no Hello password."},
	multiValued("emails", "E-mail addresses; the primary one is mapped to the Hello e-mail by default."),
	multiValued("phoneNumbers", "Phone numbers."),
	multiValued("ims", "Instant messaging addresses."),
	multiValued("photos", "Photo URLs."),
	{Name: "addresses", Type: tComplex, Multi: true, Description: "Physical addresses.", Sub: []attr{
		str("formatted", "The full address."), str("streetAddress", "The street."), str("locality", "The city."),
		str("region", "The region."), str("postalCode", "The postal code."), str("country", "The country."),
		str("type", "A label, for example work."), {Name: "primary", Type: tBoolean, Description: "The primary address."},
	}},
	{Name: "groups", Type: tComplex, Multi: true, Mutability: "readOnly", Description: "The groups the user is a member of (set through the groups).",
		Sub: []attr{
			{Name: "value", Type: tString, Mutability: "readOnly", Description: "The group id."},
			{Name: "$ref", Type: tReference, Mutability: "readOnly", Description: "The group URI."},
			{Name: "display", Type: tString, Mutability: "readOnly", Description: "The group name."},
			{Name: "type", Type: tString, Mutability: "readOnly", Description: "direct."},
		}},
	multiValued("entitlements", "Entitlements."),
	multiValued("roles", "Roles in the identity provider. They do not give a Hello role: group rules do."),
	multiValued("x509Certificates", "Certificates."),
}

var enterpriseAttrs = []attr{
	str("employeeNumber", "The employee number."),
	str("costCenter", "The cost centre."),
	str("organization", "The organisation."),
	str("division", "The division."),
	str("department", "The department."),
	{Name: "manager", Type: tComplex, Description: "The user's manager.", Sub: []attr{
		str("value", "The manager's id."), {Name: "$ref", Type: tReference, Description: "The manager's URI."},
		{Name: "displayName", Type: tString, Mutability: "readOnly", Description: "The manager's name."},
	}},
}

var pbxAttrs = []attr{
	str("extension", "The user's Hello extension number (2 to 10 digits). The extension must exist in Hello; it is linked to the user by the connector's mapping."),
}

var groupAttrs = []attr{
	{Name: "displayName", Type: tString, Required: true, Uniqueness: "server", Description: "The group name."},
	{Name: "members", Type: tComplex, Multi: true, Description: "The users in the group.", Sub: []attr{
		{Name: "value", Type: tString, Mutability: "immutable", Description: "The member's id."},
		{Name: "$ref", Type: tReference, Mutability: "immutable", Description: "The member's URI."},
		{Name: "display", Type: tString, Mutability: "readOnly", Description: "The member's name."},
		{Name: "type", Type: tString, Mutability: "immutable", Description: "User."},
	}},
}

// schemaDef is a schema served by /Schemas.
type schemaDef struct {
	ID, Name, Description string
	Attrs                 []attr
}

var schemas = []schemaDef{
	{SchemaUser, "User", "User Account", userAttrs},
	{SchemaGroup, "Group", "Group", groupAttrs},
	{SchemaEnterprise, "EnterpriseUser", "Enterprise User", enterpriseAttrs},
	{SchemaPBX, "HelloPBXUser", "Hello PBX user extension", pbxAttrs},
}

// extensionsOf are the extension schemas a resource type allows.
func extensionsOf(typ string) []schemaDef {
	if typ == TypeUser {
		return []schemaDef{schemas[2], schemas[3]}
	}
	return nil
}

func coreAttrs(typ string) []attr {
	if typ == TypeUser {
		return userAttrs
	}
	return groupAttrs
}

func coreSchema(typ string) string {
	if typ == TypeUser {
		return SchemaUser
	}
	return SchemaGroup
}

// caseExact names the attributes compared case-sensitively in filters; the
// others compare without regard to case (RFC 7643 section 2.2).
var caseExact = map[string]bool{"id": true, "externalid": true, "meta.version": true}

func (a attr) json() map[string]any {
	m := map[string]any{
		"name": a.Name, "type": a.Type, "multiValued": a.Multi, "description": a.Description,
		"required": a.Required, "caseExact": a.CaseExact || a.Name == "id", "mutability": a.mutability(),
		"returned": a.returned(), "uniqueness": a.uniqueness(),
	}
	if a.Type == tReference {
		m["referenceTypes"] = []string{"external"}
		if a.Name == "$ref" {
			m["referenceTypes"] = []string{"User", "Group"}
		}
	}
	if len(a.Sub) > 0 {
		subs := make([]map[string]any, len(a.Sub))
		for i, s := range a.Sub {
			subs[i] = s.json()
		}
		m["subAttributes"] = subs
	}
	return m
}

func (s schemaDef) json(base string) map[string]any {
	as := make([]map[string]any, len(s.Attrs))
	for i, a := range s.Attrs {
		as[i] = a.json()
	}
	return map[string]any{
		"schemas": []string{schemaSchemaDef}, "id": s.ID, "name": s.Name, "description": s.Description, "attributes": as,
		"meta": map[string]any{"resourceType": "Schema", "location": base + "/Schemas/" + s.ID},
	}
}

func resourceTypeJSON(typ, base string) map[string]any {
	ext := []map[string]any{}
	for _, e := range extensionsOf(typ) {
		ext = append(ext, map[string]any{"schema": e.ID, "required": false})
	}
	ep, desc := "/Users", "User Account"
	if typ == TypeGroup {
		ep, desc = "/Groups", "Group"
	}
	return map[string]any{
		"schemas": []string{schemaResType}, "id": typ, "name": typ, "endpoint": ep, "description": desc,
		"schema": coreSchema(typ), "schemaExtensions": ext,
		"meta": map[string]any{"resourceType": "ResourceType", "location": base + "/ResourceTypes/" + typ},
	}
}

// MaxResults caps a page (I-23).
const MaxResults = 200

// DefaultCount is a page's size when the request names none.
const DefaultCount = 100

func serviceProviderConfig(base string) map[string]any {
	return map[string]any{
		"schemas":          []string{schemaSPConfig},
		"documentationUri": "https://github.com/azrtydxb/hello/blob/main/docs/scim.md",
		"patch":            map[string]any{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": MaxResults},
		"changePassword":   map[string]any{"supported": false},
		"sort":             map[string]any{"supported": false},
		"etag":             map[string]any{"supported": true},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "Bearer token", "primary": true,
			"description": "A hello_scim_ token issued for a SCIM directory connector in the Hello console.",
		}},
		"meta": map[string]any{"resourceType": "ServiceProviderConfig", "location": base + "/ServiceProviderConfig"},
	}
}
