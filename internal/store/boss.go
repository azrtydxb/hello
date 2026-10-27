package store

// Boss and secretary links (pbx-gaps S-18, S-19). A boss has at most one
// assistant and an assistant may serve many bosses. The list is replaced as a
// whole, like the feature codes, and every change runs through configChange,
// so it is audited (ids only), bumps the configuration revision and notifies
// hello-sip.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// MaxBossLinks bounds the link list.
const MaxBossLinks = 500

// ActionBossDivert is the feature-code action of *64 (migration 00050).
const ActionBossDivert = "boss_divert"

// Ring modes of a boss link.
const (
	BossRingBoth           = "both"
	BossRingAssistantFirst = "assistant_first"
)

// BossLink is one boss with their assistant. The numbers and names are for
// display.
type BossLink struct {
	ID                   int64     `json:"id"`
	BossExtensionID      int64     `json:"bossExtensionId"`
	BossNumber           string    `json:"bossNumber"`
	BossName             string    `json:"bossName"`
	AssistantExtensionID int64     `json:"assistantExtensionId"`
	AssistantNumber      string    `json:"assistantNumber"`
	AssistantName        string    `json:"assistantName"`
	RingMode             string    `json:"ringMode"`
	Screening            bool      `json:"screening"`
	Enabled              bool      `json:"enabled"`
	DivertOn             bool      `json:"divertOn"`
	OutboundAsBoss       bool      `json:"outboundAsBoss"`
	CreatedAt, UpdatedAt time.Time `json:"-"`
}

// BossLinkInput is the writable shape of a link.
type BossLinkInput struct {
	BossExtensionID      int64
	AssistantExtensionID int64
	RingMode             string
	Screening            bool
	Enabled              bool
	DivertOn             bool
	OutboundAsBoss       bool
}

func listBossLinks(ctx context.Context, q querier) ([]BossLink, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.id, l.boss_extension_id, b.number, b.name,
			l.assistant_extension_id, a.number, a.name, l.ring_mode, l.screening, l.enabled, l.divert_on, l.outbound_as_boss,
			l.created_at, l.updated_at
		FROM boss_links l JOIN extensions b ON b.id = l.boss_extension_id
		JOIN extensions a ON a.id = l.assistant_extension_id ORDER BY b.number, l.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []BossLink{}
	for rows.Next() {
		var l BossLink
		if err := rows.Scan(&l.ID, &l.BossExtensionID, &l.BossNumber, &l.BossName,
			&l.AssistantExtensionID, &l.AssistantNumber, &l.AssistantName, &l.RingMode, &l.Screening, &l.Enabled,
			&l.DivertOn, &l.OutboundAsBoss, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListBossLinks returns every link ordered by the boss's number.
func (s *Store) ListBossLinks(ctx context.Context) ([]BossLink, error) {
	return listBossLinks(ctx, s.db)
}

// PutBossLinks replaces the link list: every given link is upserted by its
// boss and every link whose boss is not in the list is removed. The caller
// sends the complete configuration and has checked that a boss appears once.
func (s *Store) PutBossLinks(ctx context.Context, actor string, in []BossLinkInput) error {
	if len(in) > MaxBossLinks {
		return &InUseError{Msg: fmt.Sprintf("at most %d boss links", MaxBossLinks)}
	}
	return s.configChangeID(ctx, actor, "update", "boss_link", nil, func(tx *sql.Tx) (string, error) {
		kept := make([]int64, len(in))
		for i, l := range in {
			kept[i] = l.BossExtensionID
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO boss_links (boss_extension_id, assistant_extension_id, ring_mode, screening, enabled, divert_on, outbound_as_boss)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (boss_extension_id) DO UPDATE SET
					assistant_extension_id = EXCLUDED.assistant_extension_id, ring_mode = EXCLUDED.ring_mode,
					screening = EXCLUDED.screening, enabled = EXCLUDED.enabled, divert_on = EXCLUDED.divert_on,
					outbound_as_boss = EXCLUDED.outbound_as_boss, updated_at = now()`,
				l.BossExtensionID, l.AssistantExtensionID, l.RingMode, l.Screening, l.Enabled, l.DivertOn, l.OutboundAsBoss); err != nil {
				return "", pagingFK(err, fmt.Sprintf("items[%d]", i))
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM boss_links WHERE boss_extension_id <> ALL($1)`, kept); err != nil {
			return "", err
		}
		return "list", nil
	})
}
