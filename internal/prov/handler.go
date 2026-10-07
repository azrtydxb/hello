package prov

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MaxUpload is how much of a discarded upload is read (spec S-4).
const MaxUpload = 1 << 20

// RetryAfter is the Retry-After of a 503 while PostgreSQL or MinIO is down.
const RetryAfter = "300"

// Options configure the provisioning handler (config.Prov).
type Options struct {
	// PublicURL is the https:// base the phones reach the listener at; the
	// boot and CA URLs use its host over plain HTTP.
	PublicURL *url.URL
	// TrustedProxies may set X-Forwarded-Proto and X-Forwarded-For.
	TrustedProxies []netip.Prefix
	// BootCIDRs limits the trust-on-first-use hand-off; empty allows any.
	BootCIDRs []netip.Prefix
	// CACertFile is the PEM served at /p/ca.crt (read per request, so a
	// renewed CA is served without a restart); empty serves none.
	CACertFile string
	// Resync is the re-check interval the boot bodies set.
	Resync  time.Duration
	Audit   *Audit
	Metrics *Metrics
	Log     *slog.Logger
}

type handler struct {
	s   Store
	l   *Limiter
	o   Opener
	opt Options
}

// NewHandler returns the provisioning listener's handler (spec S-4): the
// per-device, boot, CA and firmware paths, each request rate-limited,
// audited and counted. Every denial is an empty 404.
func NewHandler(s Store, l *Limiter, o Opener, opt Options) http.Handler {
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	if opt.Metrics == nil {
		opt.Metrics = NewMetrics(prometheus.NewRegistry())
	}
	if opt.Audit == nil {
		opt.Audit = NewAudit(s, opt.Metrics, opt.Log)
	}
	return &handler{s: s, l: l, o: o, opt: opt}
}

// event is one request's audit row and metric labels.
type event struct {
	rec    FetchRecord
	vendor string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip, https := h.client(r)
	ev := &event{vendor: "unknown", rec: FetchRecord{
		At: time.Now(), IP: ip, UserAgent: truncateUA(r.UserAgent()),
		PathRedacted: RedactPath(r.URL.Path), Kind: KindOther,
	}}
	defer h.finish(ev)
	ctx := r.Context()
	if !h.l.Request(ctx, ip) {
		h.empty(w, ev, http.StatusTooManyRequests, ResultRateLimited)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/p/")
	readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
	allowed := readOnly || r.Method == http.MethodPut
	if !ok || !allowed {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	seg, name, _ := strings.Cut(rest, "/")
	switch {
	case !readOnly && (seg == "boot" || seg == "ca.crt" || seg == "ca.der"):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
	case rest == "ca.crt" || rest == "ca.der":
		h.serveCA(w, r, ev, rest)
	case seg == "boot":
		h.serveBoot(ctx, w, ev, name)
	case strings.HasPrefix(name, "fw/") && readOnly:
		h.serveFirmware(w, r, ev, seg, strings.TrimPrefix(name, "fw/"), https)
	default:
		h.serveDevice(w, r, ev, seg, name, https)
	}
}

func (h *handler) finish(ev *event) {
	h.opt.Metrics.Requests.WithLabelValues(ev.vendor, string(ev.rec.Kind), string(ev.rec.Result)).Inc()
	h.opt.Audit.Record(ev.rec)
}

// empty answers a status with no body.
func (h *handler) empty(w http.ResponseWriter, ev *event, status int, res Result) {
	ev.rec.Status, ev.rec.Result = status, res
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", RetryAfter)
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// deny answers a denial: an empty 404 that counts towards the source's
// block.
func (h *handler) deny(ctx context.Context, w http.ResponseWriter, ev *event, res Result) {
	h.l.Denied(ctx, ev.rec.IP)
	h.empty(w, ev, http.StatusNotFound, res)
}

func (h *handler) outage(w http.ResponseWriter, ev *event, what string, err error) {
	h.opt.Log.Error("provisioning: store unavailable", "op", what, "path", ev.rec.PathRedacted, "error", err)
	h.empty(w, ev, http.StatusServiceUnavailable, ResultUnavailable)
}

// client returns the client IP and whether the request arrived over HTTPS:
// TLS on the listener, or X-Forwarded-Proto: https from a trusted proxy.
// Behind trusted proxies the client is the last X-Forwarded-For hop that
// is not itself a trusted proxy.
func (h *handler) client(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, r.TLS != nil
	}
	ip := ap.Addr().Unmap()
	https := r.TLS != nil
	if !inPrefixes(ip, h.opt.TrustedProxies) {
		return ip, https
	}
	if protos := splitHeader(r.Header.Values("X-Forwarded-Proto")); len(protos) > 0 && strings.EqualFold(protos[len(protos)-1], "https") {
		https = true
	}
	hops := splitHeader(r.Header.Values("X-Forwarded-For"))
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			break
		}
		ip = a.Unmap()
		if !inPrefixes(ip, h.opt.TrustedProxies) {
			break
		}
	}
	return ip, https
}

