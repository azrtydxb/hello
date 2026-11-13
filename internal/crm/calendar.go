package crm

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// Calendar DND (spec I-56): for users who connect Outlook or Google and turn
// the setting on, a busy or out-of-office stretch in their calendar sets DND
// on their extensions, and its end clears it. The poll runs every two minutes.
// A DND the user set by hand is never overridden and never cleared: the DND
// of an extension records who set it (extensions.dnd_source).
const (
	// CalendarInterval is the poll interval.
	CalendarInterval = 2 * time.Minute
	// calendarTimeout bounds one calendar read.
	calendarTimeout = 15 * time.Second
	// calendarGrace is how long past the end of the last busy stretch a
	// calendar that cannot be read keeps the DND on before it is cleared.
	calendarGrace = 5 * time.Minute
	calendarLock  = "hello:crm:calendar"
	calendarConc  = 8
)

// CalendarWorker polls the calendars of the users who asked for calendar DND.
type CalendarWorker struct {
	Repo       Repo
	Tokens     *TokenManager
	DND        DNDSetter
	Locker     events.Locker
	Interval   time.Duration
	Log        *slog.Logger
	Now        func() time.Time
	Concurrent int
}

func (w *CalendarWorker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *CalendarWorker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Run polls until ctx ends.
func (w *CalendarWorker) Run(ctx context.Context) {
	iv := w.Interval
	if iv <= 0 {
		iv = CalendarInterval
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		w.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reading is what one connection's calendar said.
type reading struct {
	row      ConnectionRow
	conn     ConnectorRow
	busy     Busy
	err      error
	needAuth bool
}

// Once polls every calendar once and applies the result. One replica at a
// time runs it.
func (w *CalendarWorker) Once(ctx context.Context) {
	if w.Locker != nil {
		unlock, ok, err := w.Locker.TryLock(ctx, calendarLock)
		if err != nil || !ok {
			return
		}
		defer unlock()
	}
	rows, err := w.Repo.ListConnectors(ctx)
	if err != nil {
		w.log().Warn("crm: calendar poll: listing connectors", "error", err)
		return
	}
	var jobs []reading
	for _, cr := range rows {
		conn, ok := w.Tokens.Providers(cr.Kind)
		if !ok || !cr.Enabled || !slices.Contains(Features(cr.Config), FeatureCalendar) {
			continue
		}
		if _, ok := conn.(Calendar); !ok {
			continue
		}
		conns, err := w.Repo.UserConnections(ctx, cr.ID)
		if err != nil {
			w.log().Warn("crm: calendar poll: listing connections", "connector", cr.ID, "error", err)
			continue
		}
		for _, c := range conns {
			if c.CalendarDND {
				jobs = append(jobs, reading{row: c, conn: cr})
			}
		}
	}
	if len(jobs) == 0 {
		return
	}
	now := w.now()
	n := w.Concurrent
	if n <= 0 {
		n = calendarConc
	}
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			w.read(ctx, &jobs[i], now)
		}()
	}
	wg.Wait()
	byUser := map[int64][]*reading{}
	for i := range jobs {
		byUser[jobs[i].row.UserID] = append(byUser[jobs[i].row.UserID], &jobs[i])
	}
	for uid, rs := range byUser {
		w.apply(ctx, uid, rs, now)
	}
}

// read asks one connection's calendar.
func (w *CalendarWorker) read(ctx context.Context, r *reading, now time.Time) {
	if r.row.Status != StatusConnected {
		r.err, r.needAuth = ErrNeedsReauth, true
		return
	}
	cctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()
	conn, cc, err := w.Tokens.ConnFor(cctx, r.conn, r.row.UserID)
	if err != nil {
		r.err, r.needAuth = err, errors.Is(err, ErrNeedsReauth)
		return
	}
	cal := conn.(Calendar)
	b, err := cal.Busy(cctx, cc, now)
	if errors.Is(err, ErrAuth) {
		var nc Conn
		if nc, err = w.Tokens.RefreshFor(cctx, r.conn, r.row.UserID); err == nil {
			b, err = cal.Busy(cctx, nc, now)
		} else {
			r.needAuth = errors.Is(err, ErrNeedsReauth)
		}
	}
	r.busy, r.err = b, err
	if errors.Is(err, ErrAuth) {
		w.Tokens.MarkNeedsReauth(ctx, r.conn, "the calendar refused the access token")
		r.needAuth = true
	}
}

// apply decides a user's DND from every calendar they connected, then
// records each connection's state.
func (w *CalendarWorker) apply(ctx context.Context, uid int64, rs []*reading, now time.Time) {
	anyBusy, anyActive, unreadable := false, false, false
	var until time.Time
	for _, r := range rs {
		anyActive = anyActive || r.row.CalendarActive
		switch {
		case r.err != nil:
			unreadable = true
		case r.busy.Busy:
			anyBusy = true
			if r.busy.Until.After(until) {
				until = r.busy.Until
			}
		}
	}
	active := anyActive
	switch {
	case anyBusy && !anyActive:
		changed, err := w.DND.SetCalendarDND(ctx, uid, true)
		if err != nil {
			w.log().Warn("crm: calendar DND on", "user", uid, "error", err)
			return
		}
		// changed is 0 when the user's own DND is on: the calendar took
		// nothing, so it must not clear anything at the end either.
		active = changed > 0
	case !anyBusy && anyActive:
		// An unreadable calendar keeps the DND until the last known end plus
		// a grace, so a network blip does not drop it mid-meeting and a dead
		// connection cannot hold it forever.
		stale := false
		for _, r := range rs {
			if r.row.CalendarActive && (r.row.CalendarBusyUntil.IsZero() || now.After(r.row.CalendarBusyUntil.Add(calendarGrace))) {
				stale = true
			}
		}
		if unreadable && !stale {
			break
		}
		if _, err := w.DND.SetCalendarDND(ctx, uid, false); err != nil {
			w.log().Warn("crm: calendar DND off", "user", uid, "error", err)
			return
		}
		active = false
	}
	for _, r := range rs {
		errText := ""
		switch {
		case r.needAuth:
			errText = "sign in again to keep calendar DND working"
		case r.err != nil:
			errText = "the calendar could not be read"
		}
		busyUntil := r.row.CalendarBusyUntil
		if r.err == nil {
			busyUntil = time.Time{}
			if r.busy.Busy {
				busyUntil = r.busy.Until
			}
		}
		act := active && (r.row.CalendarActive || r.busy.Busy || !anyActive)
		if !active {
			act, busyUntil = false, time.Time{}
			if r.err != nil {
				busyUntil = r.row.CalendarBusyUntil
			}
		}
		if err := w.Repo.SetCalendarState(ctx, r.row.ID, act, busyUntil, now, errText); err != nil {
			w.log().Warn("crm: calendar state", "connection", r.row.ID, "error", err)
		}
	}
}
