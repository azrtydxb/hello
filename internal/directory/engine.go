package directory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Store is the engine's persistence. The PostgreSQL implementation is PG; Mem
// serves the tests.
type Store interface {
	ListConnectors(ctx context.Context) ([]Connector, error)
	GetConnector(ctx context.Context, id int64) (Connector, error)
	// SaveConnector inserts (c.ID == 0) or updates a connector. A non-nil
	// secret replaces the sealed credential and sets its fingerprint to fp.
	SaveConnector(ctx context.Context, actor string, c Connector, secret *string, fp string) (Connector, error)
	// DeleteConnector removes a connector. While users are linked it fails
	// with ErrHasUsers, unless release is set, which turns them into local
	// users first.
	DeleteConnector(ctx context.Context, actor string, id int64, release bool) error
	// Secret opens the connector's credential.
	Secret(ctx context.Context, id int64) (string, error)
	RecordOutcome(ctx context.Context, id int64, ok bool, code, detail string, at time.Time) (failures int, err error)
	Audit(ctx context.Context, actor, action, resource, id string) error

	CreateRun(ctx context.Context, r Run) (Run, error)
	GetRun(ctx context.Context, id int64) (Run, error)
	ListRuns(ctx context.Context, connectorID int64, limit int) ([]Run, error)
	UpdateRun(ctx context.Context, r Run) error
	// ClaimQueued moves the oldest queued run of the connector to running.
	ClaimQueued(ctx context.Context, connectorID int64, replica string) (*Run, error)
	// CloseOrphans fails the runs left running by a dead replica.
	CloseOrphans(ctx context.Context, connectorID int64) (int, error)
	// LastScheduled is when the connector's latest scheduled run was created.
	LastScheduled(ctx context.Context, connectorID int64) (time.Time, bool, error)
	// HasApplied reports whether an apply or scheduled run ever succeeded.
	HasApplied(ctx context.Context, connectorID int64) (bool, error)
	ReplaceChanges(ctx context.Context, runID int64, cs []Change) error
	ListChanges(ctx context.Context, runID int64, f ChangeFilter) ([]Change, error)

	LoadState(ctx context.Context, connectorID int64) (State, error)
	// Apply performs one operation, audited with actor.
	Apply(ctx context.Context, actor string, c Connector, op Op) error
	PruneChanges(ctx context.Context, before time.Time) (int64, error)
	TryLock(ctx context.Context, key string) (unlock func(), ok bool, err error)
}

// ChangeRetention is how long sync changes are kept (spec Data).
const ChangeRetention = 90 * 24 * time.Hour

// Engine runs connectors. One runs on every control replica; a connector runs
// on the replica that holds its advisory lock (I-13).
type Engine struct {
	Store   Store
	Drivers map[string]Driver
	Metrics *Metrics
	Log     *slog.Logger
	// Replica names this replica in run rows.
	Replica string
	// AllowPlaintext is HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP.
	AllowPlaintext bool
	// Allow are the private ranges connections may reach.
	Allow []netip.Prefix
	// Dial replaces the guarded dialer (tests only).
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Notify is told after the third consecutive failure of a connector (an
	// administrator e-mail when SMTP is set); it must not block.
	Notify func(ctx context.Context, c Connector, code string)
	// Now replaces the clock in tests.
	Now func() time.Time
	// Tick is the scheduler's period (30 s when zero).
	Tick time.Duration

	wake chan struct{}
	once sync.Once
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) log() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Log
}

func (e *Engine) init() { e.once.Do(func() { e.wake = make(chan struct{}, 1) }) }

