package redirect

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // the RPS fake recomputes Content-MD5
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/prov"
)

// secretVal assembles a credential-like test value at run time, so
// scanners do not flag a literal.
func secretVal(name string) string { return "s3" + "cr" + "et-" + name + "-" + strings.Repeat("x", 6) }

const (
	phoneURL = "https://prov.hello.test/p/tokentokentoken/{mac}"
	macColon = "00:04:13:aa:bb:cc"
	macPlain = "000413aabbcc"
)

// rewrite sends every request to the fake, keeping the vendor's real host
// in the Host header, and counts the requests made.
type rewrite struct {
	addr string
	n    atomic.Int64
}

func (r *rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	r.n.Add(1)
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host = "http", r.addr, req.URL.Host
	return http.DefaultTransport.RoundTrip(out)
}

func fakeHTTP(t *testing.T, h http.Handler) (*http.Client, *rewrite) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	rw := &rewrite{addr: srv.Listener.Addr().String()}
	return &http.Client{Transport: rw, Timeout: 5 * time.Second}, rw
}

// ---- Snom SRAPS (REST with Hawk, legacy XML-RPC) ----

var hawkAttr = regexp.MustCompile(`(\w+)="([^"]*)"`)

// snomFake is SRAPS: it verifies every Hawk header the way the Hawk spec
// does (its own implementation, not the client's) and keeps endpoints.
type snomFake struct {
	t        *testing.T
	id, key  string
	mu       sync.Mutex
	eps      map[string]json.RawMessage
	rpc      map[string]string // XML-RPC registrations by uppercase MAC
	calls    []string
	foreign  string // "company" or "endpoints": that link points at another host
	rpcCalls []string
}

func (f *snomFake) verifyHawk(r *http.Request, body []byte) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Hawk ") {
		return false
	}
	a := map[string]string{}
	for _, m := range hawkAttr.FindAllStringSubmatch(h, -1) {
		a[m[1]] = m[2]
	}
	ts, err := strconv.ParseInt(a["ts"], 10, 64)
	if err != nil || a["id"] != f.id || time.Since(time.Unix(ts, 0)).Abs() > time.Minute {
		return false
	}
	ctype := r.Header.Get("Content-Type")
	ph := sha256.Sum256([]byte("hawk.1.payload\n" + ctype + "\n" + string(body) + "\n"))
	hash := base64.StdEncoding.EncodeToString(ph[:])
	if a["hash"] != hash {
		return false
	}
	host, port := r.Host, "443"
	norm := strings.Join([]string{"hawk.1.header", a["ts"], a["nonce"], r.Method, r.URL.RequestURI(), host, port, hash, a["ext"]}, "\n") + "\n"
	m := hmac.New(sha256.New, []byte(f.key))
	m.Write([]byte(norm))
	return hmac.Equal([]byte(a["mac"]), []byte(base64.StdEncoding.EncodeToString(m.Sum(nil))))
}

func (f *snomFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Host == "secure-provisioning.snom.com:8083" {
		f.serveRPC(w, r, body)
		return
	}
	if r.Host != "api.sraps.snom.com" || !f.verifyHawk(r, body) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Hawk authentication failed"}`))
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	base := func(link string) string {
		if f.foreign == link {
			return "https://evil.example/api/v1/"
		}
		return "https://api.sraps.snom.com/api/v1/"
	}
	switch p := r.URL.Path; {
	case p == "/api/v1/settings/" && r.URL.Query().Get("page") == "":
		w.Header().Set("Link", `<https://api.sraps.snom.com/api/v1/settings/?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"uuid":"u-other","param_name":"user_name"}]`))
	case p == "/api/v1/settings/":
		_, _ = w.Write([]byte(`[{"uuid":"u-server","param_name":"setting_server"}]`))
	case p == "/api/v1/tokens/"+f.id:
		_, _ = fmt.Fprintf(w, `{"links":{"company":"%scompanies/7/"}}`, base("company"))
	case p == "/api/v1/companies/7/":
		_, _ = fmt.Fprintf(w, `{"links":{"endpoints":"%scompanies/7/endpoints/"}}`, base("endpoints"))
	case strings.HasPrefix(p, "/api/v1/companies/7/endpoints/"):
		mac := strings.TrimPrefix(p, "/api/v1/companies/7/endpoints/")
		switch r.Method {
		case http.MethodPut:
			f.eps[mac] = body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(body)
		case http.MethodGet, http.MethodDelete:
			ep, ok := f.eps[mac]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodDelete {
				delete(f.eps, mac)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write(ep)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *snomFake) serveRPC(w http.ResponseWriter, r *http.Request, body []byte) {
	u, p, ok := r.BasicAuth()
	if !ok || u != f.id || p != f.key || r.URL.Path != "/xmlrpc/" || r.Header.Get("Content-Type") != "text/xml" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var call struct {
		Method string   `xml:"methodName"`
		Params []string `xml:"params>param>value>string"`
	}
	if err := xml.Unmarshal(body, &call); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.rpcCalls = append(f.rpcCalls, call.Method+"("+strings.Join(call.Params, ",")+")")
	answer := func(ok bool, msg string) {
		b := "0"
		if ok {
			b = "1"
		}
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><array><data><value><boolean>%s</boolean></value><value><string>%s</string></value></data></array></value></param></params></methodResponse>`, b, msg)
	}
	switch call.Method {
	case "redirect.registerPhone":
		f.rpc[call.Params[0]] = call.Params[1]
		answer(true, "ok")
	case "redirect.getPhoneRedirection":
		u, ok := f.rpc[call.Params[0]]
		if !ok {
			answer(false, "phone not found")
			return
		}
		answer(true, u)
	case "redirect.deregisterPhone":
		delete(f.rpc, call.Params[0])
		answer(true, "ok")
	default:
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>1</int></value></member><member><name>faultString</name><value><string>no such method</string></value></member></struct></value></fault></methodResponse>`))
	}
}

func snomCreds() Credentials {
	return Credentials{KeySnomKeyID: "kid-snom", KeySnomKeySecret: secretVal("snom")}
}

