package directory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// LocalUser is a user as the planner sees it.
type LocalUser struct {
	ID          int64
	Username    string
	Email       string
	DisplayName string
	Role        string
	Enabled     bool
	// ConnectorID and ExternalID are set for a directory-managed user.
	ConnectorID int64
	ExternalID  string
	// DisabledByDirectory is set when sync disabled the user (disabled_at).
	DisabledByDirectory bool
	AbsentRuns          int
	// OwnedExtension is the extension id of the link this user's connector
	// owns (0 none); ExtensionIDs are all the user's links.
	OwnedExtension int64
	ExtensionIDs   []int64
	// MemberOf are the ring groups the user's owned or primary extension is in,
	// of any origin; SyncedMemberOf the ones the connector put it in.
	MemberOf       []int64
	SyncedMemberOf []int64
	// Managed is the managed-field set stored on the user.
	Managed []string
}

// LocalExt is an extension as the planner sees it.
type LocalExt struct {
	ID     int64
	Number string
	// Door is an extension of another kind of record (a door station).
	Door bool
	// UserID is the user the extension is linked to, 0 when free.
	UserID int64
}

// State is the local side of a run.
type State struct {
	Users []LocalUser
	Exts  map[string]LocalExt
	// Admins is the number of enabled admins.
	Admins int
	// RingGroups are the ring groups that exist.
	RingGroups map[int64]bool
}

// Op is what applying a change does to the database.
type Op struct {
	Kind       string // create, update, adopt, disable, enable, absent
	UserID     int64
	ExternalID string
	Username   *string
	Email      *string
	Display    *string
	Role       *string
	// Extension is the extension to link (0 none); SetExtension says it is
	// part of the change; Override takes it from the user that holds it.
	Extension    int64
	SetExtension bool
	Override     bool
	RingAdd      []int64
	RingRemove   []int64
	// Managed is the set of managed fields to store on the user.
	Managed []string
	// SetManaged says Managed is to be stored (an empty set clears it).
	SetManaged bool
	AbsentRuns int
	Devices    bool
}

// Plan is the planner's answer: the rows, their hash and the operations.
type Plan struct {
	Changes []Change
	// Quiet are operations that show no row: a present user's absence count
	// going back to 0, and a managed-field set that changed with the mapping.
	Quiet  []Op
	Counts map[string]int
	Hash   string
	// Disables is how many users the plan disables.
	Disables int
	// Linked is how many users are linked to the connector and enabled.
	Linked int
}

func fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func sortInt64(a []int64) { slices.Sort(a) }

// PlanInput is everything the planner needs.
type PlanInput struct {
	Connector Connector
	State     State
	Result    *Result
	Decisions Decisions
	// Incremental says Result holds only changed users: absence proves
	// nothing, so no user is counted absent or disabled for it (I-12).
	Incremental bool
}

type planner struct {
	in      PlanInput
	c       Connector
	opts    Options
	byExt   map[string]*LocalUser // this connector's users by external id
	byEmail map[string][]*LocalUser
	byName  map[string]*LocalUser
	admins  int
	// inRun tracks what earlier users of this run take.
	emails   map[string]string
	names    map[string]string
	extOwner map[string]string
	extCount map[string]int
	adopt    map[string]bool
	adoptAll bool
	override map[string]bool
	plan     Plan
	managed  []string
}

// MakePlan computes the changes a sync would make (I-3 to I-10). It is a pure
// function of its input, so a preview and a later apply of the same source and
// state agree, which the diff hash proves.
func MakePlan(in PlanInput) Plan {
	p := &planner{
		in: in, c: in.Connector, opts: in.Connector.Options,
		byExt: map[string]*LocalUser{}, byEmail: map[string][]*LocalUser{}, byName: map[string]*LocalUser{},
		admins: in.State.Admins, emails: map[string]string{}, names: map[string]string{},
		extOwner: map[string]string{}, extCount: map[string]int{},
		adopt: map[string]bool{}, override: map[string]bool{}, adoptAll: in.Decisions.AdoptAll,
		plan: Plan{Counts: map[string]int{}},
	}
	for _, id := range in.Decisions.Adopt {
		p.adopt[id] = true
	}
	for _, id := range in.Decisions.Override {
		p.override[id] = true
	}
	p.managed = p.c.Mappings.Managed(p.c.GroupRules)
	us := in.State.Users
	for i := range us {
		u := &us[i]
		if u.ConnectorID == p.c.ID && u.ExternalID != "" {
			p.byExt[u.ExternalID] = u
			if u.Enabled {
				p.plan.Linked++
			}
		}
		if e := fold(u.Email); e != "" {
			p.byEmail[e] = append(p.byEmail[e], u)
		}
		p.byName[fold(u.Username)] = u
	}
	p.run()
	return p.finish()
}