// Kick wakes the scheduler, so a queued run starts at once.
func (e *Engine) Kick() {
	e.init()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func lockKey(id int64) string { return "hello:dirsync:" + strconv.FormatInt(id, 10) }

func actorOf(c Connector) string { return "directory:" + c.Name }

// Fingerprint is the short fingerprint of what a sealed secret is bound to.
func Fingerprint(identity string) string {
	h := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(h[:8])
}

func (e *Engine) driver(kind string) (Driver, error) {
	d, ok := e.Drivers[kind]
	if !ok {
		return nil, fieldErr("kind", "unsupported connector kind")
	}
	return d, nil
}

// ConnectorInput is a create or a patch: on create nil takes the default, on
// update nil keeps the stored value.
type ConnectorInput struct {
	Kind            string
	Name            *string
	Enabled         *bool
	Mode            *string
	ScheduleMinutes *int
	Config          json.RawMessage
	Mappings        *Mappings
	GroupRules      *GroupRules
	Options         *Options
	AutoProvision   json.RawMessage
	// Secret is the bind password: write-only, sealed by the store.
	Secret *string
}

// Create validates and stores a connector, disabled unless asked.
func (e *Engine) Create(ctx context.Context, actor string, in ConnectorInput) (Connector, error) {
	d, err := e.driver(in.Kind)
	if err != nil {
		return Connector{}, err
	}
	c := Connector{Kind: in.Kind, Mode: ModeAuthoritative, ScheduleMinutes: 60, Mappings: d.DefaultMappings(),
		GroupRules: GroupRules{Roles: []RoleRule{}, RingGroups: []RingGroupRule{}}, AutoProvision: json.RawMessage(`{}`)}
	return e.save(ctx, actor, d, c, in)
}

// Update applies a patch.
func (e *Engine) Update(ctx context.Context, actor string, id int64, in ConnectorInput) (Connector, error) {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return Connector{}, err
	}
	d, err := e.driver(c.Kind)
	if err != nil {
		return Connector{}, err
	}
	return e.save(ctx, actor, d, c, in)
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func (e *Engine) save(ctx context.Context, actor string, d Driver, c Connector, in ConnectorInput) (Connector, error) {
	var v ValidationError
	oldIdentity := ""
	if c.ID != 0 {
		oldIdentity = d.Identity(c.Config)
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if n := len([]rune(c.Name)); n < 1 || n > 100 || !printable(c.Name) {
		v.Fields = append(v.Fields, FieldError{"name", "must be 1-100 printable characters"})
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Mode != nil {
		c.Mode = *in.Mode
	}
	switch c.Mode {
	case ModeAuthoritative, ModeCreateOnly, ModeLinkOnly:
	default:
		v.Fields = append(v.Fields, FieldError{"mode", "must be authoritative, create_only or link_only"})
	}
	if in.ScheduleMinutes != nil {
		c.ScheduleMinutes = *in.ScheduleMinutes
	}
	if c.ScheduleMinutes != 0 && (c.ScheduleMinutes < 5 || c.ScheduleMinutes > 10080) {
		v.Fields = append(v.Fields, FieldError{"scheduleMinutes", "must be 0 (manual only) or 5 to 10080"})
	}
	if in.Mappings != nil {
		c.Mappings = *in.Mappings
	}
	if mv := c.Mappings.Validate(); mv != nil {
		v.Fields = append(v.Fields, mv.Fields...)
	}
	c.Mappings = c.Mappings.Normalize()
	if in.GroupRules != nil {
		c.GroupRules = *in.GroupRules
	}
	if c.GroupRules.Roles == nil {
		c.GroupRules.Roles = []RoleRule{}
	}
	if c.GroupRules.RingGroups == nil {
		c.GroupRules.RingGroups = []RingGroupRule{}
	}
	if gv := c.GroupRules.Validate(); gv != nil {
		v.Fields = append(v.Fields, gv.Fields...)
	}
	if in.Options != nil {
		c.Options = *in.Options
	}
	o := c.Options
	if o.AbsenceRuns < 0 || o.AbsenceRuns > 10 {
		v.Fields = append(v.Fields, FieldError{"options.absenceRuns", "must be 1 to 10"})
	}
	if o.UserCap < 0 || o.UserCap > MaxUserCap {
		v.Fields = append(v.Fields, FieldError{"options.userCap", "must be at most " + strconv.Itoa(MaxUserCap)})
	}
	if o.RunTimeoutMinutes < 0 || o.RunTimeoutMinutes > 240 {
		v.Fields = append(v.Fields, FieldError{"options.runTimeoutMinutes", "must be 1 to 240"})
	}
	if len(in.AutoProvision) > 0 {
		var ap struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(in.AutoProvision, &ap); err != nil && string(in.AutoProvision) != "null" {
			v.Fields = append(v.Fields, FieldError{"autoProvision", "must be an object"})
		} else if ap.Enabled {
			v.Fields = append(v.Fields, FieldError{"autoProvision.enabled", "range auto-provisioning is not available yet"})
		}
	}
	if in.Config != nil {
		c.Config = in.Config
	}
	cfg, cerr := d.Validate(c.Config, ValidateOptions{AllowPlaintext: e.AllowPlaintext, GroupRules: len(c.GroupRules.RingGroups)+len(c.GroupRules.Roles) > 0})
	if cerr != nil {
		var ve *ValidationError
		if errors.As(cerr, &ve) {
			v.Fields = append(v.Fields, ve.Fields...)
		} else {
			return Connector{}, cerr
		}
	} else {
		c.Config = cfg
	}
	fp := ""
	if in.Secret != nil {
		if *in.Secret == "" {
			v.Fields = append(v.Fields, FieldError{"bindPassword", "must not be empty: an empty password is an anonymous bind"})
		}
	}
	if cerr == nil {
		fp = Fingerprint(d.Identity(c.Config))
		if c.ID != 0 && in.Secret == nil && c.HasCredential && Fingerprint(oldIdentity) != fp {
			v.Fields = append(v.Fields, FieldError{"bindPassword", "enter the bind password again: the host or bind DN changed"})
		}
		if c.Enabled && !c.HasCredential && in.Secret == nil {
			v.Fields = append(v.Fields, FieldError{"enabled", "set the bind password before enabling the connector"})
		}
	}
	if len(v.Fields) > 0 {
		return Connector{}, &v
	}
	if len(c.AutoProvision) == 0 {
		c.AutoProvision = json.RawMessage(`{}`)
	}
	return e.Store.SaveConnector(ctx, actor, c, in.Secret, fp)
}

// Get returns a connector.
func (e *Engine) Get(ctx context.Context, id int64) (Connector, error) {
	return e.Store.GetConnector(ctx, id)
}

// List returns every connector.
func (e *Engine) List(ctx context.Context) ([]Connector, error) { return e.Store.ListConnectors(ctx) }

// Delete removes a connector; release turns its users into local ones.
func (e *Engine) Delete(ctx context.Context, actor string, id int64, release bool) error {
	return e.Store.DeleteConnector(ctx, actor, id, release)
}

// Attributes are the source attributes the mappings read.
func (m Mappings) Attributes() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if k := strings.ToLower(s); s != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	for _, a := range []AttrMap{m.Username, m.Email, m.DisplayName, m.Extension} {
		for _, s := range a.Sources {
			add(s)
		}
	}
	add(m.Active.Attribute)
	sort.Strings(out)
	return out
}

func (e *Engine) fetchOptions(c Connector) FetchOptions {
	return FetchOptions{Limit: c.Options.userCap(), NeedGroups: c.GroupRules.GroupIDs(), Attributes: c.Mappings.Attributes(), Allow: e.Allow, Dial: e.Dial, AllowPlaintext: e.AllowPlaintext}
}

// Test connects with the stored credential and reads a small page (I-19). The
// mappings of the request, when given, are used for the sample instead of the
// stored ones, so the editor previews a change before it is saved.
func (e *Engine) Test(ctx context.Context, actor string, id int64, mappings *Mappings) (*TestResult, error) {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return nil, err
	}
	d, err := e.driver(c.Kind)
	if err != nil {
		return nil, err
	}
	if mappings != nil {
		if mv := mappings.Validate(); mv != nil {
			return nil, mv
		}
		c.Mappings = *mappings
	}
	if err := e.Store.Audit(ctx, actor, "test", "directory_connector", strconv.FormatInt(id, 10)); err != nil {
		return nil, err
	}
	failed := func(err error) *TestResult {
		ce := classify(err)
		return &TestResult{OK: false, ErrorCode: ce.Code, ErrorDetail: ce.Detail, Sample: []SampleRow{}, Groups: []SourceGroup{}}
	}
	secret, err := e.Store.Secret(ctx, id)
	if err != nil {
		return failed(&Error{Code: CodeAuthFailed, Detail: "no bind password is set"}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := d.Test(ctx, c.Config, secret, e.fetchOptions(c))
	if err != nil {
		return failed(err), nil
	}
	for i := range res.Sample {
		res.Sample[i].Mapped = c.Mappings.Apply(SourceUser{ExternalID: res.Sample[i].ExternalID, Attrs: res.Sample[i].Raw})
	}
	return res, nil
}

// classify turns any error into a scrubbed, classified one.
func classify(err error) *Error {
	var de *Error
	switch {
	case errors.As(err, &de):
		return de
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeTimeout, Detail: "the run took too long"}
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeInternal, Detail: "the run was cancelled"}
	}
	return &Error{Code: CodeInternal, Detail: "unexpected error; see the server log"}
}

// ListRuns returns a connector's runs, newest first.
func (e *Engine) ListRuns(ctx context.Context, connectorID int64, limit int) ([]Run, error) {
	if _, err := e.Store.GetConnector(ctx, connectorID); err != nil {
		return nil, err
	}
	return e.Store.ListRuns(ctx, connectorID, limit)
}

// GetRun returns one run.
func (e *Engine) GetRun(ctx context.Context, id int64) (Run, error) { return e.Store.GetRun(ctx, id) }

// ListChanges returns the rows of a run.
func (e *Engine) ListChanges(ctx context.Context, runID int64, f ChangeFilter) ([]Change, error) {
	if _, err := e.Store.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	return e.Store.ListChanges(ctx, runID, f)
}

// RequestRun queues a preview (dryRun) or an apply run; the replica holding the
// connector's lock picks it up (I-13).
func (e *Engine) RequestRun(ctx context.Context, actor string, id int64, dryRun bool) (Run, error) {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if !c.Enabled {
		return Run{}, ErrDisabled
	}
	kind := RunApply
	if dryRun {
		kind = RunPreview
	}
	r, err := e.Store.CreateRun(ctx, Run{ConnectorID: id, Kind: kind, Status: StatusQueued, RequestedBy: actor, Counts: map[string]int{}})
	if err != nil {
		return Run{}, err
	}
	act := "sync-apply"
	if dryRun {
		act = "sync-preview"
	}
	if err := e.Store.Audit(ctx, actor, act, "directory_connector", strconv.FormatInt(id, 10)); err != nil {
		e.log().Warn("directory: audit", "err", err)
	}
	e.Kick()
	return r, nil
}

func (e *Engine) nowPtr() *time.Time { t := e.now().UTC(); return &t }

// Apply applies a preview (I-8). It re-reads the source: when the preview is
// older than PreviewTTL or the diff hash differs, it marks the preview stale,
// stores a fresh preview and returns it with ErrStale.
func (e *Engine) Apply(ctx context.Context, actor string, runID int64, dec Decisions) (Run, error) {
	return e.applyRun(ctx, actor, runID, dec, StatusPreview, false)
}

// Confirm applies a run the mass-deprovision guard halted (I-7), after an
// administrator saw the diff.
func (e *Engine) Confirm(ctx context.Context, actor string, runID int64, dec Decisions) (Run, error) {
	return e.applyRun(ctx, actor, runID, dec, StatusNeedsConfirmation, true)
}

func (e *Engine) applyRun(ctx context.Context, actor string, runID int64, dec Decisions, want string, confirmed bool) (Run, error) {
	run, err := e.Store.GetRun(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Status != want {
		return run, ErrNotApplicable
	}
	c, err := e.Store.GetConnector(ctx, run.ConnectorID)
	if err != nil {
		return Run{}, err
	}
	if !c.Enabled {
		return run, ErrDisabled
	}
	// The apply and the work it does must outlive the client that asked.
	ctx = context.WithoutCancel(ctx)
	unlock, ok, err := e.Store.TryLock(ctx, lockKey(c.ID))
	if err != nil {
		return run, err
	}
	if !ok {
		return run, ErrBusy
	}
	defer unlock()

	action := "sync-apply"
	if confirmed {
		action = "sync-confirm"
	}
	if err := e.Store.Audit(ctx, actor, action, "directory_connector", strconv.FormatInt(c.ID, 10)); err != nil {
		return run, err
	}
	started := e.now()
	ref := run.CreatedAt
	if run.FinishedAt != nil {
		ref = *run.FinishedAt
	}
	runCtx, cancel := context.WithTimeout(ctx, c.Options.runTimeout())
	defer cancel()
	res, ferr := e.fetch(runCtx, c)
	if ferr != nil {
		return e.fail(ctx, c, run, started, ferr), nil
	}
	st, err := e.Store.LoadState(ctx, c.ID)
	if err != nil {
		return e.fail(ctx, c, run, started, err), nil
	}
	base := MakePlan(PlanInput{Connector: c, State: st, Result: res})
	if started.Sub(ref) > PreviewTTL || base.Hash != run.DiffHash {
		// Stale: keep the old preview as such and answer with a fresh one.
		run.Status = StatusStale
		run.FinishedAt = e.nowPtr()
		if err := e.Store.UpdateRun(ctx, run); err != nil {
			return run, err
		}
		fresh, err := e.Store.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunPreview, Status: StatusRunning, RequestedBy: actor,
			StartedAt: e.nowPtr(), Replica: e.Replica, Counts: map[string]int{}})
		if err != nil {
			return run, err
		}
		fresh, err = e.finishPreview(ctx, c, fresh, base, res, started)
		if err != nil {
			return fresh, err
		}
		return fresh, ErrStale
	}
	plan := MakePlan(PlanInput{Connector: c, State: st, Result: res, Decisions: dec})
	run.Kind = RunApply
	run.RequestedBy = actor
	run.Replica = e.Replica
	run.Status = StatusRunning
	run.StartedAt = e.nowPtr()
	run.FinishedAt = nil
	run.DiffHash = base.Hash
	if err := e.Store.UpdateRun(ctx, run); err != nil {
		return run, err
	}
	if len(dec.Adopt) > 0 || dec.AdoptAll || len(dec.Override) > 0 {
		if err := e.Store.Audit(ctx, actor, "sync-decisions", "directory_connector", strconv.FormatInt(c.ID, 10)); err != nil {
			e.log().Warn("directory: audit", "err", err)
		}
	}
	return e.applyPlan(ctx, c, run, plan, res, started, confirmed)
}

