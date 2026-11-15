package scim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
)

// ContentType is the SCIM media type (RFC 7644 section 3.1).
const ContentType = "application/scim+json"

// maxBody bounds a request body.
const maxBody = 2 << 20

// Prefix is where the service is mounted.
const Prefix = "/scim/v2"

type request struct {
	w   http.ResponseWriter
	r   *http.Request
	c   directory.Connector
	tok Token
}

// ServeHTTP serves /scim/v2. Every request needs a bearer token of an
// enabled scim connector; the token decides the connector.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	rest, ok := strings.CutPrefix(r.URL.Path, Prefix)
	if !ok {
		s.fail(w, r, "other", errNotFound("not found"))
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	resource := "other"
	if len(parts) > 0 {
		switch strings.ToLower(parts[0]) {
		case "users":
			resource = "Users"
		case "groups":
			resource = "Groups"
		case "serviceproviderconfig", "schemas", "resourcetypes":
			resource = "discovery"
		}
	}
	tok, c, e := s.authenticate(r)
	if e != nil {
		if e.Status == http.StatusUnauthorized {
			h.Set("WWW-Authenticate", `Bearer realm="hello-scim"`)
		}
		s.fail(w, r, resource, e)
		return
	}
	if !s.allow(tok.ID) {
		e := &Error{Status: http.StatusTooManyRequests, Detail: "too many requests for this token; retry in a minute",
			headers: map[string]string{"Retry-After": "60"}}
		s.fail(w, r, resource, e)
		return
	}
	s.touch(r.Context(), tok.ID)
	q := &request{w: w, r: r, c: c, tok: tok}
	status := s.route(q, parts)
	s.Metrics.request(r.Method, resource, status)
}

func (s *Service) authenticate(r *http.Request) (Token, directory.Connector, *Error) {
	unauth := errStatus(http.StatusUnauthorized, "a valid SCIM bearer token is required")
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, TokenPrefix) || len(token) > 128 {
		return Token{}, directory.Connector{}, unauth
	}
	t, err := s.Store.LookupToken(r.Context(), hashToken(token))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.log().Error("scim: token lookup", "err", err)
			return Token{}, directory.Connector{}, errStatus(http.StatusInternalServerError, "the token could not be checked")
		}
		return Token{}, directory.Connector{}, unauth
	}
	c, err := s.Engine.Get(r.Context(), t.ConnectorID)
	if err != nil || c.Kind != directory.KindSCIM {
		return Token{}, directory.Connector{}, unauth
	}
	if !c.Enabled {
		return Token{}, directory.Connector{}, errStatus(http.StatusForbidden, "the SCIM connector is switched off in Hello")
	}
	return t, c, nil
}

func (s *Service) route(q *request, parts []string) int {
	m := q.r.Method
	switch {
	case len(parts) == 1 && strings.EqualFold(parts[0], "ServiceProviderConfig"):
		if m != http.MethodGet {
			return s.notAllowed(q)
		}
		return s.write(q, http.StatusOK, serviceProviderConfig(s.BaseURL()))
	case len(parts) <= 2 && strings.EqualFold(parts[0], "Schemas"):
		if m != http.MethodGet {
			return s.notAllowed(q)
		}
		return s.schemas(q, parts)
	case len(parts) <= 2 && strings.EqualFold(parts[0], "ResourceTypes"):
		if m != http.MethodGet {
			return s.notAllowed(q)
		}
		return s.resourceTypes(q, parts)
	case len(parts) <= 2 && (strings.EqualFold(parts[0], "Users") || strings.EqualFold(parts[0], "Groups")):
		typ := TypeUser
		if strings.EqualFold(parts[0], "Groups") {
			typ = TypeGroup
		}
		if len(parts) == 1 {
			switch m {
			case http.MethodGet:
				return s.list(q, typ)
			case http.MethodPost:
				return s.create(q, typ)
			}
			return s.notAllowed(q)
		}
		if parts[1] == ".search" {
			return s.send(q, errStatus(http.StatusNotImplemented, "POST .search is not supported; use GET with a filter"))
		}
		switch m {
		case http.MethodGet:
			return s.get(q, typ, parts[1])
		case http.MethodPut:
			return s.replace(q, typ, parts[1])
		case http.MethodPatch:
			return s.patch(q, typ, parts[1])
		case http.MethodDelete:
			return s.remove(q, typ, parts[1])
		}
		return s.notAllowed(q)
	case len(parts) == 1 && strings.EqualFold(parts[0], "Bulk"):
		return s.send(q, errStatus(http.StatusNotImplemented, "bulk operations are not supported"))
	case len(parts) == 1 && strings.EqualFold(parts[0], "Me"):
		return s.send(q, errStatus(http.StatusNotImplemented, "/Me is not supported"))
	}
	return s.send(q, errNotFound("no such endpoint"))
}

