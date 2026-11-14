package directory

// Range auto-provisioning (spec integrations I-11, P12): a connector may give
// a user that has no usable extension the next free number of a configured
// range, with an extension named after the user, a voicemail box that mails
// the user and one SIP device whose secret nobody sees. The planner predicts
// the number for the preview; the apply allocates it for real under the
// configuration lock, so two runs and an administrator's own edits cannot
// take the same number. A number once allocated to a user is kept on the
// user, so a re-run never renumbers.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Change reasons of auto-provisioning.
const (
	// ReasonAutoProvisioned marks a create or update row that allocates an
	// extension from the range.
	ReasonAutoProvisioned = "auto_provisioned"
	// ReasonRangeExhausted is a user that needs a number when the range has
	// none free: the user is left unchanged until the range grows.
	ReasonRangeExhausted = "range_exhausted"
)

// MaxRangeSize bounds a range, so an allocation scans a bounded set.
const MaxRangeSize = 100000

// ErrRangeExhausted is an allocation that found no free number.
var ErrRangeExhausted = errors.New("directory: no free number in the auto-provisioning range")

// AutoProvision is a connector's range auto-provisioning (I-11). It is off by
// default; Device and Voicemail default to on.
type AutoProvision struct {
	Enabled bool `json:"enabled"`
	// RangeStart and RangeEnd are the first and the last number, inclusive,
	// of the same length (2 to 10 digits): 2000 and 2999.
	RangeStart string `json:"rangeStart"`
	RangeEnd   string `json:"rangeEnd"`
	// Device creates one SIP device on the extension; its secret is generated
	// and shown nowhere (an administrator rotates it when a phone is set up).
	Device bool `json:"device"`
	// Voicemail turns the extension's voicemail on and sends messages to the
	// user's email.
	Voicemail bool `json:"voicemail"`
	// Groups, when set, limit provisioning to members of these source groups
	// (stable ids); other users without an extension are created without one.
	Groups []string `json:"groups"`
}

// UnmarshalJSON reads a stored or sent setting: unknown keys are refused and
// the switches that are absent are on.
func (a *AutoProvision) UnmarshalJSON(b []byte) error {
	type plain AutoProvision
	v := plain{Device: true, Voicemail: true}
	if !bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			return err
		}
	}
	if v.Groups == nil {
		v.Groups = []string{}
	}
	*a = AutoProvision(v)
	return nil
}

// DefaultAutoProvision is a new connector's setting: off.
func DefaultAutoProvision() AutoProvision {
	return AutoProvision{Device: true, Voicemail: true, Groups: []string{}}
}

var rangeNumberRe = regexp.MustCompile(`^[0-9]{2,10}$`)

// Validate checks the setting. A range is required only while it is on, but
// a range that is given must be valid either way.
func (a AutoProvision) Validate() *ValidationError {
	var v ValidationError
	add := func(path, msg string) { v.Fields = append(v.Fields, FieldError{path, msg}) }
	if a.Enabled || a.RangeStart != "" || a.RangeEnd != "" {
		okS, okE := rangeNumberRe.MatchString(a.RangeStart), rangeNumberRe.MatchString(a.RangeEnd)
		if !okS {
			add("autoProvision.rangeStart", "must be 2 to 10 digits")
		}
		if !okE {
			add("autoProvision.rangeEnd", "must be 2 to 10 digits")
		}
		if okS && okE {
			lo, hi := a.bounds()
			switch {
			case len(a.RangeStart) != len(a.RangeEnd):
				add("autoProvision.rangeEnd", "must have as many digits as the start")
			case hi < lo:
				add("autoProvision.rangeEnd", "must not be below the start")
			case hi-lo+1 > MaxRangeSize:
				add("autoProvision.rangeEnd", "a range holds at most "+strconv.Itoa(MaxRangeSize)+" numbers")
			}
		}
	}
	if len(a.Groups) > 200 {
		add("autoProvision.groups", "at most 200 groups")
	}
	for i, g := range a.Groups {
		if g == "" || len(g) > 512 {
			add("autoProvision.groups["+strconv.Itoa(i)+"]", "required (at most 512 characters)")
		}
	}
	if len(v.Fields) == 0 {
		return nil
	}
	return &v
}

func (a AutoProvision) bounds() (int64, int64) {
	lo, _ := strconv.ParseInt(a.RangeStart, 10, 64)
	hi, _ := strconv.ParseInt(a.RangeEnd, 10, 64)
	return lo, hi
}

// Numbers calls fn with every number of the range in order until fn returns
// false.
func (a AutoProvision) Numbers(fn func(string) bool) {
	if !rangeNumberRe.MatchString(a.RangeStart) || len(a.RangeStart) != len(a.RangeEnd) {
		return
	}
	lo, hi := a.bounds()
	w := len(a.RangeStart)
	for n := lo; n <= hi; n++ {
		if !fn(fmt.Sprintf("%0*d", w, n)) {
			return
		}
	}
}

// InRange reports whether number is a number of the range.
func (a AutoProvision) InRange(number string) bool {
	if len(number) != len(a.RangeStart) || !rangeNumberRe.MatchString(number) {
		return false
	}
	lo, hi := a.bounds()
	n, err := strconv.ParseInt(number, 10, 64)
	return err == nil && n >= lo && n <= hi
}