// execQueued runs a queued run the lock holder claimed.
func (e *Engine) execQueued(ctx context.Context, c Connector, run Run) {
	started := e.now()
	ctx, cancel := context.WithTimeout(ctx, c.Options.runTimeout())
	defer cancel()
	res, err := e.fetch(ctx, c)
	if err != nil {
		e.fail(ctx, c, run, started, err)
		return
	}
	st, err := e.Store.LoadState(ctx, c.ID)
	if err != nil {
		e.fail(ctx, c, run, started, err)
		return
	}
	plan := MakePlan(PlanInput{Connector: c, State: st, Result: res})
	if run.Kind == RunPreview {
		if _, err := e.finishPreview(ctx, c, run, plan, res, started); err != nil {
			e.log().Error("directory: finish preview", "connector", c.ID, "err", err)
		}
		return
	}
	if _, err := e.applyPlan(ctx, c, run, plan, res, started, false); err != nil {
		e.log().Error("directory: apply", "connector", c.ID, "err", err)
	}
}

// fetch reads the source and enforces the empty-result precondition inputs.
func (e *Engine) fetch(ctx context.Context, c Connector) (*Result, error) {
	d, err := e.driver(c.Kind)
	if err != nil {
		return nil, err
	}
	secret, err := e.Store.Secret(ctx, c.ID)
	if err != nil {
		return nil, &Error{Code: CodeAuthFailed, Detail: "no bind password is set"}
	}
	o := e.fetchOptions(c)
	res, err := d.Fetch(ctx, c.Config, secret, o)
	if err != nil {
		return nil, classify(err)
	}
	if len(res.Users) > o.Limit {
		return nil, &Error{Code: CodeUserCap, Detail: "the source returned more users than the cap of " + strconv.Itoa(o.Limit)}
	}
	return res, nil
}

