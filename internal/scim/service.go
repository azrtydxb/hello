package scim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
)

// Engine is the directory engine as the SCIM service uses it;
// *directory.Engine satisfies it.
type Engine interface {
	Get(ctx context.Context, id int64) (directory.Connector, error)
	Push(ctx context.Context, c directory.Connector, users []directory.SourceUser, o directory.PushOptions) (directory.PushResult, error)
	Guard(ctx context.Context, c directory.Connector) (directory.GuardStatus, error)
	ListRuns(ctx context.Context, connectorID int64, limit int) ([]directory.Run, error)
}

// Service is the SCIM service provider: the /scim/v2 handler and the
// administration of its tokens and status.
type Service struct {
	Store  Store
	Engine Engine
	// PublicURL is HELLO_PUBLIC_URL; the base URL of the service is
	// PublicURL/scim/v2 and every meta.location is under it.
	PublicURL string
	Metrics   *Metrics
	Log       *slog.Logger
	// Now replaces the clock in tests.
	Now func() time.Time
	// RatePerMinute is the per-token limit (600 when zero, I-25).
	RatePerMinute int

	mu      sync.Mutex
	windows map[int64]*window
	touched map[int64]time.Time
}

type window struct {
	start time.Time
	n     int
}

// TokenPrefix starts every SCIM token (I-24).
const TokenPrefix = "hello_scim_"

// DefaultRatePerMinute is the per-token limit (I-25).
const DefaultRatePerMinute = 600

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// BaseURL is the service's base URL, empty without HELLO_PUBLIC_URL.
func (s *Service) BaseURL() string {
	if s.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(s.PublicURL, "/") + "/scim/v2"
}

// Config is a scim connector's config.
type Config struct {
	// AdoptExisting lets a pushed user take over a local (non-admin) Hello
	// user with the same e-mail address or username (I-6): turning it on is
	// the administrator's confirmation, as there is no preview for a push.
	// Off, such a push is refused with 409 uniqueness.
	AdoptExisting bool `json:"adoptExisting"`
}

func configOf(c directory.Connector) Config {
	var cfg Config
	_ = json.Unmarshal(c.Config, &cfg)
	return cfg
}

// hashToken is what is stored of a token.
func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// newToken makes a token: the prefix and 256 random bits.
func newToken() (token, hint string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	tail := base64.RawURLEncoding.EncodeToString(b)
	return TokenPrefix + tail, TokenPrefix + tail[:4], nil
}

// newID makes a resource id: a random (version 4) UUID.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// IssueToken makes a token for a scim connector and returns it once.
func (s *Service) IssueToken(ctx context.Context, actor string, connectorID int64) (Token, string, error) {
	c, err := s.Engine.Get(ctx, connectorID)
	if err != nil {
		return Token{}, "", err
	}
	if c.Kind != directory.KindSCIM {
		return Token{}, "", ErrWrongKind
	}
	token, hint, err := newToken()
	if err != nil {
		return Token{}, "", err
	}
	t, err := s.Store.CreateToken(ctx, actor, connectorID, hashToken(token), hint)
	if err != nil {
		return Token{}, "", err
	}
	return t, token, nil
}

