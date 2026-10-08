package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/azrtydxb/hello/internal/replay"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// resourceDef is one hello:// resource or template and the GET operation it
// replays (spec S-16).
type resourceDef struct {
	uri, name, title, desc string
	// op is the operationId of the GET; path its path with {id} for a
	// template; query fixed query parameters.
	op, path string
	query    url.Values
}

// resources are the static resources.
var resources = []resourceDef{
	{"hello://calls", "calls", "Live calls", "The calls in progress across the cluster.", "listCalls", "/api/v1/calls", nil},
	{"hello://registrations", "registrations", "Registrations", "The devices registered now, with their contacts.", "listRegistrations", "/api/v1/registrations", nil},
	{"hello://cdrs/recent", "cdrs-recent", "Recent calls", "The last 50 call detail records.", "listCDRs", "/api/v1/cdrs", url.Values{"limit": {"50"}}},
	{"hello://trunks/status", "trunks-status", "Trunk status", "Each trunk's registration and reachability.", "listTrunkStatus", "/api/v1/trunks/status", nil},
	{"hello://cluster", "cluster", "Cluster", "The cluster's nodes, leases and health.", "getCluster", "/api/v1/cluster", nil},
}

// templates are the resource templates; {id} is the last path segment.
var templates = []resourceDef{
	{"hello://cdrs/{id}", "cdr", "Call detail record", "One CDR with its trace.", "getCDR", "/api/v1/cdrs/{id}", nil},
	{"hello://extensions/{id}", "extension", "Extension", "One extension.", "getExtension", "/api/v1/extensions/{id}", nil},
	{"hello://diagnostics/devices/{id}", "device-diagnostics", "Device diagnostics", "A device's registration attempts and failures.", "getDeviceDiagnostics", "/api/v1/diagnostics/devices/{id}", nil},
}

// addResources registers the resources and templates on srv; each read
// replays its GET as the caller.
func (s *server) addResources(srv *sdk.Server) {
	for _, d := range resources {
		srv.AddResource(&sdk.Resource{URI: d.uri, Name: d.name, Title: d.title, Description: d.desc, MIMEType: "application/json"},
			s.resourceHandler(d))
	}
	for _, d := range templates {
		srv.AddResourceTemplate(&sdk.ResourceTemplate{URITemplate: d.uri, Name: d.name, Title: d.title, Description: d.desc, MIMEType: "application/json"},
			s.resourceHandler(d))
	}
}

func (s *server) resourceHandler(d resourceDef) sdk.ResourceHandler {
	return func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		uri := req.Params.URI
		c, ok := callerFrom(ctx)
		if !ok {
			return nil, errors.New("mcp: resource read without an authenticated caller")
		}
		path := d.path
		if strings.Contains(d.uri, "{id}") {
			id, ok := strings.CutPrefix(uri, strings.TrimSuffix(d.uri, "{id}"))
			if !ok || id == "" || strings.Contains(id, "/") {
				return nil, sdk.ResourceNotFoundError(uri)
			}
			path = strings.ReplaceAll(path, "{id}", url.PathEscape(id))
		}
		res, err := s.replay(ctx, c, http.MethodGet, path, d.query, nil)
		if err != nil {
			return nil, err
		}
		switch {
		case res.Fail != "":
			return nil, fmt.Errorf("reading %s: %s", uri, res.Fail)
		case res.Status == http.StatusNotFound:
			return nil, sdk.ResourceNotFoundError(uri)
		case res.Status >= 300:
			return nil, fmt.Errorf("reading %s: %s", uri, apiErrorText(res))
		}
		v, err := decode(res.Body)
		if err != nil {
			return nil, fmt.Errorf("reading %s: the API answered malformed JSON", uri)
		}
		replay.Redact(v, s.ops[d.op].Secrets)
		text, _ := json.Marshal(v)
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(text)}}}, nil
	}
}

// apiErrorText is the API's error code and message of a non-2xx response.
func apiErrorText(res replay.Result) string {
	r := apiError(res)
	if len(r.Content) == 1 {
		if t, ok := r.Content[0].(*sdk.TextContent); ok {
			return t.Text
		}
	}
	return http.StatusText(res.Status)
}
