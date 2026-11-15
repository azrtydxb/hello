package directory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// autoRig is a rig whose connector auto-provisions from lo to hi.
func autoRig(t *testing.T, lo, hi string, mod func(*AutoProvision)) *rig {
	t.Helper()
	r := newRig(t)
	ap := AutoProvision{Enabled: true, RangeStart: lo, RangeEnd: hi, Device: true, Voicemail: true}
	if mod != nil {
		mod(&ap)
	}
	c, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{AutoProvision: &ap})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	return r
}

func (r *rig) dropExt(number string) {
	r.t.Helper()
	e := r.m.Exts[number]
	if e == nil {
		r.t.Fatalf("no extension %s", number)
	}
	delete(r.m.Exts, number)
	delete(r.m.Provisioned, number)
	for _, u := range r.m.Users {
		u.ExtensionIDs = slices.DeleteFunc(u.ExtensionIDs, func(id int64) bool { return id == e.ID })
		if u.OwnedExtension == e.ID {
			u.OwnedExtension = 0
		}
	}
}

func extOf(r *rig, u *MemUser) string {
	for n, e := range r.m.Exts {
		if e.ID == u.OwnedExtension && u.OwnedExtension != 0 {
			return n
		}
	}
	return ""
}

func TestAutoProvisionPreviewAndApply(t *testing.T) {
	r := autoRig(t, "2000", "2003", nil)
	r.m.AddExt("2001", false)
	r.m.Taken["2000"] = true // a ring group answers to 2000
	r.d.res = &Result{Users: []SourceUser{
		su("a", "ann@corp", "ann@corp.org", "Ann A", ""),
		su("b", "bob@corp", "", "Bob B", "9999"), // names no extension
		su("c", "cat@corp", "cat@corp.org", "Cat C", "2001"),
	}}
	pre := r.preview()
	cs := r.changes(pre)
	if a := cs["a"]; a.Action != ActionCreate || a.Reason != ReasonAutoProvisioned || a.FieldDiffs["extension"].After != "2002" ||
		a.FieldDiffs["device"].After != "2002" || a.FieldDiffs["voicemail"].After != "ann@corp.org" {
		t.Fatalf("a = %+v", a)
	}
	if b := cs["b"]; b.Reason != ReasonAutoProvisioned || b.FieldDiffs["extension"].After != "2003" || b.FieldDiffs["voicemail"].After != "on" {
		t.Fatalf("b = %+v", b)
	}
	if c := cs["c"]; c.Reason != "" || c.FieldDiffs["extension"].After != "2001" || c.FieldDiffs["device"] != (Diff{}) {
		t.Fatalf("c = %+v", c)
	}
	if len(r.m.Provisioned) != 0 {
		t.Fatal("a preview provisioned")
	}
	if _, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{}); err != nil {
		t.Fatal(err)
	}
	ann, bob, cat := r.user("ann@corp"), r.user("bob@corp"), r.user("cat@corp")
	if extOf(r, ann) != "2002" || ann.ProvisionedNumber != "2002" || extOf(r, bob) != "2003" || extOf(r, cat) != "2001" || cat.ProvisionedNumber != "" {
		t.Fatalf("ann %+v bob %+v cat %+v", ann, bob, cat)
	}
	if p := r.m.Provisioned["2002"]; p.Name != "Ann A" || p.Email != "ann@corp.org" || !p.Device || !p.Voicemail || p.Connector != r.c.ID {
		t.Fatalf("2002 = %+v", p)
	}
	if !slices.ContainsFunc(r.m.Audits, func(a string) bool { return strings.HasPrefix(a, "directory:AD provision user") }) {
		t.Fatalf("audits = %v", r.m.Audits)
	}

	// Idempotent: a re-run changes nothing and never renumbers.
	again := r.apply()
	if again.Status != StatusSucceeded || again.Counts[ActionCreate]+again.Counts[ActionUpdate] != 0 {
		t.Fatalf("re-run = %+v", again)
	}
	if len(r.m.Provisioned) != 2 {
		t.Fatalf("provisioned = %v", r.m.Provisioned)
	}
}