// ListTokens returns a connector's tokens, newest first.
func (s *Service) ListTokens(ctx context.Context, connectorID int64) ([]Token, error) {
	c, err := s.Engine.Get(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	if c.Kind != directory.KindSCIM {
		return nil, ErrWrongKind
	}
	return s.Store.ListTokens(ctx, connectorID)
}

// RevokeToken revokes a token at once.
func (s *Service) RevokeToken(ctx context.Context, actor string, id int64) (Token, error) {
	return s.Store.RevokeToken(ctx, actor, id)
}

// Status is a scim connector's state, for the console.
type Status struct {
	ConnectorID int64  `json:"connectorId"`
	BaseURL     string `json:"baseUrl"`
	Enabled     bool   `json:"enabled"`
	// PausedAt is set while the mass-deprovision guard holds the connector;
	// PausedRunID is the run to confirm to resume it.
	PausedAt      *time.Time            `json:"pausedAt"`
	PausedRunID   *int64                `json:"pausedRunId"`
	Guard         directory.GuardStatus `json:"guard"`
	Resources     Counts                `json:"resources"`
	LastRequestAt *time.Time            `json:"lastRequestAt"`
	ActiveTokens  int                   `json:"activeTokens"`
	AdoptExisting bool                  `json:"adoptExisting"`
}

// Status reports a connector's SCIM state.
func (s *Service) Status(ctx context.Context, connectorID int64) (Status, error) {
	c, err := s.Engine.Get(ctx, connectorID)
	if err != nil {
		return Status{}, err
	}
	if c.Kind != directory.KindSCIM {
		return Status{}, ErrWrongKind
	}
	st := Status{ConnectorID: c.ID, BaseURL: s.BaseURL(), Enabled: c.Enabled, PausedAt: c.PausedAt, AdoptExisting: configOf(c).AdoptExisting}
	if st.Guard, err = s.Engine.Guard(ctx, c); err != nil {
		return Status{}, err
	}
	if st.Resources, err = s.Store.Counts(ctx, c.ID); err != nil {
		return Status{}, err
	}
	if st.LastRequestAt, err = s.Store.LastUsed(ctx, c.ID); err != nil {
		return Status{}, err
	}
	ts, err := s.Store.ListTokens(ctx, c.ID)
	if err != nil {
		return Status{}, err
	}
	for _, t := range ts {
		if t.RevokedAt == nil {
			st.ActiveTokens++
		}
	}
	if c.PausedAt != nil {
		runs, err := s.Engine.ListRuns(ctx, c.ID, 50)
		if err != nil {
			return Status{}, err
		}
		for _, r := range runs {
			if r.Kind == directory.RunSCIM && r.Status == directory.StatusNeedsConfirmation {
				id := r.ID
				st.PausedRunID = &id
				break
			}
		}
	}
	return st, nil
}

// allow counts one request of a token and reports whether it is within the
// per-minute limit. The window is kept per replica.
func (s *Service) allow(tokenID int64) bool {
	limit := s.RatePerMinute
	if limit <= 0 {
		limit = DefaultRatePerMinute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.windows == nil {
		s.windows = map[int64]*window{}
	}
	now := s.now()
	w := s.windows[tokenID]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &window{start: now}
		s.windows[tokenID] = w
	}
	w.n++
	return w.n <= limit
}

// touch records a token's use, at most once a minute per replica.
func (s *Service) touch(ctx context.Context, tokenID int64) {
	now := s.now()
	s.mu.Lock()
	if s.touched == nil {
		s.touched = map[int64]time.Time{}
	}
	last, ok := s.touched[tokenID]
	if ok && now.Sub(last) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.touched[tokenID] = now
	s.mu.Unlock()
	if err := s.Store.TouchToken(ctx, tokenID, now.UTC()); err != nil {
		s.log().Warn("scim: record token use", "err", err)
	}
}

// refusalError answers a push the engine refused, by the conflict's reason.
func refusalError(ch directory.Change) *Error {
	switch ch.Reason {
	case directory.ReasonUsernameTaken:
		return errUniqueness("another Hello user has this username")
	case directory.ReasonEmailInUse:
		return errUniqueness("another Hello user has this e-mail address")
	case directory.ReasonClaimed:
		return errUniqueness("a Hello user with this username or e-mail address is managed by another directory connector")
	case directory.ReasonExtensionLinked:
		return errUniqueness("the extension is linked to another Hello user")
	case directory.ReasonExtensionDup:
		return errUniqueness("another user of this connector has the same extension")
	case directory.ReasonAdoptNotConfirmed:
		return errUniqueness("a local Hello user has this username or e-mail address; an administrator lets the connector adopt it (adoptExisting) or renames it")
	case directory.ReasonLocalAdmin:
		return errUniqueness("a local Hello administrator has this username or e-mail address; administrators are never taken over")
	case directory.ReasonExtensionInvalid:
		return errInvalidValue("the extension must be 2 to 10 digits")
	case directory.ReasonExtensionMissing:
		return errInvalidValue("the extension does not exist in Hello; an administrator creates it first")
	case directory.ReasonExtensionKind:
		return errInvalidValue("the extension number belongs to another kind of record (a door station)")
	case directory.ReasonUsernameInvalid:
		return errInvalidValue("the username is empty or has characters Hello does not allow (letters, digits and . _ @ + -)")
	case directory.ReasonLastAdmin:
		return errMutability("the last enabled Hello administrator cannot be disabled or demoted")
	}
	return errUniqueness("the change conflicts with Hello's users (" + ch.Reason + ")")
}

// pushError answers an engine failure.
func (s *Service) pushError(err error) *Error {
	switch {
	case errors.Is(err, directory.ErrPaused):
		e := &Error{Status: http.StatusTooManyRequests, Detail: "SCIM writes are paused: the mass-deprovision guard stopped this connector; an administrator confirms in Hello to resume"}
		e.headers = map[string]string{"Retry-After": "300"}
		return e
	case errors.Is(err, directory.ErrDisabled):
		return errStatus(http.StatusForbidden, "the SCIM connector is switched off in Hello")
	}
	s.log().Error("scim: push", "err", err)
	return errStatus(http.StatusInternalServerError, "the change could not be applied; see the Hello server log")
}

// saveError answers a store failure on save.
func (s *Service) saveError(typ string, err error) *Error {
	switch {
	case errors.Is(err, ErrConflict):
		return errUniqueness(nameAttr(typ) + " or externalId is already used by another " + typ)
	case errors.Is(err, ErrNotFound):
		return errStatus(http.StatusPreconditionFailed, "the resource changed meanwhile; read it again")
	}
	s.log().Error("scim: save", "err", err)
	return errStatus(http.StatusInternalServerError, "the resource could not be saved")
}

// unique checks that no other live resource has the name or externalId.
func (s *Service) unique(ctx context.Context, typ string, r Resource) *Error {
	others, err := s.Store.FindResources(ctx, r.ConnectorID, typ, &Hint{Field: "name", Value: r.Name})
	if err != nil {
		return s.saveError(typ, err)
	}
	for _, o := range others {
		if o.ID != r.ID {
			return errUniqueness(nameAttr(typ) + " is already used by another " + typ)
		}
	}
	if r.ExternalID == "" {
		return nil
	}
	others, err = s.Store.FindResources(ctx, r.ConnectorID, typ, &Hint{Field: "external_id", Value: r.ExternalID})
	if err != nil {
		return s.saveError(typ, err)
	}
	for _, o := range others {
		if o.ID != r.ID {
			return errUniqueness("externalId is already used by another " + typ)
		}
	}
	return nil
}

// writeUser pushes a user's new state through the engine and, when the
// engine took it, stores it. Called under the connector's lock.
func (s *Service) writeUser(ctx context.Context, c directory.Connector, next Resource) (Resource, *Error) {
	if e := s.unique(ctx, TypeUser, next); e != nil {
		return Resource{}, e
	}
	groups, err := s.Store.GroupsOf(ctx, []string{next.ID})
	if err != nil {
		return Resource{}, s.saveError(TypeUser, err)
	}
	res, err := s.Engine.Push(ctx, c, []directory.SourceUser{sourceUser(next, groups[next.ID])},
		directory.PushOptions{Strict: true, Adopt: configOf(c).AdoptExisting})
	if err != nil {
		return Resource{}, s.pushError(err)
	}
	if res.Refused != nil {
		return Resource{}, refusalError(*res.Refused)
	}
	saved, err := s.Store.SaveResource(ctx, next, nil)
	if err != nil {
		return Resource{}, s.saveError(TypeUser, err)
	}
	return saved, nil
}

// deleteUser disables the user's Hello user and marks the resource deleted.
func (s *Service) deleteUser(ctx context.Context, c directory.Connector, r Resource) *Error {
	groups, err := s.Store.GroupsOf(ctx, []string{r.ID})
	if err != nil {
		return s.saveError(TypeUser, err)
	}
	res, err := s.Engine.Push(ctx, c, []directory.SourceUser{sourceUser(r, groups[r.ID])}, directory.PushOptions{Delete: true, Strict: true})
	if err != nil {
		return s.pushError(err)
	}
	if res.Refused != nil {
		return refusalError(*res.Refused)
	}
	if err := s.Store.MarkDeleted(ctx, c.ID, TypeUser, r.ID); err != nil {
		return s.saveError(TypeUser, err)
	}
	return nil
}

// ruled reports whether a group rule of the connector names the group.
func ruled(c directory.Connector, g Resource) bool {
	for _, id := range c.GroupRules.GroupIDs() {
		if id == g.ID || g.ExternalID != "" && id == g.ExternalID {
			return true
		}
	}
	return false
}

// writeGroup stores a group and its members, then pushes the users whose
// membership changed again when a group rule names the group (their role
// and ring groups follow the membership, I-4). Called under the lock.
func (s *Service) writeGroup(ctx context.Context, c directory.Connector, old *Resource, next Resource, members []string) (Resource, *Error) {
	if e := s.unique(ctx, TypeGroup, next); e != nil {
		return Resource{}, e
	}
	members = uniqueStrings(members)
	known, err := s.Store.Existing(ctx, c.ID, members)
	if err != nil {
		return Resource{}, s.saveError(TypeGroup, err)
	}
	for _, m := range members {
		if !known[m] {
			return Resource{}, errInvalidValue("member " + m + " is not a user of this connector")
		}
	}
	var before []string
	if old != nil {
		ms, err := s.Store.Members(ctx, []string{old.ID})
		if err != nil {
			return Resource{}, s.saveError(TypeGroup, err)
		}
		for _, m := range ms[old.ID] {
			before = append(before, m.ID)
		}
	}
	saved, err := s.Store.SaveResource(ctx, next, members)
	if err != nil {
		return Resource{}, s.saveError(TypeGroup, err)
	}
	if ruled(c, saved) || old != nil && ruled(c, *old) {
		ids := symmetricDiff(before, members)
		if old == nil || old.ExternalID != saved.ExternalID {
			// The externalId a rule may name changed: every member.
			ids = uniqueStrings(append(append([]string{}, before...), members...))
		}
		s.repush(ctx, c, ids)
	}
	return saved, nil
}

// deleteGroup deletes a group and pushes its former members again when a
// rule named it.
func (s *Service) deleteGroup(ctx context.Context, c directory.Connector, g Resource) *Error {
	ms, err := s.Store.Members(ctx, []string{g.ID})
	if err != nil {
		return s.saveError(TypeGroup, err)
	}
	if err := s.Store.MarkDeleted(ctx, c.ID, TypeGroup, g.ID); err != nil {
		return s.saveError(TypeGroup, err)
	}
	if ruled(c, g) {
		ids := make([]string, 0, len(ms[g.ID]))
		for _, m := range ms[g.ID] {
			ids = append(ids, m.ID)
		}
		s.repush(ctx, c, ids)
	}
	return nil
}

// repush plans and applies users again after a group change. A conflict of
// one member is recorded on the SCIM run and does not fail the group write.
func (s *Service) repush(ctx context.Context, c directory.Connector, ids []string) {
	if len(ids) == 0 {
		return
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	all, err := s.Store.FindResources(ctx, c.ID, TypeUser, nil)
	if err != nil {
		s.log().Error("scim: repush members", "connector", c.ID, "err", err)
		return
	}
	var live []Resource
	for _, r := range all {
		if want[r.ID] {
			live = append(live, r)
		}
	}
	groups, err := s.Store.GroupsOf(ctx, ids)
	if err != nil {
		s.log().Error("scim: repush members", "connector", c.ID, "err", err)
		return
	}
	users := make([]directory.SourceUser, len(live))
	for i, r := range live {
		users[i] = sourceUser(r, groups[r.ID])
	}
	if len(users) == 0 {
		return
	}
	if _, err := s.Engine.Push(ctx, c, users, directory.PushOptions{Adopt: configOf(c).AdoptExisting}); err != nil {
		s.log().Warn("scim: members not updated after a group change", "connector", c.ID, "err", err)
	}
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func symmetricDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			out = append(out, x)
		}
	}
	return out
}