func (s *Service) notAllowed(q *request) int {
	return s.send(q, errStatus(http.StatusMethodNotAllowed, q.r.Method+" is not allowed here"))
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, resource string, e *Error) {
	s.send(&request{w: w, r: r}, e)
	s.Metrics.request(r.Method, resource, e.Status)
}

func (s *Service) send(q *request, e *Error) int {
	for k, v := range e.headers {
		q.w.Header().Set(k, v)
	}
	return s.write(q, e.Status, e.body())
}

func (s *Service) write(q *request, status int, body any) int {
	q.w.Header().Set("Content-Type", ContentType)
	q.w.WriteHeader(status)
	if body != nil {
		if err := json.NewEncoder(q.w).Encode(body); err != nil {
			s.log().Debug("scim: write response", "err", err)
		}
	}
	return status
}

func (s *Service) schemas(q *request, parts []string) int {
	base := s.BaseURL()
	if len(parts) == 2 {
		for _, sc := range schemas {
			if sc.ID == parts[1] {
				return s.write(q, http.StatusOK, sc.json(base))
			}
		}
		return s.send(q, errNotFound("no such schema"))
	}
	out := make([]any, len(schemas))
	for i, sc := range schemas {
		out[i] = sc.json(base)
	}
	return s.write(q, http.StatusOK, listResponse(out, len(out), 1))
}

func (s *Service) resourceTypes(q *request, parts []string) int {
	base := s.BaseURL()
	if len(parts) == 2 {
		switch parts[1] {
		case TypeUser, TypeGroup:
			return s.write(q, http.StatusOK, resourceTypeJSON(parts[1], base))
		}
		return s.send(q, errNotFound("no such resource type"))
	}
	out := []any{resourceTypeJSON(TypeUser, base), resourceTypeJSON(TypeGroup, base)}
	return s.write(q, http.StatusOK, listResponse(out, 2, 1))
}

func listResponse(items []any, total, start int) map[string]any {
	if items == nil {
		items = []any{}
	}
	return map[string]any{"schemas": []string{schemaList}, "totalResults": total, "startIndex": start,
		"itemsPerPage": len(items), "Resources": items}
}

func etag(r Resource) string { return `W/"` + strconv.FormatInt(r.Version, 10) + `"` }

// matches reports whether an If-Match or If-None-Match header names the tag.
func matches(header, tag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || t == tag || strings.TrimPrefix(t, "W/") == strings.TrimPrefix(tag, "W/") {
			return true
		}
	}
	return false
}

// precondition checks If-Match on a write.
func precondition(q *request, r Resource) *Error {
	if h := q.r.Header.Get("If-Match"); h != "" && !matches(h, etag(r)) {
		return errStatus(http.StatusPreconditionFailed, "the resource changed; read it again (ETag "+etag(r)+")")
	}
	return nil
}