func counts(p Plan, res *Result) map[string]int {
	m := map[string]int{}
	for k, v := range p.Counts {
		m[k] = v
	}
	m["users"] = len(res.Users)
	m["linked"] = p.Linked
	return m
}

func (e *Engine) fail(ctx context.Context, c Connector, run Run, started time.Time, err error) Run {
	ce := classify(err)
	if ce.Code == CodeInternal {
		e.log().Error("directory: run failed", "connector", c.ID, "err", err)
	} else {
		e.log().Warn("directory: run failed", "connector", c.ID, "code", ce.Code)
	}
	run.Status, run.ErrorCode, run.ErrorDetail, run.FinishedAt = StatusFailed, ce.Code, ce.Detail, e.nowPtr()
	if run.StartedAt == nil {
		run.StartedAt = &started
	}
	if uerr := e.Store.UpdateRun(context.WithoutCancel(ctx), run); uerr != nil {
		e.log().Error("directory: update run", "err", uerr)
	}
	e.Metrics.run(run.Kind, "failed", e.now().Sub(started).Seconds())
	n, rerr := e.Store.RecordOutcome(context.WithoutCancel(ctx), c.ID, false, ce.Code, ce.Detail, e.now())
	if rerr != nil {
		e.log().Error("directory: record outcome", "err", rerr)
	}
	if n == 3 && e.Notify != nil {
		go e.Notify(context.WithoutCancel(ctx), c, ce.Code)
	}
	return run
}