func TestAutoProvisionRangeExhausted(t *testing.T) {
	r := autoRig(t, "20", "21", nil)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", ""), su("c", "c@c", "", "C", "")}}
	run := r.apply()
	cs := r.changes(run)
	if cs["a"].FieldDiffs["extension"].After != "20" || cs["b"].FieldDiffs["extension"].After != "21" {
		t.Fatalf("changes = %+v", cs)
	}
	if c := cs["c"]; c.Action != ActionConflict || c.Reason != ReasonRangeExhausted || c.Overridable {
		t.Fatalf("c = %+v", c)
	}
	if r.user("c@c") != nil {
		t.Fatal("a user without a number was created")
	}
	// Widening the range lets the next run create it.
	ap := r.c.AutoProvision
	ap.RangeEnd = "22"
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{AutoProvision: &ap}); err != nil {
		t.Fatal(err)
	}
	r.apply()
	if u := r.user("c@c"); u == nil || extOf(r, u) != "22" {
		t.Fatalf("c = %+v", u)
	}
}

// A number the preview predicted that is taken before the apply (an
// administrator created it) is replaced by the next free one under the lock.
func TestAutoProvisionCollisionAtApply(t *testing.T) {
	r := autoRig(t, "3000", "3009", nil)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "a@c.org", "A", "")}}
	ctx := context.Background()
	c, _ := r.m.GetConnector(ctx, r.c.ID)
	st, _ := r.m.LoadState(ctx, c.ID)
	plan := MakePlan(PlanInput{Connector: c, State: st, Result: r.d.res})
	if got := plan.Changes[0].FieldDiffs["extension"].After; got != "3000" {
		t.Fatalf("predicted %s", got)
	}
	r.m.AddExt("3000", false) // the administrator's own extension
	run, err := r.m.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunApply, Status: StatusRunning, Counts: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	done, err := r.e.applyPlan(ctx, c, run, plan, r.d.res, r.now, false)
	if err != nil || done.Status != StatusSucceeded {
		t.Fatalf("apply = %+v, %v", done, err)
	}
	rows := r.changes(done)
	if rows["a"].FieldDiffs["extension"].After != "3001" || extOf(r, r.user("a@c")) != "3001" {
		t.Fatalf("row = %+v", rows["a"])
	}
}

func TestAutoProvisionApplyExhaustedIsErrorRow(t *testing.T) {
	r := autoRig(t, "40", "40", nil)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	ctx := context.Background()
	c, _ := r.m.GetConnector(ctx, r.c.ID)
	st, _ := r.m.LoadState(ctx, c.ID)
	plan := MakePlan(PlanInput{Connector: c, State: st, Result: r.d.res})
	r.m.Taken["40"] = true
	run, _ := r.m.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunApply, Status: StatusRunning, Counts: map[string]int{}})
	done, err := r.e.applyPlan(ctx, c, run, plan, r.d.res, r.now, false)
	if err != nil {
		t.Fatal(err)
	}
	if row := r.changes(done)["a"]; row.Action != ActionError || row.Reason != ReasonRangeExhausted {
		t.Fatalf("row = %+v", row)
	}
	if r.user("a@c") != nil {
		t.Fatal("the user was created without its extension")
	}
}