func splitHeader(vals []string) []string {
	var out []string
	for _, v := range vals {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func inPrefixes(ip netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// authorize checks a per-device request (spec S-6): a known token
// (current or in grace), HTTPS, and an allowlisted phone.
func (h *handler) authorize(w http.ResponseWriter, r *http.Request, ev *event, token string, https bool) (PhoneRecord, bool) {
	ctx := r.Context()
	if !tokenRE.MatchString(token) {
		h.deny(ctx, w, ev, ResultUnknownToken)
		return PhoneRecord{}, false
	}
	rec, err := h.s.PhoneByToken(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		h.deny(ctx, w, ev, ResultUnknownToken)
		return PhoneRecord{}, false
	}
	if err != nil {
		h.outage(w, ev, "phone by token", err)
		return PhoneRecord{}, false
	}
	ev.rec.PhoneID, ev.vendor = rec.ID, string(rec.Vendor)
	if !https {
		// The token crossed the network in clear: flag it for rotation.
		if err := h.s.FlagTokenExposed(ctx, rec.ID); err != nil {
			h.opt.Log.Error("provisioning: flag token exposed", "phone", rec.ID, "error", err)
		}
		h.deny(ctx, w, ev, ResultPlainHTTP)
		return PhoneRecord{}, false
	}
	if !rec.Allowlisted {
		h.deny(ctx, w, ev, ResultNotAllowlisted)
		return PhoneRecord{}, false
	}
	return rec, true
}

func (h *handler) serveDevice(w http.ResponseWriter, r *http.Request, ev *event, token, name string, https bool) {
	ctx := r.Context()
	rec, ok := h.authorize(w, r, ev, token, https)
	if !ok {
		return
	}
	kind, claimed, ok := classify(rec.Vendor, rec.Model, name)
	if r.Method == http.MethodPut && !ok && safeName(name) {
		// Phones upload logs and overrides under names outside their file
		// set; any is discarded, as long as it names no other MAC.
		kind, claimed, ok = KindUpload, genericMAC(strings.ToLower(name)), true
	}
	ev.rec.MACClaimed = claimed
	if ok {
		ev.rec.Kind = kind
	}
	if claimed != "" && claimed != rec.MAC {
		h.deny(ctx, w, ev, ResultMACMismatch)
		return
	}
	ua := r.UserAgent()
	if m := uaMAC(ua); m != "" && m != rec.MAC {
		ev.rec.UAMismatch = true
	}
	if !ok {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	if r.Method == http.MethodPut {
		ev.rec.Kind = KindUpload
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, MaxUpload))
		ev.rec.Bytes = n
		h.empty(w, ev, http.StatusNoContent, ResultUploadDiscarded)
		return
	}
	if kind == KindUpload || kind == KindOther {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	if !h.l.Phone(ctx, rec.ID) {
		h.empty(w, ev, http.StatusTooManyRequests, ResultRateLimited)
		return
	}
	data, tmpl, err := h.s.RenderInputs(ctx, rec.ID)
	switch {
	case errors.Is(err, ErrNoTemplate):
		h.empty(w, ev, http.StatusNotFound, ResultNoTemplate)
		return
	case errors.Is(err, ErrSealed):
		h.opt.Log.Error("provisioning: a sealed value of the phone does not open under HELLO_SECRET_KEY", "phone", rec.ID)
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	case errors.Is(err, ErrNotFound):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	case err != nil:
		h.outage(w, ev, "render inputs", err)
		return
	}
	file, ok := FileFor(tmpl, name, data.Phone)
	if !ok {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	start := time.Now()
	body, err := renderBody(ctx, file.Body, data)
	h.opt.Metrics.RenderSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		h.opt.Log.Error("provisioning: render failed", "phone", rec.ID, "template", tmpl.Name, "file", name, "error", err)
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	}
	etag := etagOf(body)
	st := FetchState{At: ev.rec.At, IP: ev.rec.IP, UserAgent: ev.rec.UserAgent, File: name, FirmwareSeen: uaFirmware(ua), UAMismatch: ev.rec.UAMismatch}
	hash := HashToken(token)
	if err := h.s.MarkFetched(ctx, rec.ID, hash, st); err != nil {
		h.outage(w, ev, "mark fetched", err)
		return
	}
	if !rec.ViaPrevious && rec.HasPrevious {
		if err := h.s.PromoteToken(ctx, rec.ID, hash); err != nil {
			h.opt.Log.Error("provisioning: promote token", "phone", rec.ID, "error", err)
		}
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		ev.rec.Status, ev.rec.Result = http.StatusNotModified, ResultNotModified
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.body(w, ev, ResultServed, file.ContentType, body)
}

func (h *handler) body(w http.ResponseWriter, ev *event, res Result, ctype string, body []byte) {
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	ev.rec.Status, ev.rec.Result, ev.rec.Bytes = http.StatusOK, res, int64(len(body))
}

func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func etagMatch(header, etag string) bool {
	for _, t := range splitHeader([]string{header}) {
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// uaMACRE finds a MAC in a User-Agent: 12 hex digits, plain or in pairs
// separated by ':' or '-' (Yealink, Grandstream, Fanvil; spec S-6).
var uaMACRE = regexp.MustCompile(`(?i)(?:^|[^0-9a-f:-])((?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}|[0-9a-f]{12})(?:$|[^0-9a-f:-])`)

func uaMAC(ua string) string {
	m := uaMACRE.FindStringSubmatch(ua)
	if m == nil {
		return ""
	}
	mac, err := NormalizeMAC(m[1])
	if err != nil || mac == zeroMAC {
		return ""
	}
	return mac
}

// uaVersionRE is the firmware version vendors put in the User-Agent: the
// first dotted number of at least three parts ("96.86.0.70", "1.0.9.69").
var uaVersionRE = regexp.MustCompile(`\b\d+(?:\.\d+){2,}\b`)

func uaFirmware(ua string) string {
	v := uaVersionRE.FindString(ua)
	if len(v) > 64 {
		return ""
	}
	return v
}

func (h *handler) serveFirmware(w http.ResponseWriter, r *http.Request, ev *event, token, file string, https bool) {
	ctx := r.Context()
	ev.rec.Kind = KindFirmware
	rec, ok := h.authorize(w, r, ev, token, https)
	if !ok {
		return
	}
	if !safeName(file) {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	fw, err := h.s.FirmwareByName(ctx, rec.Vendor, file)
	if errors.Is(err, ErrNotFound) {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	if err != nil {
		h.outage(w, ev, "firmware by name", err)
		return
	}
	f, err := h.o.OpenFirmware(ctx, fw.ObjectKey)
	if err != nil {
		h.outage(w, ev, "open firmware", err)
		return
	}
	defer func() { _ = f.Close() }()
	cw := &countingWriter{ResponseWriter: w}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+fw.SHA256+`"`)
	http.ServeContent(cw, r, fw.Filename, fw.UploadedAt, f)
	ev.rec.Status, ev.rec.Bytes = cw.status, cw.n
	switch {
	case cw.status == http.StatusNotModified:
		ev.rec.Result = ResultNotModified
	case cw.status < 300:
		ev.rec.Result = ResultServed
	default:
		ev.rec.Result = ResultNotFound
	}
	h.opt.Metrics.FirmwareBytes.WithLabelValues(string(rec.Vendor)).Add(float64(cw.n))
}

type countingWriter struct {
	http.ResponseWriter
	status int
	n      int64
}

func (c *countingWriter) WriteHeader(s int) {
	c.status = s
	c.ResponseWriter.WriteHeader(s)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	return n, err
}

// serveCA serves Hello's provisioning CA, PEM at ca.crt (DER when the
// client asks for application/pkix-cert) and DER at ca.der, over plain
// HTTP too: it is public, and phones need it before they trust HTTPS.
func (h *handler) serveCA(w http.ResponseWriter, r *http.Request, ev *event, name string) {
	ev.rec.Kind = KindCA
	if h.opt.CACertFile == "" {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	block, err := ReadCACert(h.opt.CACertFile)
	if err != nil {
		h.opt.Log.Error("provisioning: the CA certificate file is unreadable", "file", h.opt.CACertFile, "error", err)
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	if name == "ca.der" || strings.Contains(r.Header.Get("Accept"), "application/pkix-cert") {
		h.body(w, ev, ResultServed, "application/pkix-cert", block.Bytes)
		return
	}
	h.body(w, ev, ResultServed, "application/x-pem-file", pem.EncodeToMemory(block))
}

// serveBoot is the DHCP bootstrap path (spec S-10). A common name gets the
// vendor's boot body (CA, re-check, the boot URL); the per-MAC device name
// of an armed, allowlisted phone gets the one-time hand-off of its own
// HTTPS URL. Nothing here ever carries a SIP secret or an admin password:
// the data rendered holds none.
func (h *handler) serveBoot(ctx context.Context, w http.ResponseWriter, ev *event, name string) {
	ev.rec.Kind = KindBoot
	v, _, claimed, ok := bootClassify(name)
	if !ok {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	ev.rec.MACClaimed = claimed
	if claimed == "" {
		ev.vendor = string(v)
		bb, ok := bootBodies[v]
		if !ok {
			h.empty(w, ev, http.StatusNotFound, ResultNotFound)
			return
		}
		body := bb.common
		if bb.master != "" {
			body = bb.master
		}
		h.bootBody(ctx, w, ev, ResultBootServed, bb.ctype, body, Phone{}, h.bootInfo())
		return
	}
	rec, err := h.s.PhoneByMAC(ctx, claimed)
	if errors.Is(err, ErrNotFound) {
		h.deny(ctx, w, ev, ResultNotAllowlisted)
		return
	}
	if err != nil {
		h.outage(w, ev, "phone by mac", err)
		return
	}
	ev.rec.PhoneID, ev.vendor = rec.ID, string(rec.Vendor)
	kind, ok := MatchFile(rec.Vendor, rec.Model, rec.MAC, name)
	bb, known := bootBodies[rec.Vendor]
	if !ok || !known || (kind != KindDevice && kind != KindMaster) {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	phone := Phone{MAC: rec.MAC, MACUpper: strings.ToUpper(rec.MAC), Vendor: rec.Vendor, Model: rec.Model}
	if kind == KindMaster { // Poly's master names the device file; no claim yet
		h.bootBody(ctx, w, ev, ResultBootServed, bb.ctype, bb.master, phone, h.bootInfo())
		return
	}
	if len(h.opt.BootCIDRs) > 0 && !inPrefixes(ev.rec.IP, h.opt.BootCIDRs) {
		h.deny(ctx, w, ev, ResultBootDenied)
		return
	}
	rec, handoff, err := h.s.ClaimBoot(ctx, rec.MAC)
	switch {
	case errors.Is(err, ErrNotFound):
		h.deny(ctx, w, ev, ResultNotAllowlisted)
		return
	case errors.Is(err, ErrSealed):
		h.opt.Log.Error("provisioning: the phone's token does not open under HELLO_SECRET_KEY", "phone", ev.rec.PhoneID)
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	case err != nil:
		h.outage(w, ev, "claim boot", err)
		return
	case handoff == nil && rec.BootArmed:
		h.deny(ctx, w, ev, ResultNotAllowlisted)
		return
	case handoff == nil:
		h.deny(ctx, w, ev, ResultBootReclaim)
		return
	}
	h.bootBody(ctx, w, ev, ResultBootHandoff, bb.ctype, bb.common, phone, *handoff)
}

func (h *handler) bootInfo() ProvInfo {
	if h.opt.PublicURL == nil {
		return ProvInfo{}
	}
	return ProvInfo{URL: BootURL(h.opt.PublicURL), CAURL: CAURL(h.opt.PublicURL), ResyncSeconds: ResyncSeconds("", h.opt.Resync),
		CACertPEM: CACertPEM(h.opt.CACertFile)}
}

// ReadCACert reads the CA certificate file's first PEM block, which must
// be a CERTIFICATE.
func ReadCACert(path string) (*pem.Block, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the operator's HELLO_PROV_CA_CERT
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("prov: the CA file holds no PEM certificate")
	}
	return block, nil
}

// CACertPEM is ProvInfo.CACertPEM for the CA file at path: read on each
// call, so a renewed CA reaches the next render without a restart; empty
// when path is empty or unreadable.
func CACertPEM(path string) string {
	if path == "" {
		return ""
	}
	block, err := ReadCACert(path)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(string(pem.EncodeToMemory(block)), "\n")
}

func (h *handler) bootBody(ctx context.Context, w http.ResponseWriter, ev *event, res Result, ctype, body string, p Phone, info ProvInfo) {
	out, err := renderBody(ctx, body, RenderData{Phone: p, Prov: info})
	if err != nil {
		h.opt.Log.Error("provisioning: boot body render failed", "vendor", ev.vendor, "error", err)
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.body(w, ev, res, ctype, out)
}