// finishPreview stores a preview run's rows and hash.
func (e *Engine) finishPreview(ctx context.Context, c Connector, run Run, plan Plan, res *Result, started time.Time) (Run, error) {
	if len(res.Users) == 0 && plan.Linked > 0 {
		return e.halt(ctx, c, run, plan, res, started, CodeEmptyResult)
	}
	if err := e.Store.ReplaceChanges(ctx, run.ID, plan.Changes); err != nil {
		return e.fail(ctx, c, run, started, err), err
	}
	run.Status, run.Counts, run.DiffHash, run.FinishedAt = StatusPreview, counts(plan, res), plan.Hash, e.nowPtr()
	if run.StartedAt == nil {
		run.StartedAt = &started
	}
	if err := e.Store.UpdateRun(ctx, run); err != nil {
		return run, err
	}
	e.Metrics.run(RunPreview, "preview", e.now().Sub(started).Seconds())
	e.Metrics.conflicts(c.Kind, plan.Counts[ActionConflict])
	return run, nil
}

// halt stops a run before it applies anything and waits for a confirmation
// (the mass-deprovision and empty-result guards, I-7 and the edge cases).
func (e *Engine) halt(ctx context.Context, c Connector, run Run, plan Plan, res *Result, started time.Time, code string) (Run, error) {
	if err := e.Store.ReplaceChanges(ctx, run.ID, plan.Changes); err != nil {
		return e.fail(ctx, c, run, started, err), err
	}
	detail := "a run would disable " + strconv.Itoa(plan.Disables) + " of " + strconv.Itoa(plan.Linked) + " linked users"
	if code == CodeEmptyResult {
		detail = "the source returned no users although users are linked"
	}
	run.Status, run.Counts, run.DiffHash, run.FinishedAt = StatusNeedsConfirmation, counts(plan, res), plan.Hash, e.nowPtr()
	run.ErrorCode, run.ErrorDetail = code, detail
	if run.StartedAt == nil {
		run.StartedAt = &started
	}
	if err := e.Store.UpdateRun(ctx, run); err != nil {
		return run, err
	}
	e.Metrics.run(run.Kind, "needs_confirmation", e.now().Sub(started).Seconds())
	e.log().Warn("directory: run halted for confirmation", "connector", c.ID, "code", code)
	return run, nil
}