// A user's number is stable: a deleted extension comes back with the same
// number, and an existing unlinked one is linked again rather than replaced.
func TestAutoProvisionStableNumber(t *testing.T) {
	r := autoRig(t, "5000", "5009", nil)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	r.apply()
	if extOf(r, r.user("a@c")) != "5000" || extOf(r, r.user("b@c")) != "5001" {
		t.Fatal("first allocation")
	}
	r.dropExt("5000")
	run := r.apply()
	if row := r.changes(run)["a"]; row.Action != ActionUpdate || row.FieldDiffs["extension"].After != "5000" || row.Reason != ReasonAutoProvisioned {
		t.Fatalf("row = %+v", row)
	}
	if extOf(r, r.user("a@c")) != "5000" {
		t.Fatal("renumbered")
	}
	// An administrator unlinks 5001 from b: the next run links it again.
	b := r.user("b@c")
	id := b.OwnedExtension
	b.OwnedExtension, b.ExtensionIDs = 0, nil
	r.m.Exts["5001"].UserID = 0
	before := len(r.m.Provisioned)
	run = r.apply()
	if row := r.changes(run)["b"]; row.FieldDiffs["extension"].After != "5001" || row.FieldDiffs["device"] != (Diff{}) {
		t.Fatalf("row = %+v", row)
	}
	if b.OwnedExtension != id || len(r.m.Provisioned) != before {
		t.Fatalf("b = %+v", b)
	}
}

func TestAutoProvisionGroups(t *testing.T) {
	r := autoRig(t, "6000", "6009", func(a *AutoProvision) { a.Groups = []string{"phones"} })
	r.d.res = &Result{Users: []SourceUser{
		su("a", "a@c", "", "A", "", "phones"),
		su("b", "b@c", "", "B", ""),
		su("c", "c@c", "", "C", "7777"),
	}}
	run := r.apply()
	cs := r.changes(run)
	if cs["a"].FieldDiffs["extension"].After != "6000" {
		t.Fatalf("a = %+v", cs["a"])
	}
	if b := cs["b"]; b.Action != ActionCreate || b.FieldDiffs["extension"] != (Diff{}) {
		t.Fatalf("b = %+v", b)
	}
	if c := cs["c"]; c.Action != ActionConflict || c.Reason != ReasonExtensionMissing {
		t.Fatalf("c = %+v", c)
	}
	if got := r.c.needGroups(); !slices.Contains(got, "phones") {
		t.Fatalf("need groups = %v", got)
	}
}

func TestAutoProvisionOptions(t *testing.T) {
	r := autoRig(t, "70", "79", func(a *AutoProvision) { a.Device, a.Voicemail = false, false })
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "a@c.org", "", "")}}
	run := r.apply()
	row := r.changes(run)["a"]
	if row.FieldDiffs["device"] != (Diff{}) || row.FieldDiffs["voicemail"] != (Diff{}) || row.FieldDiffs["extension"].After != "70" {
		t.Fatalf("row = %+v", row)
	}
	if p := r.m.Provisioned["70"]; p.Device || p.Voicemail || p.Email != "" || p.Name != "a@c" {
		t.Fatalf("70 = %+v", p)
	}
}

func TestAutoProvisionCreateOnly(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Mode: strPtr(ModeCreateOnly)}); err != nil {
		t.Fatal(err)
	}
	r.apply() // created without a number: provisioning was off
	ap := AutoProvision{Enabled: true, RangeStart: "80", RangeEnd: "89", Device: true, Voicemail: true}
	c, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{AutoProvision: &ap})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	run := r.apply()
	if row := r.changes(run)["a"]; row.Action != ActionUpdate || row.Reason != ReasonAutoProvisioned || row.FieldDiffs["extension"].After != "80" {
		t.Fatalf("row = %+v", row)
	}
	// Hello owns the user now: a deleted extension is not recreated.
	r.dropExt("80")
	run = r.apply()
	if n := run.Counts[ActionUpdate]; n != 0 {
		t.Fatalf("create_only re-provisioned: %+v", run)
	}
}