// membersOf reads the member ids of a document's members attribute.
func membersOf(doc map[string]any) []string {
	var out []string
	for _, it := range elements(doc["members"]) {
		if m, ok := it.(map[string]any); ok {
			if v, _ := m["value"].(string); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// Driver is the scim connector kind of the directory engine: its source is
// what was pushed. A preview or apply run re-maps every pushed user with the
// current mappings and rules.
type Driver struct{ Store Store }

// Push implements directory.PushDriver.
func (Driver) Push() bool { return true }

// Validate implements directory.Driver.
func (Driver) Validate(raw json.RawMessage, _ directory.ValidateOptions) (json.RawMessage, error) {
	var cfg Config
	if len(raw) > 0 && string(raw) != "null" {
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if err := d.Decode(&cfg); err != nil {
			return nil, &directory.ValidationError{Fields: []directory.FieldError{{Path: "config", Message: "a scim config has only adoptExisting"}}}
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Identity implements directory.Driver: a scim connector seals no secret.
func (Driver) Identity(json.RawMessage) string { return "" }

// DefaultMappings implements directory.Driver.
func (Driver) DefaultMappings() directory.Mappings { return DefaultMappings() }

func (d Driver) read(ctx context.Context, connectorID int64, limit int) ([]directory.SourceUser, []directory.SourceGroup, error) {
	users, err := d.Store.FindResources(ctx, connectorID, TypeUser, nil)
	if err != nil {
		return nil, nil, err
	}
	if limit > 0 && len(users) > limit {
		return nil, nil, &directory.Error{Code: directory.CodeUserCap, Detail: fmt.Sprintf("more than %d pushed users", limit)}
	}
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	groups, err := d.Store.GroupsOf(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	out := make([]directory.SourceUser, len(users))
	for i, u := range users {
		out[i] = sourceUser(u, groups[u.ID])
	}
	gs, err := d.Store.FindResources(ctx, connectorID, TypeGroup, nil)
	if err != nil {
		return nil, nil, err
	}
	sg := make([]directory.SourceGroup, 0, len(gs))
	for _, g := range gs {
		name := g.Name
		if g.ExternalID != "" {
			name += " (externalId " + g.ExternalID + ")"
		}
		sg = append(sg, directory.SourceGroup{ID: g.ID, Name: name})
	}
	return out, sg, nil
}

// Fetch implements directory.Driver: every pushed, not deleted user.
func (d Driver) Fetch(ctx context.Context, _ json.RawMessage, _ string, o directory.FetchOptions) (*directory.Result, error) {
	users, groups, err := d.read(ctx, o.ConnectorID, o.Limit)
	if err != nil {
		return nil, err
	}
	return &directory.Result{Users: users, Groups: groups}, nil
}

// Test implements directory.Driver: what was pushed, the sample of the
// mapping editor and the groups for the rules.
func (d Driver) Test(ctx context.Context, _ json.RawMessage, _ string, o directory.FetchOptions) (*directory.TestResult, error) {
	users, groups, err := d.read(ctx, o.ConnectorID, 0)
	if err != nil {
		return nil, err
	}
	res := &directory.TestResult{OK: true, MatchedUsers: len(users), Sample: []directory.SampleRow{}, Groups: []directory.SourceGroup{}}
	for i, u := range users {
		if i == 20 {
			break
		}
		res.Sample = append(res.Sample, directory.SampleRow{ExternalID: u.ExternalID, Raw: u.Attrs})
	}
	for i, g := range groups {
		if i == 50 {
			break
		}
		res.Groups = append(res.Groups, g)
	}
	return res, nil
}