type src struct {
	u SourceUser
	m Mapped
}

func (p *planner) run() {
	res := p.in.Result
	srcs := make([]src, 0, len(res.Users))
	for _, u := range res.Users {
		srcs = append(srcs, src{u, p.c.Mappings.Apply(u)})
	}
	sort.SliceStable(srcs, func(i, j int) bool { return srcs[i].u.ExternalID < srcs[j].u.ExternalID })
	for _, s := range srcs {
		if s.m.Extension != "" && s.m.ExtensionError == "" {
			p.extCount[s.m.Extension]++
		}
	}
	seen := map[string]bool{}
	for _, s := range srcs {
		if seen[s.u.ExternalID] {
			continue
		}
		seen[s.u.ExternalID] = true
		if lu := p.byExt[s.u.ExternalID]; lu != nil {
			p.linked(s, lu)
		} else {
			p.unlinked(s)
		}
	}
	if !p.in.Incremental {
		p.absent(seen)
	}
}

func (p *planner) add(c Change, op *Op) {
	c.op = op
	p.plan.Changes = append(p.plan.Changes, c)
	p.plan.Counts[c.Action]++
}

func (p *planner) conflict(s src, lu *LocalUser, reason string, overridable bool) {
	c := Change{Action: ActionConflict, ExternalID: s.u.ExternalID, Username: s.m.Username, Reason: reason, Overridable: overridable}
	if lu != nil {
		id := lu.ID
		c.UserID = &id
		c.Username = lu.Username
	}
	p.add(c, nil)
}

// lastAdminRisk reports whether removing an enabled admin from the pool would
// leave none, and tracks the simulated count when it does not.
func (p *planner) dropAdmin(lu *LocalUser) bool {
	if lu.Role != "admin" || !lu.Enabled {
		return true
	}
	if p.admins <= 1 {
		return false
	}
	p.admins--
	return true
}

// extension resolves the mapped extension for a user: its id (0 when the user
// has none), or a conflict reason. self is the user it would be linked to
// (nil for a new user).
type extResult struct {
	id       int64
	number   string
	reason   string
	override bool
	// overridable is a conflict that an administrator may override.
	overridable bool
}

func (p *planner) extension(s src, self *LocalUser) extResult {
	m := s.m
	if len(p.c.Mappings.Extension.Sources) == 0 || m.Extension == "" && m.ExtensionError == "" {
		return extResult{}
	}
	if m.ExtensionError != "" {
		return extResult{reason: ReasonExtensionInvalid}
	}
	if p.extCount[m.Extension] > 1 {
		return extResult{reason: ReasonExtensionDup}
	}
	e, ok := p.in.State.Exts[m.Extension]
	switch {
	case !ok:
		return extResult{reason: ReasonExtensionMissing}
	case e.Door:
		return extResult{reason: ReasonExtensionKind}
	}
	if self != nil && self.OwnedExtension == e.ID {
		return extResult{id: e.ID, number: m.Extension}
	}
	if e.UserID != 0 && (self == nil || e.UserID != self.ID) {
		if p.override[s.u.ExternalID] {
			return extResult{id: e.ID, number: m.Extension, override: true}
		}
		return extResult{reason: ReasonExtensionLinked, overridable: true}
	}
	if owner, taken := p.extOwner[m.Extension]; taken && owner != s.u.ExternalID {
		return extResult{reason: ReasonExtensionDup}
	}
	return extResult{id: e.ID, number: m.Extension}
}

func (p *planner) wantedRole(s src) string {
	return p.c.GroupRules.Role(s.u.Groups, p.opts.AllowAdminFromDirectory)
}

func (p *planner) wantedRings(s src, extID int64) []int64 {
	if len(p.c.GroupRules.RingGroups) == 0 || extID == 0 {
		return nil
	}
	var out []int64
	for _, g := range p.c.GroupRules.RingGroupIDs(s.u.Groups) {
		if p.in.State.RingGroups[g] {
			out = append(out, g)
		}
	}
	return out
}