// guarded reports whether a plan disables more than the guard allows: more
// than 20 percent of the linked users and more than 5 users.
func guarded(p Plan) bool {
	limit := p.Linked * GuardPercent / 100
	if limit < GuardMinimum {
		limit = GuardMinimum
	}
	return p.Disables > limit
}

// applyPlan applies a plan. Without a confirmation the guards halt it.
func (e *Engine) applyPlan(ctx context.Context, c Connector, run Run, plan Plan, res *Result, started time.Time, confirmed bool) (Run, error) {
	if !confirmed {
		switch {
		case len(res.Users) == 0 && plan.Linked > 0:
			return e.halt(ctx, c, run, plan, res, started, CodeEmptyResult)
		case guarded(plan):
			return e.halt(ctx, c, run, plan, res, started, "needs_confirmation")
		}
	}
	actor := actorOf(c)
	rows := make([]Change, 0, len(plan.Changes))
	errs := 0
	for _, ch := range plan.Changes {
		if ch.op == nil {
			rows = append(rows, ch)
			continue
		}
		if err := e.Store.Apply(ctx, actor, c, *ch.op); err != nil {
			errs++
			rows = append(rows, Change{Action: ActionError, UserID: ch.UserID, ExternalID: ch.ExternalID, Username: ch.Username,
				FieldDiffs: ch.FieldDiffs, Reason: applyReason(err)})
			e.log().Warn("directory: apply change", "connector", c.ID, "reason", applyReason(err))
			continue
		}
		rows = append(rows, ch)
	}
	for _, q := range plan.Quiet {
		if err := e.Store.Apply(ctx, actor, c, q); err != nil {
			e.log().Warn("directory: apply quiet", "connector", c.ID, "err", applyReason(err))
		}
	}
	if err := e.Store.ReplaceChanges(ctx, run.ID, rows); err != nil {
		return e.fail(ctx, c, run, started, err), err
	}
	cn := counts(plan, res)
	cn[ActionError] = errs
	run.Status, run.Counts, run.DiffHash, run.FinishedAt = StatusSucceeded, cn, plan.Hash, e.nowPtr()
	run.ErrorCode, run.ErrorDetail = "", ""
	if err := e.Store.UpdateRun(ctx, run); err != nil {
		return run, err
	}
	if _, err := e.Store.RecordOutcome(ctx, c.ID, true, "", "", e.now()); err != nil {
		e.log().Error("directory: record outcome", "err", err)
	}
	e.Metrics.run(run.Kind, "succeeded", e.now().Sub(started).Seconds())
	e.Metrics.changes(run.Kind, cn)
	e.Metrics.conflicts(c.Kind, plan.Counts[ActionConflict])
	e.Metrics.success(c.ID, float64(e.now().Unix()))
	return run, nil
}

