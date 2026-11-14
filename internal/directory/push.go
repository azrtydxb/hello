package directory

import (
	"context"
	"strconv"
	"time"
)

// Pushes: a source that sends its changes (SCIM, I-23 to I-25) instead of
// being read. A push runs the same planner as a sync run on the users it
// names, with the absence rule off (a push says nothing about the users it
// does not name), and applies the plan at once: there is no dry run for a
// push. Its changes are recorded on a run of kind scim, one per connector and
// hour, so the Runs view shows what the identity provider did.

// PushOptions shape one push.
type PushOptions struct {
	// Delete disables the users whatever their attributes say (SCIM DELETE).
	Delete bool
	// Adopt confirms the adoption of a matching local user: the connector's
	// adoptExisting switch is the administrator's confirmation (I-6). Local
	// administrators are never adopted.
	Adopt bool
	// Strict refuses the whole push when any of its users is in conflict (a
	// single-user SCIM write, where the IdP must see the error); otherwise a
	// conflict is recorded and the other users are applied.
	Strict bool
}

// PushResult is what a push did.
type PushResult struct {
	// Changes are the rows recorded: applied changes, skips and conflicts.
	Changes []Change
	// Refused is the change that refused a Strict push (nothing was
	// applied); nil when the push went through.
	Refused *Change
}

// refusal reports whether a change stops a strict push: a conflict, an
// adoption the administrator did not allow, or a local administrator.
func refusal(c Change) bool {
	switch {
	case c.Action == ActionConflict:
		return true
	case c.Action == ActionAdopt && c.op == nil:
		return true
	case c.Action == ActionSkip && c.Reason == ReasonLocalAdmin:
		return true
	}
	return false
}

// guardLimit is how many disables the guard allows: 20 percent of the linked
// users, at least 5.
func guardLimit(linked int) int {
	limit := linked * GuardPercent / 100
	if limit < GuardMinimum {
		limit = GuardMinimum
	}
	return limit
}

// GuardWindow is the rolling window the SCIM mass-deprovision guard counts
// disables in (I-25).
const GuardWindow = time.Hour

// GuardStatus is the state of a push connector's mass-deprovision guard.
type GuardStatus struct {
	// Disables is how many users pushes disabled in the window.
	Disables int `json:"disables"`
	// Limit is how many the guard allows before it pauses the connector.
	Limit int `json:"limit"`
	// Linked is how many enabled users the connector manages.
	Linked int `json:"linked"`
}

func (e *Engine) guardSince(c Connector) time.Time {
	since := e.now().Add(-GuardWindow)
	if c.GuardSince != nil && c.GuardSince.After(since) {
		since = *c.GuardSince
	}
	return since
}

// Guard reports the guard's state for a connector.
func (e *Engine) Guard(ctx context.Context, c Connector) (GuardStatus, error) {
	n, err := e.Store.PushDisables(ctx, c.ID, e.guardSince(c))
	if err != nil {
		return GuardStatus{}, err
	}
	linked := 0
	st, err := e.Store.LoadState(ctx, c.ID)
	if err != nil {
		return GuardStatus{}, err
	}
	for _, u := range st.Users {
		if u.ConnectorID == c.ID && u.Enabled {
			linked++
		}
	}
	return GuardStatus{Disables: n, Limit: guardLimit(linked), Linked: linked}, nil
}

// Push plans and applies the given source users at once. It answers
// ErrDisabled for a disabled connector and ErrPaused while the guard holds
// it; a push that would take the disables of the rolling hour over the guard
// pauses the connector, records a run that needs confirmation and answers
// ErrPaused without applying anything.
func (e *Engine) Push(ctx context.Context, c Connector, users []SourceUser, o PushOptions) (PushResult, error) {
	if !c.Enabled {
		return PushResult{}, ErrDisabled
	}
	if c.PausedAt != nil {
		return PushResult{}, ErrPaused
	}
	st, err := e.Store.LoadState(ctx, c.ID)
	if err != nil {
		return PushResult{}, err
	}
	conn := c
	src := users
	if o.Delete {
		// A delete disables whatever the mapping reads: the users are read as
		// inactive by an active mapping made for the purpose.
		conn.Mappings.Active = ActiveMap{Attribute: "hello.deleted", Mode: ActiveNotBool}
		src = make([]SourceUser, len(users))
		for i, u := range users {
			attrs := make(map[string][]string, len(u.Attrs)+1)
			for k, v := range u.Attrs {
				attrs[k] = v
			}
			attrs["hello.deleted"] = []string{"true"}
			src[i] = SourceUser{ExternalID: u.ExternalID, Attrs: attrs, Groups: u.Groups}
		}
	}
	plan := MakePlan(PlanInput{Connector: conn, State: st, Result: &Result{Users: src, Incremental: true},
		Decisions: Decisions{AdoptAll: o.Adopt}, Incremental: true})
	now := e.now().UTC()
	if o.Strict {
		for _, ch := range plan.Changes {
			if refusal(ch) {
				refused := ch
				if err := e.Store.RecordPush(ctx, c.ID, []Change{ch}, now); err != nil {
					e.log().Warn("directory: record push", "connector", c.ID, "err", err)
				}
				e.Metrics.conflicts(c.Kind, 1)
				return PushResult{Changes: []Change{ch}, Refused: &refused}, nil
			}
		}
	}
	if plan.Disables > 0 {
		if err := e.guard(ctx, c, plan, now); err != nil {
			return PushResult{}, err
		}
	}
	actor := actorOf(c)
	rows := make([]Change, 0, len(plan.Changes))
	var applyErr error
	for _, ch := range plan.Changes {
		if ch.op == nil {
			rows = append(rows, ch)
			continue
		}
		if err := e.Store.Apply(ctx, actor, c, *ch.op); err != nil {
			rows = append(rows, Change{Action: ActionError, UserID: ch.UserID, ExternalID: ch.ExternalID, Username: ch.Username,
				FieldDiffs: ch.FieldDiffs, Reason: applyReason(err)})
			e.log().Warn("directory: apply push", "connector", c.ID, "reason", applyReason(err))
			if applyErr == nil {
				applyErr = err
			}
			continue
		}
		rows = append(rows, ch)
	}
	for _, q := range plan.Quiet {
		if o.Delete && q.Kind == "managed" {
			// The delete's own active mapping is not the connector's.
			continue
		}
		if err := e.Store.Apply(ctx, actor, c, q); err != nil {
			e.log().Warn("directory: apply quiet", "connector", c.ID, "err", applyReason(err))
		}
	}
	if len(rows) > 0 {
		if err := e.Store.RecordPush(ctx, c.ID, rows, now); err != nil {
			e.log().Warn("directory: record push", "connector", c.ID, "err", err)
		}
	}
	cn := map[string]int{}
	for _, r := range rows {
		cn[r.Action]++
	}
	e.Metrics.changes(RunSCIM, cn)
	e.Metrics.conflicts(c.Kind, cn[ActionConflict])
	if _, err := e.Store.RecordOutcome(ctx, c.ID, applyErr == nil, failCode(applyErr), failDetail(applyErr), e.now()); err != nil {
		e.log().Warn("directory: record outcome", "err", err)
	}
	res := PushResult{Changes: rows}
	if applyErr != nil && o.Strict {
		return res, applyErr
	}
	return res, nil
}