func (p *planner) linked(s src, lu *LocalUser) {
	id := lu.ID
	base := Change{UserID: &id, ExternalID: s.u.ExternalID, Username: lu.Username}
	if lu.AbsentRuns > 0 {
		p.plan.Quiet = append(p.plan.Quiet, Op{Kind: "touch", UserID: lu.ID, ExternalID: s.u.ExternalID})
	}
	if want := p.managedFor(); !slices.Equal(lu.Managed, want) && lu.ConnectorID == p.c.ID {
		p.plan.Quiet = append(p.plan.Quiet, Op{Kind: "managed", UserID: lu.ID, ExternalID: s.u.ExternalID, Managed: want, SetManaged: true})
	}
	if !s.m.Active {
		if lu.Enabled {
			if !p.dropAdmin(lu) {
				p.conflict(s, lu, ReasonLastAdmin, false)
				return
			}
			c := base
			c.Action, c.Reason = ActionDisable, ReasonInactive
			p.plan.Disables++
			p.add(c, &Op{Kind: ActionDisable, UserID: lu.ID, ExternalID: s.u.ExternalID, Devices: p.opts.disableDevices()})
		}
		return
	}
	if !lu.Enabled && lu.DisabledByDirectory {
		c := base
		c.Action, c.Reason = ActionEnable, "active_in_source"
		p.add(c, &Op{Kind: ActionEnable, UserID: lu.ID, ExternalID: s.u.ExternalID, Devices: p.opts.disableDevices()})
		if lu.Role == "admin" {
			p.admins++
		}
	}
	if p.c.Mode == ModeCreateOnly {
		return
	}
	p.fields(s, lu, base, false)
}

// fields plans the attribute changes of an existing user (a linked user, or an
// adoption candidate when adopting is true).
func (p *planner) fields(s src, lu *LocalUser, base Change, adopting bool) {
	m := s.m
	diffs := map[string]Diff{}
	op := &Op{Kind: ActionUpdate, UserID: lu.ID, ExternalID: s.u.ExternalID, Managed: p.managedFor(), SetManaged: true}
	if adopting {
		op.Kind = ActionAdopt
	}
	if m.DisplayName != "" && m.DisplayName != lu.DisplayName && len(p.c.Mappings.DisplayName.Sources) > 0 {
		diffs["displayName"] = Diff{lu.DisplayName, m.DisplayName}
		op.Display = &m.DisplayName
	}
	if m.Email != "" && len(p.c.Mappings.Email.Sources) > 0 && fold(m.Email) != fold(lu.Email) {
		if p.emailTaken(m.Email, lu, s.u.ExternalID) {
			p.conflict(s, lu, ReasonEmailInUse, false)
			return
		}
		diffs["email"] = Diff{lu.Email, m.Email}
		op.Email = &m.Email
	}
	if m.Username != "" && len(p.c.Mappings.Username.Sources) > 0 && fold(m.Username) != fold(lu.Username) {
		switch {
		case !usernameRe.MatchString(m.Username):
			p.conflict(s, lu, ReasonUsernameInvalid, false)
			return
		case p.nameTaken(m.Username, lu, s.u.ExternalID):
			p.conflict(s, lu, ReasonUsernameTaken, false)
			return
		}
		diffs["username"] = Diff{lu.Username, m.Username}
		op.Username = &m.Username
	}
	if len(p.c.GroupRules.Roles) > 0 {
		want := p.wantedRole(s)
		if want != lu.Role {
			if lu.Role == "admin" && lu.Enabled && !p.dropAdmin(lu) {
				p.conflict(s, lu, ReasonLastAdmin, false)
				return
			}
			diffs["role"] = Diff{lu.Role, want}
			op.Role = &want
		}
	}
	ext := p.extension(s, lu)
	if ext.reason != "" {
		p.conflict(s, lu, ext.reason, ext.overridable)
		return
	}
	finalExt := lu.OwnedExtension
	if ext.id != 0 {
		finalExt = ext.id
		if ext.id != lu.OwnedExtension {
			diffs["extension"] = Diff{p.extNumber(lu.OwnedExtension), ext.number}
			op.Extension, op.SetExtension, op.Override = ext.id, true, ext.override
			p.extOwner[ext.number] = s.u.ExternalID
		}
	}
	if len(p.c.GroupRules.RingGroups) > 0 && finalExt != 0 {
		want := p.wantedRings(s, finalExt)
		have, synced := lu.MemberOf, lu.SyncedMemberOf
		if op.SetExtension {
			// A new extension: the old one's synced rows go with the link and
			// the new one is joined to every wanted group.
			have, synced = nil, nil
		}
		for _, g := range want {
			if !slices.Contains(have, g) {
				op.RingAdd = append(op.RingAdd, g)
			}
		}
		for _, g := range synced {
			if !slices.Contains(want, g) {
				op.RingRemove = append(op.RingRemove, g)
			}
		}
		if len(op.RingAdd)+len(op.RingRemove) > 0 {
			diffs["ringGroups"] = Diff{joinIDs(lu.SyncedMemberOf), joinIDs(want)}
		}
	}
	if adopting {
		c := base
		c.Action, c.FieldDiffs = ActionAdopt, diffs
		p.reserve(m, lu)
		if p.adoptAll || p.adopt[s.u.ExternalID] {
			c.Reason = "confirmed"
			p.add(c, op)
			return
		}
		c.Reason = ReasonAdoptNotConfirmed
		p.add(c, nil)
		return
	}
	if len(diffs) == 0 {
		return
	}
	c := base
	c.Action, c.FieldDiffs = ActionUpdate, diffs
	p.reserve(m, lu)
	p.add(c, op)
}

