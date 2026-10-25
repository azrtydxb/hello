package main

// Automatic resync (spec S-5, HELLO_PROV_AUTO_RESYNC): when what a phone's
// configuration renders from changes (template, group, line, BLF keys, book
// assignment, firmware pin), queue a check-sync for it. A change must stay
// the same across two scans, which is the debounce, so a burst of edits
// pushes once; the push rate is enforced where pushes are sent.

import (
	"context"
	"log/slog"
	"time"
)

// autoSyncEvery is the scan interval and so the debounce.
var autoSyncEvery = 30 * time.Second

// autoSyncStore is the persistence the scan needs; *store.Store satisfies it.
type autoSyncStore interface {
	AutoSyncPhones(ctx context.Context) (map[int64]string, error)
	PhoneSyncHash(ctx context.Context, phoneID int64) (string, error)
	AutoResync(ctx context.Context, phoneID int64, old, hash string) (bool, error)
}

// autoSync scans until ctx ends.
func autoSync(ctx context.Context, st autoSyncStore, log *slog.Logger) {
	seen := map[int64]string{}
	t := time.NewTicker(autoSyncEvery)
	defer t.Stop()
	for {
		autoSyncScan(ctx, st, seen, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// autoSyncScan runs one scan. seen holds, per phone, the changed hash the
// previous scan observed.
func autoSyncScan(ctx context.Context, st autoSyncStore, seen map[int64]string, log *slog.Logger) {
	stored, err := st.AutoSyncPhones(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("auto resync: list phones", "error", err)
		}
		return
	}
	for id := range seen {
		if _, ok := stored[id]; !ok {
			delete(seen, id)
		}
	}
	for id, old := range stored {
		if ctx.Err() != nil {
			return
		}
		h, err := st.PhoneSyncHash(ctx, id)
		if err != nil {
			continue // not renderable now (sealed value, no template): nothing to push
		}
		switch {
		case h == old:
			delete(seen, id)
		case old == "" || seen[id] == h:
			// First sight is adopted without a push; a change that held
			// since the last scan is pushed.
			queued, err := st.AutoResync(ctx, id, old, h)
			if err != nil {
				log.Warn("auto resync", "phone", id, "error", err)
				continue
			}
			delete(seen, id)
			if queued {
				log.Info("auto resync queued", "phone", id)
			}
		default:
			seen[id] = h
		}
	}
}