func failCode(err error) string {
	if err == nil {
		return ""
	}
	return applyReason(err)
}

func failDetail(err error) string {
	if err == nil {
		return ""
	}
	return "a pushed change could not be applied: " + applyReason(err)
}

// guard pauses the connector when the plan's disables take the window over
// the limit (I-25): the halted changes are kept on a run that needs
// confirmation, and nothing is applied.
func (e *Engine) guard(ctx context.Context, c Connector, plan Plan, now time.Time) error {
	n, err := e.Store.PushDisables(ctx, c.ID, e.guardSince(c))
	if err != nil {
		return err
	}
	limit := guardLimit(plan.Linked)
	if n+plan.Disables <= limit {
		return nil
	}
	if err := e.Store.SetPaused(ctx, c.ID, true, now); err != nil {
		return err
	}
	run, err := e.Store.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunSCIM, Status: StatusRunning, RequestedBy: actorOf(c),
		StartedAt: &now, Replica: e.Replica, Counts: map[string]int{}})
	if err != nil {
		return err
	}
	var halted []Change
	for _, ch := range plan.Changes {
		if ch.Action == ActionDisable {
			halted = append(halted, ch)
		}
	}
	if err := e.Store.ReplaceChanges(ctx, run.ID, halted); err != nil {
		return err
	}
	run.Status, run.FinishedAt = StatusNeedsConfirmation, &now
	run.Counts = map[string]int{ActionDisable: len(halted), "linked": plan.Linked, "recentDisables": n}
	run.ErrorCode = "needs_confirmation"
	run.ErrorDetail = "SCIM would disable " + strconv.Itoa(n+plan.Disables) + " of " + strconv.Itoa(plan.Linked) +
		" linked users within an hour (limit " + strconv.Itoa(limit) + "); SCIM writes are paused until an administrator confirms"
	if err := e.Store.UpdateRun(ctx, run); err != nil {
		return err
	}
	if err := e.Store.Audit(ctx, actorOf(c), "scim-paused", "directory_connector", strconv.FormatInt(c.ID, 10)); err != nil {
		e.log().Warn("directory: audit", "err", err)
	}
	e.Metrics.run(RunSCIM, "needs_confirmation", 0)
	e.log().Warn("directory: SCIM connector paused by the mass-deprovision guard", "connector", c.ID, "disables", n+plan.Disables, "limit", limit)
	return ErrPaused
}

// resumePush confirms a paused push connector's halted run: SCIM writes are
// taken again and the guard's window restarts. The refused requests are not
// replayed; the identity provider sends them again (Entra on its next cycle,
// Okta when its task is retried).
func (e *Engine) resumePush(ctx context.Context, actor string, run Run) (Run, error) {
	now := e.now().UTC()
	if err := e.Store.SetPaused(ctx, run.ConnectorID, false, now); err != nil {
		return run, err
	}
	run.Status, run.FinishedAt = StatusSucceeded, &now
	run.ErrorCode = ""
	run.ErrorDetail = "confirmed by " + actor + ": SCIM writes resumed; the identity provider sends the refused changes again"
	if len(run.ErrorDetail) > 300 {
		run.ErrorDetail = run.ErrorDetail[:300]
	}
	if err := e.Store.UpdateRun(ctx, run); err != nil {
		return run, err
	}
	if err := e.Store.Audit(ctx, actor, "scim-resume", "directory_connector", strconv.FormatInt(run.ConnectorID, 10)); err != nil {
		e.log().Warn("directory: audit", "err", err)
	}
	return run, nil
}