// managedFor is the managed-field set a user of this connector carries: none
// in create_only mode, where Hello owns the fields after the first write.
func (p *planner) managedFor() []string {
	if p.c.Mode == ModeCreateOnly {
		return nil
	}
	return p.managed
}

func (p *planner) extNumber(id int64) string {
	if id == 0 {
		return ""
	}
	for n, e := range p.in.State.Exts {
		if e.ID == id {
			return n
		}
	}
	return ""
}

func joinIDs(ids []int64) string {
	s := slices.Clone(ids)
	slices.Sort(s)
	out := make([]string, len(s))
	for i, id := range s {
		out[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(out, ",")
}

// emailTaken reports whether another user, or an earlier user of the run, has
// the address.
func (p *planner) emailTaken(email string, self *LocalUser, ext string) bool {
	key := fold(email)
	for _, o := range p.byEmail[key] {
		if self == nil || o.ID != self.ID {
			return true
		}
	}
	owner, ok := p.emails[key]
	return ok && owner != ext
}

func (p *planner) nameTaken(name string, self *LocalUser, ext string) bool {
	key := fold(name)
	if o, ok := p.byName[key]; ok && (self == nil || o.ID != self.ID) {
		return true
	}
	owner, ok := p.names[key]
	return ok && owner != ext
}

// reserve records what a planned change takes, so a later user of the run that
// wants the same email or username is a conflict.
func (p *planner) reserve(m Mapped, lu *LocalUser) {
	ext := ""
	if lu != nil {
		ext = lu.ExternalID
	}
	if m.Email != "" {
		p.emails[fold(m.Email)] = ext
	}
	if m.Username != "" {
		p.names[fold(m.Username)] = ext
	}
}

func (p *planner) unlinked(s src) {
	m := s.m
	if !m.Active {
		c := Change{Action: ActionSkip, ExternalID: s.u.ExternalID, Username: m.Username, Reason: ReasonInactive}
		p.add(c, nil)
		return
	}
	// A match by verified email, then by username.
	var cand *LocalUser
	byUsername := false
	if m.Email != "" {
		for _, o := range p.byEmail[fold(m.Email)] {
			if cand == nil {
				cand = o
			} else if o.ID != cand.ID {
				// Two local users hold the address: ambiguous.
				p.conflict(s, nil, ReasonEmailInUse, false)
				return
			}
		}
	}
	if cand == nil && m.Username != "" {
		if o, ok := p.byName[fold(m.Username)]; ok {
			cand, byUsername = o, true
		}
	}
	if cand != nil {
		p.match(s, cand, byUsername)
		return
	}
	if p.c.Mode == ModeLinkOnly {
		p.add(Change{Action: ActionSkip, ExternalID: s.u.ExternalID, Username: m.Username, Reason: ReasonLinkOnlyNoMatch}, nil)
		return
	}
	p.create(s)
}

// match handles a source user that matches a local user that is not yet linked
// to it.
func (p *planner) match(s src, cand *LocalUser, byUsername bool) {
	m := s.m
	switch {
	case cand.ConnectorID != 0 && cand.ConnectorID != p.c.ID:
		p.conflict(s, cand, ReasonClaimed, false)
		return
	case cand.ConnectorID == p.c.ID:
		// Linked to another record of this connector: the old record is
		// disabled by absence and this one is a new user; a name clash.
		reason := ReasonUsernameTaken
		if !byUsername {
			reason = ReasonEmailInUse
		}
		p.conflict(s, cand, reason, false)
		return
	case cand.Role == "admin":
		id := cand.ID
		p.add(Change{Action: ActionSkip, UserID: &id, ExternalID: s.u.ExternalID, Username: cand.Username, Reason: ReasonLocalAdmin}, nil)
		return
	case byUsername && m.Email != "" && cand.Email != "" && fold(cand.Email) != fold(m.Email):
		p.conflict(s, cand, ReasonUsernameTaken, false)
		return
	}
	id := cand.ID
	base := Change{UserID: &id, ExternalID: s.u.ExternalID, Username: cand.Username}
	cand2 := *cand
	cand2.ExternalID = s.u.ExternalID
	p.fields(s, &cand2, base, true)
}

func (p *planner) create(s src) {
	m := s.m
	switch {
	case m.Username == "" || !usernameRe.MatchString(m.Username):
		p.conflict(s, nil, ReasonUsernameInvalid, false)
		return
	case p.nameTaken(m.Username, nil, s.u.ExternalID):
		p.conflict(s, nil, ReasonUsernameTaken, false)
		return
	case m.Email != "" && p.emailTaken(m.Email, nil, s.u.ExternalID):
		p.conflict(s, nil, ReasonEmailInUse, false)
		return
	}
	ext := p.extension(s, nil)
	if ext.reason != "" {
		p.conflict(s, nil, ext.reason, ext.overridable)
		return
	}
	role := "user"
	if len(p.c.GroupRules.Roles) > 0 {
		role = p.wantedRole(s)
	}
	name := m.DisplayName
	if name == "" {
		name = m.Username
	}
	op := &Op{Kind: ActionCreate, ExternalID: s.u.ExternalID, Username: &m.Username, Email: &m.Email, Display: &name, Role: &role,
		Extension: ext.id, SetExtension: ext.id != 0, Override: ext.override, Managed: p.managedFor(), SetManaged: true}
	diffs := map[string]Diff{"username": {"", m.Username}, "displayName": {"", name}, "role": {"", role}}
	if m.Email != "" {
		diffs["email"] = Diff{"", m.Email}
	}
	if ext.id != 0 {
		diffs["extension"] = Diff{"", ext.number}
		p.extOwner[ext.number] = s.u.ExternalID
	}
	op.RingAdd = p.wantedRings(s, ext.id)
	if len(op.RingAdd) > 0 {
		diffs["ringGroups"] = Diff{"", joinIDs(op.RingAdd)}
	}
	p.reserve(m, &LocalUser{ExternalID: s.u.ExternalID})
	p.add(Change{Action: ActionCreate, ExternalID: s.u.ExternalID, Username: m.Username, FieldDiffs: diffs}, op)
}

// absent handles linked users missing from the source (I-7).
func (p *planner) absent(seen map[string]bool) {
	for _, lu := range p.in.State.Users {
		if lu.ConnectorID != p.c.ID || lu.ExternalID == "" || seen[lu.ExternalID] || !lu.Enabled {
			continue
		}
		u := lu
		id := u.ID
		base := Change{UserID: &id, ExternalID: u.ExternalID, Username: u.Username}
		runs := u.AbsentRuns + 1
		if runs >= p.opts.absenceRuns() {
			if !p.dropAdmin(&u) {
				c := base
				c.Action, c.Reason = ActionConflict, ReasonLastAdmin
				p.add(c, nil)
				continue
			}
			c := base
			c.Action, c.Reason = ActionDisable, ReasonAbsent
			p.plan.Disables++
			p.add(c, &Op{Kind: ActionDisable, UserID: u.ID, ExternalID: u.ExternalID, AbsentRuns: runs, Devices: p.opts.disableDevices()})
			continue
		}
		c := base
		c.Action, c.Reason = ActionSkip, ReasonAbsentPending+":"+strconv.Itoa(runs)+"/"+strconv.Itoa(p.opts.absenceRuns())
		p.add(c, &Op{Kind: "absent", UserID: u.ID, ExternalID: u.ExternalID, AbsentRuns: runs})
	}
}

func (p *planner) finish() Plan {
	pl := p.plan
	sort.SliceStable(pl.Changes, func(i, j int) bool {
		a, b := pl.Changes[i], pl.Changes[j]
		if a.ExternalID != b.ExternalID {
			return a.ExternalID < b.ExternalID
		}
		return a.Action < b.Action
	})
	pl.Hash = hashChanges(pl.Changes)
	pl.Counts["conflicts"] = pl.Counts[ActionConflict]
	return pl
}

// hashChanges is the diff hash: it covers what each row does, so two reads of
// an unchanged source and state agree (I-8).
func hashChanges(cs []Change) string {
	h := sha256.New()
	for _, c := range cs {
		uid := int64(0)
		if c.UserID != nil {
			uid = *c.UserID
		}
		keys := make([]string, 0, len(c.FieldDiffs))
		for k := range c.FieldDiffs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b, _ := json.Marshal(struct {
			A, E, R string
			U       int64
			K       []string
			D       []Diff
		}{c.Action, c.ExternalID, c.Reason, uid, keys, diffsOf(c.FieldDiffs, keys)})
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func diffsOf(m map[string]Diff, keys []string) []Diff {
	out := make([]Diff, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}