// active reports whether provisioning is on with a usable range.
func (a AutoProvision) active() bool { return a.Enabled && a.Validate() == nil }

// wants reports whether a user of these groups is provisioned.
func (a AutoProvision) wants(groups []string) bool {
	if len(a.Groups) == 0 {
		return true
	}
	for _, g := range groups {
		for _, w := range a.Groups {
			if g == w {
				return true
			}
		}
	}
	return false
}

// Provision is what applying a change allocates: the number the preview
// predicted (tried first, then the range in order) and what to create.
type Provision struct {
	Preferred string
	Range     AutoProvision
	Name      string
	Email     string
}

// ProvisionRequest asks a Provisioner for one extension.
type ProvisionRequest struct {
	ConnectorID int64
	// Preferred is tried first (the user's earlier number, or the number the
	// preview showed); then RangeStart to RangeEnd in order.
	Preferred  string
	RangeStart string
	RangeEnd   string
	// Name is the extension's name (the user's display name).
	Name string
	// Email receives the voicemail when Voicemail is on.
	Email     string
	Voicemail bool
	Device    bool
}

// ProvisionResult is what a Provisioner created.
type ProvisionResult struct {
	ExtensionID int64
	Number      string
	// DeviceID is the SIP device (0 when none was asked for).
	DeviceID int64
}

// Provisioner creates auto-provisioned extensions. The PostgreSQL store
// delegates to it so that the numbering rules and the whole-configuration
// check are the configuration store's own (hello-control wires the store).
type Provisioner interface {
	// Taken lists the numbers between lo and hi (same length) that something
	// in the dial space already claims: an extension, a ring group, IVR menu,
	// conference room, queue or voicemail group number, a park slot, or a
	// number a feature or paging code is a prefix of (or that is a prefix of
	// one).
	Taken(ctx context.Context, tx *sql.Tx, lo, hi string) (map[string]bool, error)
	// Provision runs inside tx, which holds the configuration lock: it
	// allocates the first free number, creates the extension (marked with the
	// connector), its voicemail box and, when asked, the device, runs the
	// whole-configuration check and audits as actor. No free number is
	// ErrRangeExhausted.
	Provision(ctx context.Context, tx *sql.Tx, actor string, req ProvisionRequest) (ProvisionResult, error)
}

// Applied is what applying one operation produced.
type Applied struct {
	// Extension is the number auto-provisioning allocated ("" none).
	Extension string
}

// RangeUsage is how a connector's range is used.
type RangeUsage struct {
	Enabled    bool   `json:"enabled"`
	RangeStart string `json:"rangeStart"`
	RangeEnd   string `json:"rangeEnd"`
	// Size is how many numbers the range holds; Used how many of them are
	// claimed (by anything); Free the rest.
	Size int `json:"size"`
	Used int `json:"used"`
	Free int `json:"free"`
	// NextFree is the number the next allocation takes ("" none free).
	NextFree string `json:"nextFree"`
	// Provisioned are the users of this connector with an allocated number.
	Provisioned []ProvisionedUser `json:"provisioned"`
}

// ProvisionedUser is one user's allocation.
type ProvisionedUser struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	Number   string `json:"number"`
	// ExtensionID is the extension of that number (0 when it no longer
	// exists: the next run creates it again with the same number).
	ExtensionID int64 `json:"extensionId"`
	Enabled     bool  `json:"enabled"`
}

// Usage computes the range usage from a loaded state.
func (a AutoProvision) Usage(st State, connectorID int64) RangeUsage {
	u := RangeUsage{Enabled: a.Enabled, RangeStart: a.RangeStart, RangeEnd: a.RangeEnd, Provisioned: []ProvisionedUser{}}
	a.Numbers(func(n string) bool {
		u.Size++
		if _, ext := st.Exts[n]; ext || st.Taken[n] {
			u.Used++
		} else if u.NextFree == "" {
			u.NextFree = n
		}
		return true
	})
	u.Free = u.Size - u.Used
	for _, lu := range st.Users {
		if lu.ConnectorID != connectorID || lu.ProvisionedNumber == "" {
			continue
		}
		p := ProvisionedUser{UserID: lu.ID, Username: lu.Username, Number: lu.ProvisionedNumber, Enabled: lu.Enabled}
		if e, ok := st.Exts[lu.ProvisionedNumber]; ok {
			p.ExtensionID = e.ID
		}
		u.Provisioned = append(u.Provisioned, p)
	}
	return u
}

// RangeUsage returns how a connector's range is used.
func (e *Engine) RangeUsage(ctx context.Context, id int64) (RangeUsage, error) {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return RangeUsage{}, err
	}
	st, err := e.Store.LoadState(ctx, c.ID)
	if err != nil {
		return RangeUsage{}, err
	}
	return c.AutoProvision.Usage(st, c.ID), nil
}

// MaxExtensionName is the longest extension name the console accepts.
const MaxExtensionName = 100

// ExtensionName is the name of an extension auto-provisioning creates for a
// user called name: trimmed, without control characters, at most 100
// characters; fallback when nothing is left.
func ExtensionName(name, fallback string) string {
	out := make([]rune, 0, len(name))
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
		if len(out) == MaxExtensionName {
			break
		}
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return fallback
}