// render is a resource as returned: its document, id, schemas, meta and
// the computed groups of a user or members of a group.
func (s *Service) render(r Resource, groups []groupRef, members []memberRef, withMembers bool) map[string]any {
	base := s.BaseURL()
	d := r.doc()
	d["id"] = r.ID
	sc := []string{coreSchema(r.Type)}
	for _, e := range extensionsOf(r.Type) {
		if _, ok := d[e.ID]; ok {
			sc = append(sc, e.ID)
		}
	}
	d["schemas"] = sc
	ep := "/Users/"
	if r.Type == TypeGroup {
		ep = "/Groups/"
	}
	d["meta"] = map[string]any{
		"resourceType": r.Type, "created": r.Created.UTC().Format(time.RFC3339), "lastModified": r.Updated.UTC().Format(time.RFC3339),
		"location": base + ep + r.ID, "version": etag(r),
	}
	if r.Type == TypeUser {
		if _, ok := d["active"]; !ok {
			d["active"] = true
		}
		if len(groups) > 0 {
			gs := make([]any, len(groups))
			for i, g := range groups {
				gs[i] = map[string]any{"value": g.ID, "display": g.Name, "$ref": base + "/Groups/" + g.ID, "type": "direct"}
			}
			d["groups"] = gs
		}
	}
	if r.Type == TypeGroup && withMembers {
		ms := make([]any, len(members))
		for i, m := range members {
			ms[i] = map[string]any{"value": m.ID, "display": m.Name, "$ref": base + "/Users/" + m.ID, "type": "User"}
		}
		d["members"] = ms
	}
	return d
}

// renderOne reads a resource's groups or members and renders it.
func (s *Service) renderOne(ctx context.Context, r Resource) (map[string]any, *Error) {
	if r.Type == TypeUser {
		gs, err := s.Store.GroupsOf(ctx, []string{r.ID})
		if err != nil {
			return nil, s.saveError(r.Type, err)
		}
		return s.render(r, gs[r.ID], nil, false), nil
	}
	ms, err := s.Store.Members(ctx, []string{r.ID})
	if err != nil {
		return nil, s.saveError(r.Type, err)
	}
	return s.render(r, nil, ms[r.ID], true), nil
}

func (s *Service) respond(q *request, status int, r Resource) int {
	d, e := s.renderOne(q.r.Context(), r)
	if e != nil {
		return s.send(q, e)
	}
	q.w.Header().Set("ETag", etag(r))
	if status == http.StatusCreated {
		q.w.Header().Set("Location", d["meta"].(map[string]any)["location"].(string))
	}
	return s.write(q, status, project(d, q.r.URL.Query()))
}

// project applies the attributes and excludedAttributes parameters.
func project(d map[string]any, qs map[string][]string) map[string]any {
	split := func(k string) []string {
		var out []string
		for _, v := range qs[k] {
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
		}
		return out
	}
	if inc := split("attributes"); len(inc) > 0 {
		out := map[string]any{"id": d["id"], "schemas": d["schemas"]}
		for _, p := range inc {
			copyPath(d, out, p)
		}
		return out
	}
	for _, p := range split("excludedAttributes") {
		pp, err := parsePath(p)
		if err != nil || strings.EqualFold(pp.attr, "id") || strings.EqualFold(pp.attr, "schemas") {
			continue
		}
		holder, key := locate(d, pp.attr)
		if holder == nil {
			continue
		}
		if pp.sub == "" {
			delete(holder, key)
		} else if m, ok := holder[key].(map[string]any); ok {
			for k := range m {
				if strings.EqualFold(k, pp.sub) {
					delete(m, k)
				}
			}
		}
	}
	return d
}

// locate finds the map that holds an attribute and its key there.
func locate(d map[string]any, attrName string) (map[string]any, string) {
	for _, e := range knownSchemas[2:] {
		if len(attrName) > len(e)+1 && strings.EqualFold(attrName[:len(e)], e) {
			ext, _ := d[e].(map[string]any)
			if ext == nil {
				return nil, ""
			}
			return ext, keyCI(ext, attrName[len(e)+1:])
		}
	}
	return d, keyCI(d, attrName)
}

func keyCI(d map[string]any, k string) string {
	if _, ok := d[k]; ok {
		return k
	}
	for kk := range d {
		if strings.EqualFold(kk, k) {
			return kk
		}
	}
	return ""
}