// Deprovisioning disables the user and its devices and keeps the extension
// and its voicemail (I-7).
func TestAutoProvisionDeprovisionKeepsExtension(t *testing.T) {
	r := autoRig(t, "90", "99", nil)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	r.apply()
	r.d.res = &Result{Users: []SourceUser{su("b", "b@c", "", "B", "")}}
	r.apply()
	r.apply()
	a := r.user("a@c")
	if a.Enabled || a.Devices {
		t.Fatalf("a = %+v", a)
	}
	if _, ok := r.m.Exts["90"]; !ok || extOf(r, a) != "90" {
		t.Fatal("the extension of a disabled user went away")
	}
	if _, ok := r.m.Provisioned["90"]; !ok {
		t.Fatal("the voicemail of a disabled user went away")
	}
	// It comes back with the same number.
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	r.apply()
	if !a.Enabled || extOf(r, a) != "90" {
		t.Fatalf("a = %+v", a)
	}
}

func TestAutoProvisionOffKeepsConflict(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "1234")}}
	run := r.apply()
	if row := r.changes(run)["a"]; row.Reason != ReasonExtensionMissing {
		t.Fatalf("row = %+v", row)
	}
}

func TestAutoProvisionJSON(t *testing.T) {
	var a AutoProvision
	if err := json.Unmarshal([]byte(`{}`), &a); err != nil || !a.Device || !a.Voicemail || a.Enabled || a.Groups == nil {
		t.Fatalf("defaults = %+v, %v", a, err)
	}
	if err := json.Unmarshal([]byte(`{"enabled":true,"device":false}`), &a); err != nil || a.Device || !a.Voicemail {
		t.Fatalf("explicit = %+v, %v", a, err)
	}
	if err := json.Unmarshal([]byte(`{"rangeStrat":"1"}`), &a); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	for _, bad := range []AutoProvision{
		{Enabled: true, RangeStart: "1", RangeEnd: "9"},
		{Enabled: true, RangeStart: "0000000000", RangeEnd: "9999999999"},
		{RangeStart: "20", RangeEnd: "2x"},
		{Groups: []string{""}},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
	if (AutoProvision{RangeStart: "0100", RangeEnd: "0199"}).Validate() != nil {
		t.Error("a zero-padded range was refused")
	}
	var got []string
	AutoProvision{RangeStart: "098", RangeEnd: "101"}.Numbers(func(n string) bool { got = append(got, n); return true })
	if strings.Join(got, ",") != "098,099,100,101" {
		t.Fatalf("numbers = %v", got)
	}
	if got := ExtensionName("  Ann\x00 B  ", "x"); got != "Ann B" {
		t.Fatalf("name not cleaned: %q", got)
	}
	if ExtensionName(" \t", "2000") != "2000" || len([]rune(ExtensionName(strings.Repeat("é", 300), ""))) != MaxExtensionName {
		t.Fatal("name fallback or length")
	}
}

func TestRangeUsage(t *testing.T) {
	r := autoRig(t, "2000", "2004", nil)
	r.m.AddExt("2001", false)
	r.m.Taken["2003"] = true
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	r.apply()
	u, err := r.e.RangeUsage(context.Background(), r.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Size != 5 || u.Used != 3 || u.Free != 2 || u.NextFree != "2002" || len(u.Provisioned) != 1 || u.Provisioned[0].Number != "2000" || u.Provisioned[0].ExtensionID == 0 {
		t.Fatalf("usage = %+v", u)
	}
	if _, err := r.e.RangeUsage(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing connector: %v", err)
	}
}

// Enabling provisioning changes the fingerprint, so the next scheduled run is
// a full one and reaches every user without a number.
func TestAutoProvisionForcesFullRun(t *testing.T) {
	r := newRig(t)
	before := syncFingerprint(r.c)
	ap := AutoProvision{Enabled: true, RangeStart: "20", RangeEnd: "29"}
	c, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{AutoProvision: &ap})
	if err != nil {
		t.Fatal(err)
	}
	if syncFingerprint(c) == before {
		t.Fatal("the fingerprint ignores auto-provisioning")
	}
}