func applyReason(err error) string {
	switch {
	case errors.Is(err, ErrLastAdmin):
		return ReasonLastAdmin
	case errors.Is(err, ErrNotFound):
		return "not_found"
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return "invalid"
	}
	return "apply_failed"
}

// Run is the scheduler loop of this replica; it returns when ctx ends.
func (e *Engine) Run(ctx context.Context) {
	e.init()
	t := e.Tick
	if t <= 0 {
		t = 30 * time.Second
	}
	tk := time.NewTicker(t)
	defer tk.Stop()
	prune := time.NewTicker(6 * time.Hour)
	defer prune.Stop()
	for {
		e.TickOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		case <-e.wake:
		case <-prune.C:
			e.prune(ctx)
		}
	}
}

func (e *Engine) prune(ctx context.Context) {
	unlock, ok, err := e.Store.TryLock(ctx, "hello:dirsync:prune")
	if err != nil || !ok {
		return
	}
	defer unlock()
	if n, err := e.Store.PruneChanges(ctx, e.now().Add(-ChangeRetention)); err != nil {
		e.log().Error("directory: prune", "err", err)
	} else if n > 0 {
		e.log().Info("directory: pruned sync changes", "rows", n)
	}
}

// TickOnce visits every enabled connector once.
func (e *Engine) TickOnce(ctx context.Context) {
	cs, err := e.Store.ListConnectors(ctx)
	if err != nil {
		e.log().Error("directory: list connectors", "err", err)
		return
	}
	for _, c := range cs {
		if ctx.Err() != nil {
			return
		}
		if c.Enabled {
			e.process(ctx, c)
		}
	}
}

