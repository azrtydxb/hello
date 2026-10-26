package store

// The dial space: every string a caller can dial that Hello gives a meaning
// besides an extension number. Feature codes, server paging codes and park
// slots must not overlap one another, because a phone dials them as one
// string and the first match wins:
//
//   - a feature code takes its argument after the code (*72599), so two codes
//     collide when one is a prefix of the other (*8 and *80);
//   - a paging code is dialled as is, but shares the prefix rule so a paging
//     group never shadows, or is shadowed by, a feature code;
//   - a park slot is a number (701) dialled to retrieve the call or watched by
//     a BLF key, so it collides with an extension number, a ring group name,
//     an IVR menu number, a conference room number, a queue number or another
//     lot's slot of the same value.
//
// Every write that can introduce an overlap runs dialGuard, which compares
// the overlaps before and after the change and refuses only the new ones, so
// a configuration that already overlaps does not block unrelated changes.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// dialItem is one claimed piece of the dial space.
type dialItem struct {
	kind string // feature, paging or park
	key  string // what it is called in a message
	code string // the dialled string, or the first slot for a park lot
	lo   int    // park lot: first slot
	hi   int    // park lot: last slot
}

// loadDialItems reads every claim on the dial space inside tx.
func loadDialItems(ctx context.Context, tx *sql.Tx) ([]dialItem, map[string]string, error) {
	var items []dialItem
	rows, err := tx.QueryContext(ctx, `SELECT code, action FROM feature_codes`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var code, action string
		if err := rows.Scan(&code, &action); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		items = append(items, dialItem{kind: "feature code", key: code + " (" + action + ")", code: code})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT code, name FROM paging_groups`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		items = append(items, dialItem{kind: "paging code", key: code + " (paging group " + name + ")", code: code})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT name, slot_start, slot_end FROM park_lots`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var name string
		var lo, hi int
		if err := rows.Scan(&name, &lo, &hi); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		items = append(items, dialItem{kind: "park slots", key: fmt.Sprintf("%d-%d (park lot %s)", lo, hi, name), lo: lo, hi: hi})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// Extension numbers, ring group names, IVR menu numbers, conference room
	// numbers and queue numbers a slot must not equal. The queues table comes with its own migration, which may not
	// be applied yet.
	var hasQueues bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('queues') IS NOT NULL`).Scan(&hasQueues); err != nil {
		return nil, nil, err
	}
	q := `SELECT number, 'extension ' || number FROM extensions
		UNION ALL SELECT name, 'ring group ' || name FROM ring_groups
		UNION ALL SELECT number, 'IVR menu ' || name FROM ivr_menus WHERE number <> ''
		UNION ALL SELECT number, 'conference room ' || name FROM conference_rooms`
	if hasQueues {
		q += ` UNION ALL SELECT number, 'queue ' || name FROM queues WHERE number <> ''`
	}
	names := map[string]string{}
	rows, err = tx.QueryContext(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var n, what string
		if err := rows.Scan(&n, &what); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		names[n] = what
	}
	_ = rows.Close()
	return items, names, rows.Err()
}

// dialConflicts lists the overlaps as "a | b" strings, each pair once.
func dialConflicts(items []dialItem, names map[string]string) map[string]string {
	out := map[string]string{}
	pair := func(a, b string) {
		if a > b {
			a, b = b, a
		}
		out[a+" | "+b] = a + " overlaps " + b
	}
	for i, a := range items {
		for _, b := range items[i+1:] {
			switch {
			case a.kind != "park slots" && b.kind != "park slots":
				if codesCollide(a.code, b.code) {
					pair(a.kind+" "+a.key, b.kind+" "+b.key)
				}
			case a.kind == "park slots" && b.kind == "park slots":
				if a.lo <= b.hi && b.lo <= a.hi {
					pair(a.kind+" "+a.key, b.kind+" "+b.key)
				}
			}
		}
		if a.kind == "park slots" {
			for n := a.lo; n <= a.hi; n++ {
				if what, ok := names[strconv.Itoa(n)]; ok {
					pair(a.kind+" "+a.key, what)
				}
			}
		}
	}
	return out
}

// dialGuard runs change and refuses it, naming field, when it introduced an
// overlap in the dial space.
func dialGuard(ctx context.Context, tx *sql.Tx, field string, change func() error) error {
	items, names, err := loadDialItems(ctx, tx)
	if err != nil {
		return err
	}
	before := dialConflicts(items, names)
	if err := change(); err != nil {
		return err
	}
	items, names, err = loadDialItems(ctx, tx)
	if err != nil {
		return err
	}
	var fresh []string
	for k, msg := range dialConflicts(items, names) {
		if _, old := before[k]; !old {
			fresh = append(fresh, msg)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.Strings(fresh)
	return fieldError(field, strings.Join(fresh, "; "))
}

// Kinds of object that answer to a plain dialled number; the own argument of
// checkDialNumber.
const (
	numExtension  = "extension"
	numRingGroup  = "ring group"
	numIVRMenu    = "IVR menu"
	numConference = "conference room"
	numQueue      = "queue"
)

// checkDialNumber refuses number (field names it in the error) when another
// object already answers to it: an extension number, ring group name, IVR menu
// number, conference room number or queue number, or a slot of a park lot.
// own is the kind of the object being written (one of the num* constants), so
// its own table is skipped; a duplicate within a kind is the table's unique
// index to refuse. Every create and every renumber of those objects runs this
// one check, so the refusal is symmetric between all of them.
func checkDialNumber(ctx context.Context, tx *sql.Tx, field, number, own string) error {
	if number == "" {
		return nil
	}
	var what string
	err := tx.QueryRowContext(ctx, `SELECT what FROM (
			SELECT 'extension' AS kind, 'an extension number' AS what, number AS n FROM extensions
			UNION ALL SELECT 'ring group', 'a ring group name', name FROM ring_groups
			UNION ALL SELECT 'IVR menu', 'the number of IVR menu ' || name, number FROM ivr_menus
			UNION ALL SELECT 'conference room', 'the number of conference room ' || name, number FROM conference_rooms
			UNION ALL SELECT 'queue', 'the number of queue ' || name, number FROM queues
		) t WHERE n = $1 AND kind <> $2 LIMIT 1`, number, own).Scan(&what)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return checkNotSlot(ctx, tx, field, number)
	case err != nil:
		return err
	}
	return fieldError(field, "is already "+what)
}