func newSnomFake(t *testing.T) *snomFake {
	return &snomFake{t: t, id: "kid-snom", key: secretVal("snom"), eps: map[string]json.RawMessage{}, rpc: map[string]string{}}
}

// ---- Yealink RPS (signed JSON API v3.6) and YMCS v2 ----

type rpsFake struct {
	key, secret string
	mu          sync.Mutex
	servers     map[string]string // name -> id
	serverAdds  int
	devices     map[string]map[string]string // mac -> device
	calls       []string
}

// rpsSignature is the v3.6 algorithm written out independently: method,
// sorted system headers, the path without "/", then the sorted query.
func rpsSignatureOf(secret string, r *http.Request) string {
	var b strings.Builder
	b.WriteString(r.Method + "\n")
	if md := r.Header.Get("Content-MD5"); md != "" {
		b.WriteString("Content-MD5:" + md + "\n")
	}
	for _, k := range []string{"X-Ca-Key", "X-Ca-Nonce", "X-Ca-Timestamp"} {
		b.WriteString(k + ":" + r.Header.Get(k) + "\n")
	}
	b.WriteString(strings.TrimPrefix(r.URL.Path, "/"))
	if q := r.URL.Query(); len(q) > 0 {
		var keys []string
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var pairs []string
		for _, k := range keys {
			pairs = append(pairs, k+"="+q.Get(k))
		}
		b.WriteString("\n" + strings.Join(pairs, "&"))
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (f *rpsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	refuse := func(code string) {
		_, _ = fmt.Fprintf(w, `{"ret":-1,"data":null,"error":{"msg":%q,"errorCode":400,"fieldErrors":[]}}`, code)
	}
	ok := func(data any) {
		b, _ := json.Marshal(map[string]any{"ret": 1, "data": data, "error": nil})
		_, _ = w.Write(b)
	}
	if r.Host != "api-dm.yealink.com:8443" || r.Header.Get("X-Ca-Key") != f.key {
		refuse("accesskey.id.invalid")
		return
	}
	if len(body) > 0 {
		sum := md5.Sum(body) //nolint:gosec // the API's Content-MD5
		if r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
			refuse("Content.MD5.invalid")
			return
		}
	}
	if r.Header.Get("X-Ca-Signature") != rpsSignatureOf(f.secret, r) {
		refuse("service.common.token.unauthorized")
		return
	}
	f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/api/open/v1/"))
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch strings.TrimPrefix(r.URL.Path, "/api/open/v1/") {
	case "device/serverList":
		ok([]any{})
	case "server/list":
		var list []map[string]string
		for n, id := range f.servers {
			if strings.Contains(n, str("key")) {
				list = append(list, map[string]string{"id": id, "serverName": n})
			}
		}
		ok(map[string]any{"data": list})
	case "server/add":
		f.serverAdds++
		id := fmt.Sprintf("srv%d", f.serverAdds)
		f.servers[str("serverName")] = id
		ok(map[string]string{"id": id, "serverName": str("serverName"), "url": str("url")})
	case "device/add":
		macs, _ := in["macs"].([]any)
		m, _ := macs[0].(string)
		if _, exists := f.devices[m]; exists {
			refuse("device.mac.existed")
			return
		}
		f.devices[m] = map[string]string{"id": "dev-" + m, "mac": m, "serverId": str("serverId"), "uniqueServerUrl": str("uniqueServerUrl")}
		ok([]any{f.devices[m]})
	case "device/list":
		var list []map[string]string
		for m, d := range f.devices {
			if strings.Contains(m, str("key")) {
				list = append(list, d)
			}
		}
		ok(map[string]any{"data": list})
	case "device/edit":
		for _, d := range f.devices {
			if d["id"] == str("id") {
				d["serverId"], d["uniqueServerUrl"] = str("serverId"), str("uniqueServerUrl")
				ok(d)
				return
			}
		}
		refuse("device.not.found")
	case "device/delete":
		macs, _ := in["macs"].([]any)
		m, _ := macs[0].(string)
		if _, exists := f.devices[m]; !exists {
			refuse("device.not.found")
			return
		}
		delete(f.devices, m)
		_, _ = w.Write([]byte(`{"ret":0,"data":null,"error":null}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type ymcsFake struct {
	id, secret string
	mu         sync.Mutex
	tokens     int
	devices    map[string]map[string]string
	calls      []string
	existsErr  string // addDevicesByMac errors[] entry for a MAC it holds
}

func (f *ymcsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Host != "eu-api.ymcs.yealink.com" || r.Header.Get("timestamp") == "" || r.Header.Get("nonce") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/v2/token" {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.id || p != f.secret || !strings.Contains(string(body), `"grant_type":"client_credentials"`) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		f.tokens++
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"bearer","expires_in":3600}`, f.tokens)
		return
	}
	if r.Header.Get("Authorization") != fmt.Sprintf("Bearer tok-%d", f.tokens) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"900401","message":"Not logged in"}`))
		return
	}
	f.calls = append(f.calls, r.URL.Path)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	switch r.URL.Path {
	case "/v2/rps/devices":
		m, _ := in["mac"].(string)
		sn, _ := in["sn"].(string)
		if sn == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"800002","message":"Invalid SN"}`))
			return
		}
		if _, ok := f.devices[m]; ok {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"800003","message":"Resource already exists"}`))
			return
		}
		u, _ := in["uniqueServerUrl"].(string)
		f.devices[m] = map[string]string{"mac": m, "sn": sn, "uniqueServerUrl": u}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	case "/v2/rps/addDevicesByMac":
		var list []map[string]string
		_ = json.Unmarshal(body, &list)
		m := list[0]["mac"]
		if _, ok := f.devices[m]; ok {
			_, _ = fmt.Fprintf(w, `{"total":1,"successCount":0,"failureCount":1,"errors":[%s]}`, f.existsErr)
			return
		}
		f.devices[m] = map[string]string{"mac": m, "uniqueServerUrl": list[0]["uniqueServerUrl"]}
		_, _ = w.Write([]byte(`{"total":1,"successCount":1,"failureCount":0}`))
	case "/v2/rps/delDevices":
		ids, _ := in["deviceIds"].([]any)
		m, _ := ids[0].(string)
		_, had := f.devices[m]
		delete(f.devices, m)
		if had {
			_, _ = w.Write([]byte(`{"total":1,"successCount":1,"failureCount":0}`))
		} else {
			_, _ = w.Write([]byte(`{"total":1,"successCount":0,"failureCount":1,"errors":[{"field":"mac","msg":"not found"}]}`))
		}
	case "/v2/rps/listDevices":
		filter, _ := in["filter"].(map[string]any)
		m, _ := filter["mac"].(string)
		var data []map[string]string
		if d, ok := f.devices[m]; ok {
			data = append(data, d)
		}
		b, _ := json.Marshal(map[string]any{"data": data})
		_, _ = w.Write(b)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ---- Grandstream GDMS ----

type gdmsFake struct {
	clientID, clientSecret, user, password string
	mu                                     sync.Mutex
	tokens                                 int
	devices                                map[string]map[string]any
	calls                                  []string
	hangUpOn                               string // path whose connection is dropped
}

func sha256h(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (f *gdmsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Host != "eu.gdms.cloud" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/oapi/oauth/token" {
		form, _ := url.ParseQuery(string(body))
		md := md5.Sum([]byte(f.password)) //nolint:gosec // GDMS's documented encoding
		var keys []string
		for k := range form {
			if k != "signature" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var pairs []string
		for _, k := range keys {
			pairs = append(pairs, k+"="+form.Get(k))
		}
		if form.Get("grant_type") != "password" || form.Get("username") != f.user || form.Get("client_id") != f.clientID ||
			form.Get("client_secret") != f.clientSecret || form.Get("password") != sha256h(hex.EncodeToString(md[:])) ||
			form.Get("signature") != sha256h("&"+strings.Join(pairs, "&")+"&") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad credentials"}`))
			return
		}
		f.tokens++
		_, _ = fmt.Fprintf(w, `{"access_token":"gdms-tok-%d","refresh_token":"r","expires_in":3600}`, f.tokens)
		return
	}
	q := r.URL.Query()
	tok := fmt.Sprintf("gdms-tok-%d", f.tokens)
	want := "&access_token=" + tok + "&client_id=" + f.clientID + "&client_secret=" + f.clientSecret + "&timestamp=" + q.Get("timestamp") + "&"
	if len(body) > 0 {
		want += sha256h(string(body)) + "&"
	}
	if q.Get("access_token") != tok {
		_, _ = w.Write([]byte(`{"retCode":40004,"msg":"access_token is invalid or expired"}`))
		return
	}
	if q.Get("signature") != sha256h(want) {
		_, _ = w.Write([]byte(`{"retCode":40002,"msg":"signature error"}`))
		return
	}
	f.calls = append(f.calls, r.URL.Path)
	if r.URL.Path == f.hangUpOn {
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close()
		return
	}
	switch r.URL.Path {
	case "/oapi/v1.0.0/site/list":
		_, _ = w.Write([]byte(`{"retCode":0,"msg":"","data":{"result":[{"id":3345,"siteName":"Default"}]}}`))
	case "/oapi/v1.0.0/device/add":
		var devs []map[string]any
		_ = json.Unmarshal(body, &devs)
		m, _ := devs[0]["mac"].(string)
		if _, ok := f.devices[m]; ok {
			_, _ = w.Write([]byte(`{"retCode":10012,"msg":"The device already exists"}`))
			return
		}
		f.devices[m] = devs[0]
		_, _ = w.Write([]byte(`{"retCode":0,"msg":"","data":{}}`))
	case "/oapi/v1.0.0/device/list":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		m, _ := in["mac"].(string)
		var res []map[string]any
		if d, ok := f.devices[m]; ok {
			res = append(res, map[string]any{"mac": m, "siteId": d["siteId"]})
		}
		b, _ := json.Marshal(map[string]any{"retCode": 0, "msg": "", "data": map[string]any{"result": res}})
		_, _ = w.Write(b)
	case "/oapi/v1.0.0/device/delete":
		var in struct {
			MacList []string `json:"macList"`
		}
		_ = json.Unmarshal(body, &in)
		if _, ok := f.devices[in.MacList[0]]; !ok {
			_, _ = w.Write([]byte(`{"retCode":10013,"msg":"The device does not exist"}`))
			return
		}
		delete(f.devices, in.MacList[0])
		_, _ = w.Write([]byte(`{"retCode":0,"msg":""}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func gdmsCreds() Credentials {
	return Credentials{
		KeyGDMSClientID: "gdms-client", KeyGDMSClientSecret: secretVal("gdmsclient"),
		KeyGDMSUsername: "admin@example.test", KeyGDMSPassword: secretVal("gdmspw"),
		KeyGDMSRegion: "eu", KeyGDMSSiteID: "3345",
	}
}

func newGDMSFake() *gdmsFake {
	c := gdmsCreds()
	return &gdmsFake{clientID: c[KeyGDMSClientID], clientSecret: c[KeyGDMSClientSecret], user: c[KeyGDMSUsername],
		password: c[KeyGDMSPassword], devices: map[string]map[string]any{}}
}

func rpsCreds() Credentials {
	return Credentials{KeyYealinkKey: "ak-yealink", KeyYealinkSecret: secretVal("rps")}
}

func ymcsCreds() Credentials {
	return Credentials{KeyYMCSClientID: "ymcs-client", KeyYMCSClientSecret: secretVal("ymcs"), KeyYMCSRegion: "eu"}
}

// ---- fake store ----

type finished struct {
	Job    Job
	Status Status
	Set    bool // false: the worker left the phone's status alone
}

type retried struct {
	Job    Job
	Next   time.Time
	Status Status
	Set    bool
}

func deref(st *Status) (Status, bool) {
	if st == nil {
		return Status{}, false
	}
	return *st, true
}

type fakeStore struct {
	mu         sync.Mutex
	jobs       []Job
	targets    map[string]Target // vendor/mac
	accounts   map[prov.Vendor]Account
	registered map[prov.Vendor][]Target
	finished   []finished
	retried    []retried
	statuses   map[string]Status
	accountErr map[prov.Vendor]error
	lastDrift  time.Time
	driftSets  []time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{targets: map[string]Target{}, accounts: map[prov.Vendor]Account{},
		registered: map[prov.Vendor][]Target{}, statuses: map[string]Status{}}
}

func (s *fakeStore) DueJobs(_ context.Context, limit int) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[:min(limit, len(s.jobs))]
	s.jobs = s.jobs[len(j):]
	return j, nil
}

func (s *fakeStore) Target(_ context.Context, v prov.Vendor, mac string) (Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[string(v)+"/"+mac]
	if !ok {
		return Target{}, prov.ErrNotFound
	}
	return t, nil
}

func (s *fakeStore) Account(_ context.Context, v prov.Vendor) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.accountErr[v]; err != nil {
		return Account{}, err
	}
	a, ok := s.accounts[v]
	if !ok {
		return Account{}, prov.ErrNotFound
	}
	return a, nil
}

func (s *fakeStore) FinishJob(_ context.Context, j Job, st *Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, set := deref(st)
	s.finished = append(s.finished, finished{j, v, set})
	return nil
}

func (s *fakeStore) RetryJob(_ context.Context, j Job, next time.Time, st *Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, set := deref(st)
	s.retried = append(s.retried, retried{j, next, v, set})
	return nil
}

func (s *fakeStore) LastDriftCheck(context.Context) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDrift, nil
}

func (s *fakeStore) SetLastDriftCheck(_ context.Context, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastDrift = t
	s.driftSets = append(s.driftSets, t)
	return nil
}

func (s *fakeStore) Registered(_ context.Context, v prov.Vendor) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registered[v], nil
}

func (s *fakeStore) SetStatus(_ context.Context, mac string, st Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[mac] = st
	return nil
}

// ---- the tests ----

// collector gathers every error string and log line a test produced, to
// grep for credentials.
type collector struct {
	logs bytes.Buffer
	errs []string
}

func (c *collector) err(err error) error {
	if err != nil {
		c.errs = append(c.errs, err.Error(), fmt.Sprintf("%+v %#v", err, err))
	}
	return err
}

func (c *collector) logger() *slog.Logger { return slog.New(slog.NewJSONHandler(&c.logs, nil)) }

func (c *collector) assertClean(t *testing.T, secrets ...string) {
	t.Helper()
	all := c.logs.String() + "\n" + strings.Join(c.errs, "\n")
	for _, s := range secrets {
		if s != "" && strings.Contains(all, s) {
			t.Fatalf("a credential or token appears in a log line or error: %q", s)
		}
	}
}

func credValues(cs ...Credentials) []string {
	var out []string
	for _, c := range cs {
		for k, v := range c {
			if k != KeyGDMSRegion && k != KeyYMCSRegion && k != KeyGDMSSiteID {
				out = append(out, v)
			}
		}
	}
	return out
}

// TestRedirectClients exercises every vendor client against a fake of the
// vendor's documented API (signatures verified by the fake's own
// implementation of each algorithm) and the worker against a fake store.
// It fails if create, rotate or delete does not issue the documented call
// with the per-device URL, if a credential or token appears in a log or
// error string, if a failure does not back off and surface failed, or if
// a call is made without credentials or for Poly and Fanvil (spec S-11).
func TestRedirectClients(t *testing.T) {
	ctx := context.Background()
	var col collector

	t.Run("snom rest", func(t *testing.T) {
		f := newSnomFake(t)
		hc, _ := fakeHTTP(t, f)
		c, err := New(prov.Snom, snomCreds(), nil, hc)
		if err != nil {
			t.Fatal(err)
		}
		if caps := c.Capabilities(); !caps.Supported || !caps.RegistersURL || caps.NeedsSerial {
			t.Fatalf("caps = %+v", caps)
		}
		if err := col.err(c.Check(ctx)); err != nil {
			t.Fatal(err)
		}
		if err := col.err(c.Register(ctx, macColon, "", phoneURL)); err != nil {
			t.Fatal(err)
		}
		var ep struct {
			MAC      string `json:"mac"`
			Auto     bool   `json:"autoprovisioning_enabled"`
			Settings map[string]struct {
				Value string            `json:"value"`
				Attrs map[string]string `json:"attrs"`
			} `json:"settings_manager"`
		}
		if err := json.Unmarshal(f.eps[macPlain], &ep); err != nil {
			t.Fatalf("endpoint %s not created: %v", macPlain, err)
		}
		if ep.MAC != macPlain || !ep.Auto || ep.Settings["u-server"].Value != phoneURL {
			t.Fatalf("endpoint = %+v, want setting_server (u-server, found on page 2) = the phone URL", ep)
		}
		u, found, err := c.Lookup(ctx, macPlain)
		if col.err(err) != nil || !found || u != phoneURL {
			t.Fatalf("lookup = %q %v %v", u, found, err)
		}
		rotated := strings.Replace(phoneURL, "tokentokentoken", "newtokennewtoken", 1)
		if err := col.err(c.Register(ctx, macPlain, "", rotated)); err != nil {
			t.Fatal(err)
		}
		if u, _, _ := c.Lookup(ctx, macPlain); u != rotated {
			t.Fatalf("after rotation lookup = %q, want the new URL", u)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.eps[macPlain]; ok {
			t.Fatal("unregister left the endpoint")
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil {
			t.Fatalf("unregistering an unknown MAC = %v, want nil", err)
		}
		if _, found, err := c.Lookup(ctx, macPlain); found || err != nil {
			t.Fatalf("lookup after delete = %v %v", found, err)
		}

		bad := Credentials{KeySnomKeyID: "kid-snom", KeySnomKeySecret: secretVal("wrong")}
		cb, _ := New(prov.Snom, bad, nil, hc)
		if err := col.err(cb.Check(ctx)); err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("check with a wrong secret = %v, want the 401", err)
		}

		for _, link := range []string{"company", "endpoints"} {
			f2 := newSnomFake(t)
			f2.foreign = link
			hc2, _ := fakeHTTP(t, f2)
			c2, _ := New(prov.Snom, snomCreds(), nil, hc2)
			if err := col.err(c2.Register(ctx, macPlain, "", phoneURL)); err == nil || !strings.Contains(err.Error(), link+" link") {
				t.Fatalf("a %s link to another host: %v, want it refused", link, err)
			}
		}
	})

	t.Run("snom xmlrpc", func(t *testing.T) {
		f := newSnomFake(t)
		hc, _ := fakeHTTP(t, f)
		c, err := New(prov.Snom, snomCreds(), []byte(`{"api":"xmlrpc"}`), hc)
		if err != nil {
			t.Fatal(err)
		}
		if err := col.err(c.Check(ctx)); err != nil {
			t.Fatal(err)
		}
		if err := col.err(c.Register(ctx, macColon, "", phoneURL)); err != nil {
			t.Fatal(err)
		}
		if f.rpc["000413AABBCC"] != phoneURL {
			t.Fatalf("registerPhone not called with the uppercase MAC and URL: %v", f.rpcCalls)
		}
		if u, found, err := c.Lookup(ctx, macPlain); col.err(err) != nil || !found || u != phoneURL {
			t.Fatalf("lookup = %q %v %v", u, found, err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil || len(f.rpc) != 0 {
			t.Fatalf("unregister = %v, left %v", err, f.rpc)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil {
			t.Fatalf("unregistering an unknown MAC = %v", err)
		}
		if _, err := New(prov.Snom, snomCreds(), []byte(`{"api":"soap"}`), hc); err == nil {
			t.Fatal("an unknown api setting was accepted")
		}
		if _, err := New(prov.Snom, snomCreds(), []byte(`{"apu":"rest"}`), hc); err == nil {
			t.Fatal("an unknown settings key was accepted")
		}
	})

	t.Run("yealink rps", func(t *testing.T) {
		cr := rpsCreds()
		f := &rpsFake{key: cr[KeyYealinkKey], secret: cr[KeyYealinkSecret], servers: map[string]string{}, devices: map[string]map[string]string{}}
		hc, _ := fakeHTTP(t, f)
		c, err := New(prov.Yealink, cr, []byte(`{"serverName":"hello-test"}`), hc)
		if err != nil {
			t.Fatal(err)
		}
		if err := col.err(c.Check(ctx)); err != nil {
			t.Fatal(err)
		}
		if err := col.err(c.Register(ctx, macColon, "", phoneURL)); err != nil {
			t.Fatal(err)
		}
		d := f.devices[macPlain]
		if d["uniqueServerUrl"] != phoneURL || d["serverId"] != f.servers["hello-test"] || f.serverAdds != 1 {
			t.Fatalf("device = %v, servers = %v: want device/add with uniqueServerUrl under Hello's server", d, f.servers)
		}
		rotated := phoneURL + "?r=2"
		if err := col.err(c.Register(ctx, macPlain, "", rotated)); err != nil {
			t.Fatal(err)
		}
		if f.devices[macPlain]["uniqueServerUrl"] != rotated || f.serverAdds != 1 || !slices.Contains(f.calls, "device/edit") {
			t.Fatalf("rotation: device = %v, calls = %v; want device/edit and no second server", f.devices[macPlain], f.calls)
		}
		// A new client (a restart) finds the existing server by name.
		c2, _ := New(prov.Yealink, cr, []byte(`{"serverName":"hello-test"}`), hc)
		if err := col.err(c2.Register(ctx, "001565000001", "", phoneURL)); err != nil || f.serverAdds != 1 {
			t.Fatalf("second client: %v, server adds %d", err, f.serverAdds)
		}
		if u, found, err := c.Lookup(ctx, macPlain); col.err(err) != nil || !found || u != rotated {
			t.Fatalf("lookup = %q %v %v", u, found, err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil || f.devices[macPlain] != nil {
			t.Fatalf("unregister = %v", err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil {
			t.Fatalf("unregistering an unknown MAC = %v", err)
		}
		bad, _ := New(prov.Yealink, Credentials{KeyYealinkKey: cr[KeyYealinkKey], KeyYealinkSecret: secretVal("wrong")}, nil, hc)
		if err := col.err(bad.Check(ctx)); err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Fatalf("check with a wrong secret = %v", err)
		}
	})

	t.Run("yealink ymcs", func(t *testing.T) {
		cr := ymcsCreds()
		f := &ymcsFake{id: cr[KeyYMCSClientID], secret: cr[KeyYMCSClientSecret], devices: map[string]map[string]string{}}
		hc, _ := fakeHTTP(t, f)
		c, err := New(prov.Yealink, cr, []byte(`{"api":"ymcs"}`), hc)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Capabilities().NeedsSerial {
			t.Fatal("YMCS without macOnly must need the serial")
		}
		if err := col.err(c.Register(ctx, macPlain, "", phoneURL)); err == nil {
			t.Fatal("register without a serial succeeded")
		}
		if err := col.err(c.Register(ctx, macPlain, "SN123", phoneURL)); err != nil {
			t.Fatal(err)
		}
		if d := f.devices[macPlain]; d["sn"] != "SN123" || d["uniqueServerUrl"] != phoneURL {
			t.Fatalf("device = %v", d)
		}
		rotated := phoneURL + "?r=2"
		if err := col.err(c.Register(ctx, macPlain, "SN123", rotated)); err != nil || f.devices[macPlain]["uniqueServerUrl"] != rotated {
			t.Fatalf("re-register = %v, device %v", err, f.devices[macPlain])
		}
		if u, found, err := c.Lookup(ctx, macPlain); col.err(err) != nil || !found || u != rotated {
			t.Fatalf("lookup = %q %v %v", u, found, err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil || len(f.devices) != 0 {
			t.Fatalf("unregister = %v", err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil {
			t.Fatalf("unregistering an unknown MAC = %v", err)
		}
		if f.tokens != 1 {
			t.Fatalf("token fetched %d times, want once (cached)", f.tokens)
		}
		if _, err := New(prov.Yealink, Credentials{KeyYMCSClientID: "a", KeyYMCSClientSecret: "b", KeyYMCSRegion: "evil.example/"}, []byte(`{"api":"ymcs"}`), hc); err == nil {
			t.Fatal("a region that is not a region code was accepted")
		}
		mo, _ := New(prov.Yealink, cr, []byte(`{"api":"ymcs","macOnly":true}`), hc)
		if mo.Capabilities().NeedsSerial {
			t.Fatal("macOnly still needs the serial")
		}
	})

	t.Run("grandstream gdms", func(t *testing.T) {
		f := newGDMSFake()
		hc, _ := fakeHTTP(t, f)
		c, err := New(prov.Grandstream, gdmsCreds(), nil, hc)
		if err != nil {
			t.Fatal(err)
		}
		if caps := c.Capabilities(); caps.RegistersURL || !caps.NeedsSerial || !caps.Supported {
			t.Fatalf("caps = %+v", caps)
		}
		if err := col.err(c.Check(ctx)); err != nil {
			t.Fatal(err)
		}
		if err := col.err(c.Register(ctx, macPlain, "", phoneURL)); err == nil {
			t.Fatal("register without a serial succeeded")
		}
		if err := col.err(c.Register(ctx, macPlain, "SN9", phoneURL)); err != nil {
			t.Fatal(err)
		}
		d := f.devices["00:04:13:AA:BB:CC"]
		if d["sn"] != "SN9" || d["siteId"] != float64(3345) {
			t.Fatalf("device/add body = %v, want the colon MAC, the serial and the numeric site", d)
		}
		if err := col.err(c.Register(ctx, macPlain, "SN9", phoneURL)); err != nil {
			t.Fatalf("re-adding an existing device = %v, want registered", err)
		}
		if _, _, err := c.Lookup(ctx, macPlain); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("lookup = %v, want ErrUnsupported", err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil || len(f.devices) != 0 {
			t.Fatalf("unregister = %v", err)
		}
		if err := col.err(c.Unregister(ctx, macPlain)); err != nil {
			t.Fatalf("unregistering an unknown MAC = %v", err)
		}
		// A dropped connection must not leak the access token from the query.
		f.hangUpOn = "/oapi/v1.0.0/device/add"
		err = col.err(c.Register(ctx, macPlain, "SN9", phoneURL))
		if err == nil || strings.Contains(err.Error(), "gdms-tok-") || strings.Contains(err.Error(), "signature=") {
			t.Fatalf("transport error = %v, want an error without the access token", err)
		}
		bad := gdmsCreds()
		bad[KeyGDMSPassword] = secretVal("wrongpw")
		cb, _ := New(prov.Grandstream, bad, nil, hc)
		if err := col.err(cb.Check(ctx)); err == nil {
			t.Fatal("check with a wrong password succeeded")
		}
		col.assertClean(t, "gdms-tok-1", gdmsPassword(gdmsCreds()[KeyGDMSPassword]), gdmsPassword(bad[KeyGDMSPassword]))
	})

	t.Run("poly fanvil generic make no call", func(t *testing.T) {
		hc, rw := fakeHTTP(t, http.NotFoundHandler())
		for _, v := range []prov.Vendor{prov.Poly, prov.Fanvil, prov.Generic} {
			c, err := New(v, Credentials{"any": "thing"}, nil, hc)
			if err != nil {
				t.Fatal(err)
			}
			if c.Capabilities().Supported {
				t.Fatalf("%s supported", v)
			}
			_, _, lerr := c.Lookup(ctx, macPlain)
			for _, err := range []error{c.Check(ctx), c.Register(ctx, macPlain, "s", phoneURL), c.Unregister(ctx, macPlain), lerr} {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("%s: %v, want ErrUnsupported", v, err)
				}
			}
		}
		if rw.n.Load() != 0 {
			t.Fatalf("%d requests made for unsupported vendors", rw.n.Load())
		}
	})

	t.Run("missing credentials are named", func(t *testing.T) {
		_, err := New(prov.Grandstream, Credentials{KeyGDMSClientID: "x"}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), KeyGDMSClientSecret) {
			t.Fatalf("err = %v, want the missing key named", err)
		}
	})

	t.Run("worker", func(t *testing.T) { testWorker(t, &col) })

	col.assertClean(t, credValues(snomCreds(), rpsCreds(), ymcsCreds(), gdmsCreds())...)
	col.assertClean(t, "tokentokentoken", "newtokennewtoken")
}

func testWorker(t *testing.T, col *collector) {
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	mkWorker := func(st Store, dep map[prov.Vendor]Credentials, hc *http.Client) *Worker {
		w := NewWorker(st, dep, col.logger())
		w.hc, w.now = hc, func() time.Time { return now }
		return w
	}

	t.Run("register and unregister", func(t *testing.T) {
		f := newSnomFake(t)
		hc, _ := fakeHTTP(t, f)
		st := newFakeStore()
		st.accounts[prov.Snom] = Account{Vendor: prov.Snom, Enabled: true, Credentials: snomCreds()}
		st.targets["snom/"+macPlain] = Target{MAC: macPlain, URL: phoneURL}
		st.jobs = []Job{{Seq: 1, Vendor: prov.Snom, MAC: macPlain, Op: OpRegister, FirstQueuedAt: now}}
		before := opsCount("snom", "register", "ok")
		mkWorker(st, nil, hc).work(ctx)
		if len(st.finished) != 1 || st.finished[0].Status.State != StateRegistered || st.finished[0].Job.Seq != 1 {
			t.Fatalf("finished = %+v", st.finished)
		}
		if f.eps[macPlain] == nil {
			t.Fatal("the vendor was not called")
		}
		if got := opsCount("snom", "register", "ok"); got != before+1 {
			t.Fatalf("ops metric = %v, want %v", got, before+1)
		}
		st.jobs = []Job{{Seq: 2, Vendor: prov.Snom, MAC: macPlain, Op: OpUnregister, FirstQueuedAt: now}}
		mkWorker(st, nil, hc).work(ctx)
		if f.eps[macPlain] != nil || len(st.finished) != 2 || st.finished[1].Status != (Status{}) {
			t.Fatalf("unregister: finished = %+v", st.finished)
		}
	})

	t.Run("failure backs off then fails", func(t *testing.T) {
		hc, _ := fakeHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		st := newFakeStore()
		st.accounts[prov.Snom] = Account{Vendor: prov.Snom, Enabled: true, Credentials: snomCreds()}
		st.targets["snom/"+macPlain] = Target{MAC: macPlain, URL: phoneURL}
		w := mkWorker(st, nil, hc)
		for i, c := range []struct {
			attempts int
			queued   time.Duration
			wantNext time.Duration
		}{{0, 0, 30 * time.Second}, {3, time.Hour, 4 * time.Minute}, {12, 20 * time.Hour, time.Hour}} {
			st.jobs = []Job{{Seq: int64(i), Vendor: prov.Snom, MAC: macPlain, Op: OpRegister, Attempts: c.attempts, FirstQueuedAt: now.Add(-c.queued)}}
			w.work(ctx)
			r := st.retried[len(st.retried)-1]
			if r.Next.Sub(now) != c.wantNext || r.Status.State != StatePending || !strings.Contains(r.Status.Reason, "503") {
				t.Fatalf("attempt %d: retry = %+v, want next in %v, pending with the vendor's error", c.attempts, r, c.wantNext)
			}
		}
		st.jobs = []Job{{Seq: 9, Vendor: prov.Snom, MAC: macPlain, Op: OpRegister, Attempts: 30, FirstQueuedAt: now.Add(-25 * time.Hour)}}
		w.work(ctx)
		if len(st.finished) != 1 || st.finished[0].Status.State != StateFailed || !strings.Contains(st.finished[0].Status.Reason, "503") {
			t.Fatalf("after 24 h: finished = %+v, want failed with the reason", st.finished)
		}
		for _, r := range st.retried {
			col.errs = append(col.errs, r.Status.Reason)
		}
	})

	t.Run("no credentials and unsupported make no call", func(t *testing.T) {
		hc, rw := fakeHTTP(t, http.NotFoundHandler())
		st := newFakeStore()
		st.accounts[prov.Yealink] = Account{Vendor: prov.Yealink, Enabled: false, Credentials: rpsCreds()}
		for _, v := range []prov.Vendor{prov.Snom, prov.Yealink, prov.Grandstream, prov.Poly, prov.Fanvil} {
			st.targets[string(v)+"/"+macPlain] = Target{MAC: macPlain, Serial: "S", URL: phoneURL}
			st.jobs = append(st.jobs, Job{Vendor: v, MAC: macPlain, Op: OpRegister, FirstQueuedAt: now})
		}
		mkWorker(st, nil, hc).work(ctx)
		if rw.n.Load() != 0 {
			t.Fatalf("%d requests made without credentials", rw.n.Load())
		}
		want := []State{StateNotConfigured, StateNotConfigured, StateNotConfigured, StateManual, StateManual}
		for i, f := range st.finished {
			if f.Status.State != want[i] {
				t.Fatalf("%s: status %q, want %q", f.Job.Vendor, f.Status.State, want[i])
			}
		}
		if len(st.finished) != len(want) {
			t.Fatalf("finished %d jobs, want %d", len(st.finished), len(want))
		}
	})

	t.Run("deployment credentials win and serial is required", func(t *testing.T) {
		f := newGDMSFake()
		hc, rw := fakeHTTP(t, f)
		st := newFakeStore()
		wrong := gdmsCreds()
		wrong[KeyGDMSPassword] = secretVal("stale")
		st.accounts[prov.Grandstream] = Account{Vendor: prov.Grandstream, Enabled: false, Credentials: wrong}
		st.targets["grandstream/"+macPlain] = Target{MAC: macPlain, URL: phoneURL}
		st.jobs = []Job{{Vendor: prov.Grandstream, MAC: macPlain, Op: OpRegister, FirstQueuedAt: now}}
		dep := Deployment(config.ProvRedirect{
			GDMSClientID: "gdms-client", GDMSClientSecret: secretVal("gdmsclient"), GDMSUsername: "admin@example.test",
			GDMSPassword: secretVal("gdmspw"), GDMSRegion: "eu", GDMSSiteID: "3345",
		})
		w := mkWorker(st, dep, hc)
		w.work(ctx)
		if rw.n.Load() != 0 || st.finished[0].Status.State != StateFailed || !strings.Contains(st.finished[0].Status.Reason, "serial") {
			t.Fatalf("no serial: %d calls, finished %+v", rw.n.Load(), st.finished)
		}
		st.targets["grandstream/"+macPlain] = Target{MAC: macPlain, Serial: "SN1", URL: phoneURL}
		st.jobs = []Job{{Vendor: prov.Grandstream, MAC: macPlain, Op: OpRegister, FirstQueuedAt: now}}
		w.work(ctx)
		if st.finished[1].Status.State != StateRegistered || len(f.devices) != 1 {
			t.Fatalf("deployment credentials not used: %+v", st.finished)
		}
	})

	t.Run("drift", func(t *testing.T) {
		f := newSnomFake(t)
		hc, _ := fakeHTTP(t, f)
		st := newFakeStore()
		st.accounts[prov.Snom] = Account{Vendor: prov.Snom, Enabled: true, Credentials: snomCreds()}
		c, _ := New(prov.Snom, snomCreds(), nil, hc)
		_ = c.Register(ctx, "000413000001", "", phoneURL)
		_ = c.Register(ctx, "000413000002", "", "https://changed.by.hand/")
		st.registered[prov.Snom] = []Target{
			{MAC: "000413000001", URL: phoneURL}, {MAC: "000413000002", URL: phoneURL}, {MAC: "000413000003", URL: phoneURL},
		}
		mkWorker(st, nil, hc).reconcile(ctx)
		if _, ok := st.statuses["000413000001"]; ok {
			t.Fatal("an unchanged registration was flagged")
		}
		for _, m := range []string{"000413000002", "000413000003"} {
			if s := st.statuses[m]; s.State != StateFailed || s.Reason != "drift" {
				t.Fatalf("%s: status %+v, want failed: drift", m, s)
			}
		}
	})

	t.Run("run stops with its context", func(t *testing.T) {
		st := newFakeStore()
		st.jobs = []Job{{Vendor: prov.Poly, MAC: macPlain, Op: OpRegister, FirstQueuedAt: now}}
		cctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { mkWorker(st, nil, nil).Run(cctx); close(done) }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st.mu.Lock()
			n := len(st.finished)
			st.mu.Unlock()
			if n == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Run did not process the due job")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	})
}

func opsCount(labels ...string) float64 {
	var m dto.Metric
	_ = OpsTotal.WithLabelValues(labels...).Write(&m)
	return m.GetCounter().GetValue()
}

// TestHawkVectors pins the Hawk implementation to the Hawk specification's
// published examples.
func TestHawkVectors(t *testing.T) {
	// The Hawk spec's published example credentials, joined so scanners do not flag them.
	hk := strings.Join([]string{"werxhqb98rpaxn", "39848xrunpaw3489ruxnpa98w4rxn"}, "")
	u, _ := url.Parse("http://example.com:8000/resource/1?b=1&a=2")
	if got := hawkMAC(hk, 1353832234, "j4h3g2", "GET", u, "", "some-app-ext-data"); got != "6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE=" {
		t.Fatalf("GET mac = %s", got)
	}
	hash := hawkPayloadHash("text/plain", []byte("Thank you for flying Hawk"))
	if got := hawkMAC(hk, 1353832234, "j4h3g2", "POST", u, hash, "some-app-ext-data"); got != "aSe1DERmZuRl3pI36/9BdZmnErTw3sNzOOAUlfeKjVw=" {
		t.Fatalf("POST mac = %s (hash %s)", got, hash)
	}
	h := hawkHeader("dh37fgj492je", hk, "POST", u, 1353832234, "j4h3g2", "text/plain", []byte("Thank you for flying Hawk"), "")
	if !strings.HasPrefix(h, `Hawk id="dh37fgj492je", ts="1353832234", nonce="j4h3g2", hash="`+hash+`"`) {
		t.Fatalf("header = %s", h)
	}
}

// TestRPSStringToSign pins the RPS signing string to the two worked
// examples of the RPS JSON API v3.6 guide (section 1.3.4).
func TestRPSStringToSign(t *testing.T) {
	// The guide's example values, assembled so scanners do not flag them.
	ak, md := "2df23f2d9c255e71"+"38dc603b3847b58a", "KOK7YpXasjJC"+"+MslP+MGWw=="
	n1, n2 := "b681e77450a04d22"+"aaffc914a3379561", "9e730a223b484337"+"85494801fb016d39"
	body := rpsStringToSign("POST", map[string]string{
		"Content-MD5": md, "X-Ca-Key": ak,
		"X-Ca-Nonce": n1, "X-Ca-Timestamp": "1544008291631",
	}, "/api/open/v1/server/list", nil)
	want := "POST\nContent-MD5:" + md + "\nX-Ca-Key:" + ak + "\n" +
		"X-Ca-Nonce:" + n1 + "\nX-Ca-Timestamp:1544008291631\napi/open/v1/server/list"
	if body != want {
		t.Fatalf("body form:\n%q\nwant\n%q", body, want)
	}
	q := rpsStringToSign("GET", map[string]string{
		"X-Ca-Key": ak, "X-Ca-Nonce": n2, "X-Ca-Timestamp": "1544094691000",
	}, "/api/open/v1/device/checkMac", url.Values{"mac": {"001565123123"}})
	want = "GET\nX-Ca-Key:" + ak + "\nX-Ca-Nonce:" + n2 + "\n" +
		"X-Ca-Timestamp:1544094691000\napi/open/v1/device/checkMac\nmac=001565123123"
	if q != want {
		t.Fatalf("query form:\n%q\nwant\n%q", q, want)
	}
	if got := rpsStringToSign("GET", nil, "/x", url.Values{"b": {"2"}, "a": {""}}); got != "GET\nx\na&b=2" {
		t.Fatalf("empty value form = %q, want the key alone", got)
	}
}

// TestGDMSSignature pins the GDMS signature to the guide's construction.
func TestGDMSSignature(t *testing.T) {
	body := []byte(`[{"mac":"00:0B:82:00:00:01"}]`)
	p := map[string]string{"timestamp": "1621958396000", "access_token": "tok", "client_secret": "cs", "client_id": "ci"}
	want := sha256h("&access_token=tok&client_id=ci&client_secret=cs&timestamp=1621958396000&" + sha256h(string(body)) + "&")
	if got := gdmsSign(p, body); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	if got := gdmsPassword("pw"); got != sha256h(fmt.Sprintf("%x", md5.Sum([]byte("pw")))) { //nolint:gosec // the guide's encoding
		t.Fatalf("password encoding = %s", got)
	}
	if gdmsMAC("000b82aabbcc") != "00:0B:82:AA:BB:CC" {
		t.Fatalf("mac = %s", gdmsMAC("000b82aabbcc"))
	}
}

// TestDeployment fails if a vendor's deployment credentials are not mapped
// to the S-11 keys, or a vendor without them appears.
func TestDeployment(t *testing.T) {
	d := Deployment(config.ProvRedirect{SnomKeyID: "a", SnomKeySecret: "b", YMCSClientID: "c", YMCSClientSecret: "d", YMCSRegion: "eu"})
	if d[prov.Snom][KeySnomKeyID] != "a" || d[prov.Snom][KeySnomKeySecret] != "b" {
		t.Fatalf("snom = %v", d[prov.Snom])
	}
	if y := d[prov.Yealink]; y[KeyYMCSClientID] != "c" || y[KeyYMCSRegion] != "eu" || y[KeyYealinkKey] != "" {
		t.Fatalf("yealink = %v", y)
	}
	if _, ok := d[prov.Grandstream]; ok {
		t.Fatal("grandstream without credentials")
	}
	if len(Deployment(config.ProvRedirect{})) != 0 {
		t.Fatal("an empty deployment has credentials")
	}
}

// TestCleanRedacts fails if a vendor message echoing a credential reaches
// an error, or a cancelled call stops matching context.Canceled.
func TestCleanRedacts(t *testing.T) {
	s := secretVal("echo")
	c := conn{vendor: prov.Snom, secrets: []string{s}}
	if err := c.clean("check", errors.New("invalid key "+s)); strings.Contains(err.Error(), s) {
		t.Fatalf("error = %v", err)
	}
	if err := c.clean("check", fmt.Errorf("x: %w", context.Canceled)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if err := c.clean("check", errors.New("tok-abcdef")); err.Error() != "redirect snom check: tok-abcdef" {
		t.Fatalf("error = %v", err)
	}
}