func copyPath(from, to map[string]any, p string) {
	pp, err := parsePath(p)
	if err != nil {
		return
	}
	if ext := extensionByName(TypeUser, pp.attr); ext != nil {
		if v, ok := from[ext.ID]; ok {
			to[ext.ID] = v
		}
		return
	}
	src, key := locate(from, pp.attr)
	if src == nil || key == "" {
		return
	}
	dst := to
	for _, e := range knownSchemas[2:] {
		if len(pp.attr) > len(e)+1 && strings.EqualFold(pp.attr[:len(e)], e) {
			m, _ := to[e].(map[string]any)
			if m == nil {
				m = map[string]any{}
				to[e] = m
			}
			dst = m
		}
	}
	v := src[key]
	if pp.sub == "" {
		dst[key] = v
		return
	}
	switch x := v.(type) {
	case map[string]any:
		m, _ := dst[key].(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		if sk := keyCI(x, pp.sub); sk != "" {
			m[sk] = x[sk]
		}
		dst[key] = m
	case []any:
		dst[key] = x
	}
}

func intParam(qs map[string][]string, k string, def int) (int, *Error) {
	v := ""
	if l := qs[k]; len(l) > 0 {
		v = l[0]
	}
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errInvalidValue(k + " must be an integer")
	}
	return n, nil
}

func (s *Service) list(q *request, typ string) int {
	ctx := q.r.Context()
	qs := q.r.URL.Query()
	start, e := intParam(qs, "startIndex", 1)
	if e != nil {
		return s.send(q, e)
	}
	count, e := intParam(qs, "count", DefaultCount)
	if e != nil {
		return s.send(q, e)
	}
	if start < 1 {
		start = 1
	}
	if count < 0 {
		count = 0
	}
	if count > MaxResults {
		count = MaxResults
	}
	if qs.Get("sortBy") != "" {
		return s.send(q, &Error{Status: http.StatusBadRequest, Type: typeInvalidValue, Detail: "sorting is not supported"})
	}
	var (
		f    node
		hint *Hint
	)
	if fs := strings.TrimSpace(qs.Get("filter")); fs != "" {
		var err *Error
		if f, err = parseFilter(fs); err != nil {
			return s.send(q, err)
		}
		hint = hintOf(typ, f)
	}
	rs, err := s.Store.FindResources(ctx, q.c.ID, typ, hint)
	if err != nil {
		return s.send(q, s.saveError(typ, err))
	}
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
	}
	withMembers := typ == TypeGroup && !excludes(qs, "members")
	var (
		groups  map[string][]groupRef
		members map[string][]memberRef
	)
	if typ == TypeUser {
		if groups, err = s.Store.GroupsOf(ctx, ids); err != nil {
			return s.send(q, s.saveError(typ, err))
		}
	} else if withMembers || f != nil {
		if members, err = s.Store.Members(ctx, ids); err != nil {
			return s.send(q, s.saveError(typ, err))
		}
	}
	var docs []map[string]any
	for _, r := range rs {
		d := s.render(r, groups[r.ID], members[r.ID], typ == TypeGroup)
		if f != nil && !f.eval(d) {
			continue
		}
		if typ == TypeGroup && !withMembers {
			delete(d, "members")
		}
		docs = append(docs, d)
	}
	total := len(docs)
	page := []any{}
	for i := start - 1; i < len(docs) && len(page) < count; i++ {
		page = append(page, project(docs[i], qs))
	}
	return s.write(q, http.StatusOK, listResponse(page, total, start))
}

func excludes(qs map[string][]string, attrName string) bool {
	for _, v := range qs["excludedAttributes"] {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), attrName) {
				return true
			}
		}
	}
	return false
}

func (s *Service) get(q *request, typ, id string) int {
	r, err := s.Store.GetResource(q.r.Context(), q.c.ID, typ, id, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return s.send(q, errNotFound(typ+" "+id+" not found"))
		}
		return s.send(q, s.saveError(typ, err))
	}
	if h := q.r.Header.Get("If-None-Match"); h != "" && matches(h, etag(r)) {
		q.w.Header().Set("ETag", etag(r))
		q.w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}
	return s.respond(q, http.StatusOK, r)
}