// process takes the connector's lock, closes runs a dead replica left, runs
// the queued ones and then a scheduled one when it is due.
func (e *Engine) process(ctx context.Context, c Connector) {
	unlock, ok, err := e.Store.TryLock(ctx, lockKey(c.ID))
	if err != nil {
		e.log().Error("directory: lock", "connector", c.ID, "err", err)
		return
	}
	if !ok {
		return
	}
	defer unlock()
	if n, err := e.Store.CloseOrphans(ctx, c.ID); err != nil {
		e.log().Error("directory: close orphans", "connector", c.ID, "err", err)
	} else if n > 0 {
		e.log().Warn("directory: closed runs of a lost replica", "connector", c.ID, "runs", n)
	}
	for ctx.Err() == nil {
		r, err := e.Store.ClaimQueued(ctx, c.ID, e.Replica)
		if err != nil {
			e.log().Error("directory: claim", "connector", c.ID, "err", err)
			return
		}
		if r == nil {
			break
		}
		// The connector may have been edited since the tick read it.
		fresh, err := e.Store.GetConnector(ctx, c.ID)
		if err != nil || !fresh.Enabled {
			e.fail(ctx, c, *r, e.now(), &Error{Code: CodeInternal, Detail: "the connector was disabled or removed"})
			continue
		}
		e.execQueued(ctx, fresh, *r)
	}
	if c.ScheduleMinutes <= 0 || ctx.Err() != nil {
		return
	}
	// A schedule starts only after an administrator applied a run by hand: a
	// connector never changes users on its first, unreviewed run.
	if applied, err := e.Store.HasApplied(ctx, c.ID); err != nil || !applied {
		return
	}
	last, ok, err := e.Store.LastScheduled(ctx, c.ID)
	if err != nil {
		e.log().Error("directory: last scheduled", "connector", c.ID, "err", err)
		return
	}
	if ok && e.now().Sub(last) < time.Duration(c.ScheduleMinutes)*time.Minute {
		return
	}
	r, err := e.Store.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunScheduled, Status: StatusRunning, RequestedBy: "scheduler",
		StartedAt: e.nowPtr(), Replica: e.Replica, Counts: map[string]int{}})
	if err != nil {
		e.log().Error("directory: create scheduled run", "connector", c.ID, "err", err)
		return
	}
	e.execQueued(ctx, c, r)
}