// body reads a JSON object body.
func body(q *request) (map[string]any, *Error) {
	ct := strings.ToLower(q.r.Header.Get("Content-Type"))
	if ct != "" && !strings.HasPrefix(ct, ContentType) && !strings.HasPrefix(ct, "application/json") {
		return nil, &Error{Status: http.StatusUnsupportedMediaType, Detail: "send application/scim+json"}
	}
	b, err := io.ReadAll(http.MaxBytesReader(q.w, q.r.Body, maxBody))
	if err != nil {
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Detail: "the body is too large"}
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil || m == nil {
		return nil, errInvalidSyntax("the body is not a JSON object")
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errInvalidSyntax("the body has more than one JSON value")
	}
	return m, nil
}

// locked runs a write under the connector's lock, after checking the
// connector again (a pause or a switch-off since the token was checked).
func (s *Service) locked(q *request, fn func(c directory.Connector) int) int {
	ctx := q.r.Context()
	unlock, err := s.Store.Lock(ctx, q.c.ID)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			e := errStatus(http.StatusServiceUnavailable, "the connector is busy (a sync run is applying); retry shortly")
			e.headers = map[string]string{"Retry-After": "30"}
			return s.send(q, e)
		}
		s.log().Error("scim: lock", "err", err)
		return s.send(q, errStatus(http.StatusInternalServerError, "the connector could not be locked"))
	}
	defer unlock()
	c, err := s.Engine.Get(ctx, q.c.ID)
	if err != nil {
		return s.send(q, errStatus(http.StatusUnauthorized, "a valid SCIM bearer token is required"))
	}
	switch {
	case !c.Enabled:
		return s.send(q, s.pushError(directory.ErrDisabled))
	case c.PausedAt != nil:
		return s.send(q, s.pushError(directory.ErrPaused))
	}
	return fn(c)
}

func (s *Service) create(q *request, typ string) int {
	in, e := body(q)
	if e != nil {
		return s.send(q, e)
	}
	doc, e := normalize(typ, in)
	if e != nil {
		return s.send(q, e)
	}
	if typ == TypeGroup {
		if v := getCI(in, "members"); v != nil {
			mv, err := coerce(*sub(groupAttrs, "members"), v, "members")
			if err != nil {
				return s.send(q, err)
			}
			doc["members"] = mv
		}
	}
	name, ext, data, e := fromDoc(typ, doc)
	if e != nil {
		return s.send(q, e)
	}
	return s.locked(q, func(c directory.Connector) int {
		ctx := q.r.Context()
		next := Resource{ConnectorID: c.ID, Type: typ, Name: name, ExternalID: ext, Data: data}
		if typ == TypeUser {
			// A user the IdP deleted and now creates again comes back with
			// its id, so it links to the same (disabled) Hello user.
			old, err := s.Store.FindDeleted(ctx, c.ID, typ, name, ext)
			if err != nil {
				return s.send(q, s.saveError(typ, err))
			}
			if old != nil {
				next.ID, next.Version, next.Created = old.ID, old.Version, old.Created
			}
		}
		if next.ID == "" {
			id, err := newID()
			if err != nil {
				return s.send(q, s.saveError(typ, err))
			}
			next.ID = id
		}
		var (
			saved Resource
			werr  *Error
		)
		if typ == TypeUser {
			saved, werr = s.writeUser(ctx, c, next)
		} else {
			saved, werr = s.writeGroup(ctx, c, nil, next, membersOf(doc))
		}
		if werr != nil {
			return s.send(q, werr)
		}
		return s.respond(q, http.StatusCreated, saved)
	})
}

// load reads a live resource for a write and checks If-Match.
func (s *Service) load(q *request, c directory.Connector, typ, id string) (Resource, *Error) {
	r, err := s.Store.GetResource(q.r.Context(), c.ID, typ, id, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Resource{}, errNotFound(typ + " " + id + " not found")
		}
		return Resource{}, s.saveError(typ, err)
	}
	if e := precondition(q, r); e != nil {
		return Resource{}, e
	}
	return r, nil
}

func (s *Service) replace(q *request, typ, id string) int {
	in, e := body(q)
	if e != nil {
		return s.send(q, e)
	}
	if v, ok := in["id"].(string); ok && v != "" && v != id {
		return s.send(q, errMutability("id cannot be changed"))
	}
	doc, e := normalize(typ, in)
	if e != nil {
		return s.send(q, e)
	}
	var members []string
	if typ == TypeGroup {
		if v := getCI(in, "members"); v != nil {
			mv, err := coerce(*sub(groupAttrs, "members"), v, "members")
			if err != nil {
				return s.send(q, err)
			}
			members = membersOf(map[string]any{"members": mv})
		}
	}
	name, ext, data, e := fromDoc(typ, doc)
	if e != nil {
		return s.send(q, e)
	}
	return s.locked(q, func(c directory.Connector) int {
		old, e := s.load(q, c, typ, id)
		if e != nil {
			return s.send(q, e)
		}
		next := old
		next.Name, next.ExternalID, next.Data = name, ext, data
		var saved Resource
		if typ == TypeUser {
			saved, e = s.writeUser(q.r.Context(), c, next)
		} else {
			if members == nil {
				members = []string{}
			}
			saved, e = s.writeGroup(q.r.Context(), c, &old, next, members)
		}
		if e != nil {
			return s.send(q, e)
		}
		return s.respond(q, http.StatusOK, saved)
	})
}

func (s *Service) patch(q *request, typ, id string) int {
	in, e := body(q)
	if e != nil {
		return s.send(q, e)
	}
	raw, _ := json.Marshal(in)
	var p patchBody
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return s.send(q, errInvalidSyntax("the body is not a PatchOp"))
	}
	if len(p.Schemas) > 0 && !containsFold(p.Schemas, schemaPatch) {
		return s.send(q, errInvalidSyntax("the body is not a PatchOp ("+schemaPatch+")"))
	}
	if p.Operations == nil {
		// Some clients write "operations".
		if ops, ok := getCI(in, "operations").([]any); ok {
			b, _ := json.Marshal(ops)
			d := json.NewDecoder(strings.NewReader(string(b)))
			d.UseNumber()
			_ = d.Decode(&p.Operations)
		}
	}
	return s.locked(q, func(c directory.Connector) int {
		ctx := q.r.Context()
		old, e := s.load(q, c, typ, id)
		if e != nil {
			return s.send(q, e)
		}
		doc := old.doc()
		if typ == TypeGroup {
			ms, err := s.Store.Members(ctx, []string{old.ID})
			if err != nil {
				return s.send(q, s.saveError(typ, err))
			}
			list := make([]any, len(ms[old.ID]))
			for i, m := range ms[old.ID] {
				list[i] = map[string]any{"value": m.ID}
			}
			if len(list) > 0 {
				doc["members"] = list
			}
		}
		if e := applyPatch(typ, doc, p.Operations); e != nil {
			return s.send(q, e)
		}
		name, ext, data, e := fromDoc(typ, doc)
		if e != nil {
			return s.send(q, e)
		}
		next := old
		next.Name, next.ExternalID, next.Data = name, ext, data
		var saved Resource
		if typ == TypeUser {
			saved, e = s.writeUser(ctx, c, next)
		} else {
			saved, e = s.writeGroup(ctx, c, &old, next, membersOf(doc))
		}
		if e != nil {
			return s.send(q, e)
		}
		// Entra and Okta accept 200 with the resource; 204 is also allowed.
		return s.respond(q, http.StatusOK, saved)
	})
}

func (s *Service) remove(q *request, typ, id string) int {
	return s.locked(q, func(c directory.Connector) int {
		old, e := s.load(q, c, typ, id)
		if e != nil {
			return s.send(q, e)
		}
		if typ == TypeUser {
			e = s.deleteUser(q.r.Context(), c, old)
		} else {
			e = s.deleteGroup(q.r.Context(), c, old)
		}
		if e != nil {
			return s.send(q, e)
		}
		q.w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	})
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
