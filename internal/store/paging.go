package store

// Server paging groups (phonebooks-ptt spec P-12). A group is dialled by its
// page code; hello-sip forks the call to every member with auto-answer
// headers. Every mutation runs through configChange, so it is audited, bumps
// the configuration revision and notifies hello-sip.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Paging limits (spec P-17).
const (
	MaxPagingGroups  = 100
	MaxPagingMembers = 200
)

// Paging priorities.
const (
	PagingNormal    = "normal"
	PagingEmergency = "emergency"
)

// PagingExtension names one extension in a paging group's member or caller
// list. Number and Name are read-only display values.
type PagingExtension struct {
	ExtensionID int64  `json:"extensionId"`
	Number      string `json:"number"`
	Name        string `json:"name"`
}

// PagingGroup is a paging group with its members (in position order) and the
// extensions allowed to page it (empty: any internal extension).
type PagingGroup struct {
	ID       int64             `json:"id"`
	Name     string            `json:"name"`
	Code     string            `json:"code"`
	Priority string            `json:"priority"`
	Enabled  bool              `json:"enabled"`
	Members  []PagingExtension `json:"members"`
	Callers  []PagingExtension `json:"callers"`
	// MulticastChannelID names a multicast channel of a phone group the page
	// is also sent to (migration 00016); MulticastChannel describes it.
	MulticastChannelID *int64           `json:"multicastChannelId"`
	MulticastChannel   *PagingMulticast `json:"multicastChannel"`
	CreatedAt          time.Time        `json:"createdAt"`
	UpdatedAt          time.Time        `json:"updatedAt"`
}

// PagingGroupInput is the writable shape of a paging group; an update
// replaces the whole group, member and caller lists included.
type PagingGroupInput struct {
	Name     string
	Code     string
	Priority string
	Enabled  bool
	Members  []int64 // extension ids, in display order
	Callers  []int64 // extension ids
	// MulticastChannelID is the multicast channel to page too, or nil.
	MulticastChannelID *int64
}

// PagingMulticast describes the multicast channel of a paging group
// (read-only).
type PagingMulticast struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"groupId"`
	GroupName string `json:"groupName"`
	Label     string `json:"label"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
	Codec     string `json:"codec"`
}

func listPagingGroups(ctx context.Context, q querier, where string, args ...any) ([]PagingGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.name, g.code, g.priority, g.enabled, g.created_at, g.updated_at,
			g.multicast_channel_id, c.id, c.group_id, pg.name, c.label, c.address, c.port, c.codec
		FROM paging_groups g LEFT JOIN multicast_channels c ON c.id = g.multicast_channel_id
		LEFT JOIN phone_groups pg ON pg.id = c.group_id `+where+` ORDER BY g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []PagingGroup{}
	var ids []int64
	for rows.Next() {
		var g PagingGroup
		var cid, cgroup sql.NullInt64
		var cgname, clabel, caddr, ccodec sql.NullString
		var cport sql.NullInt64
		if err := rows.Scan(&g.ID, &g.Name, &g.Code, &g.Priority, &g.Enabled, &g.CreatedAt, &g.UpdatedAt,
			&g.MulticastChannelID, &cid, &cgroup, &cgname, &clabel, &caddr, &cport, &ccodec); err != nil {
			return nil, err
		}
		if cid.Valid {
			g.MulticastChannel = &PagingMulticast{ID: cid.Int64, GroupID: cgroup.Int64, GroupName: cgname.String,
				Label: clabel.String, Address: caddr.String, Port: int(cport.Int64), Codec: ccodec.String}
		}
		g.Members, g.Callers = []PagingExtension{}, []PagingExtension{}
		out = append(out, g)
		ids = append(ids, g.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	byID := map[int64]*PagingGroup{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	const members = `SELECT m.group_id, m.extension_id, e.number, e.name FROM paging_group_members m
		JOIN extensions e ON e.id = m.extension_id WHERE m.group_id = ANY($1) ORDER BY m.group_id, m.position, e.number`
	const callers = `SELECT m.group_id, m.extension_id, e.number, e.name FROM paging_group_callers m
		JOIN extensions e ON e.id = m.extension_id WHERE m.group_id = ANY($1) ORDER BY m.group_id, e.number`
	for _, kind := range []struct {
		query string
		add   func(g *PagingGroup, pe PagingExtension)
	}{
		{members, func(g *PagingGroup, pe PagingExtension) { g.Members = append(g.Members, pe) }},
		{callers, func(g *PagingGroup, pe PagingExtension) { g.Callers = append(g.Callers, pe) }},
	} {
		mrows, err := q.QueryContext(ctx, kind.query, ids)
		if err != nil {
			return nil, err
		}
		for mrows.Next() {
			var gid int64
			var pe PagingExtension
			if err := mrows.Scan(&gid, &pe.ExtensionID, &pe.Number, &pe.Name); err != nil {
				_ = mrows.Close()
				return nil, err
			}
			kind.add(byID[gid], pe)
		}
		_ = mrows.Close()
		if err := mrows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListPagingGroups returns every paging group.
func (s *Store) ListPagingGroups(ctx context.Context) ([]PagingGroup, error) {
	return listPagingGroups(ctx, s.db, "")
}

// GetPagingGroup returns one paging group.
func (s *Store) GetPagingGroup(ctx context.Context, id int64) (PagingGroup, error) {
	gs, err := listPagingGroups(ctx, s.db, ` WHERE g.id = $1`, id)
	if err != nil {
		return PagingGroup{}, err
	}
	if len(gs) == 0 {
		return PagingGroup{}, ErrNotFound
	}
	return gs[0], nil
}

// codesCollide reports whether two feature or page codes would shadow one
// another: equal, or one a prefix of the other (a feature code takes its
// argument after the code, so *80 with *8059 is ambiguous).
func codesCollide(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// checkPageCode refuses a page code that collides with a feature code.
func checkPageCode(ctx context.Context, tx *sql.Tx, code string) error {
	rows, err := tx.QueryContext(ctx, `SELECT code FROM feature_codes`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var fc string
		if err := rows.Scan(&fc); err != nil {
			return err
		}
		if codesCollide(code, fc) {
			return fieldError("code", fmt.Sprintf("collides with feature code %s", fc))
		}
	}
	return rows.Err()
}

// checkPagingChannel refuses a multicast channel Hello cannot page: one that
// does not exist, that the phones do not listen on, or whose codec is not
// G.711 (Hello does not transcode to G.722).
func checkPagingChannel(ctx context.Context, tx *sql.Tx, id *int64) error {
	if id == nil {
		return nil
	}
	var role, codec string
	err := tx.QueryRowContext(ctx, `SELECT role, codec FROM multicast_channels WHERE id = $1`, *id).Scan(&role, &codec)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fieldError("multicastChannelId", "no such multicast channel")
	case err != nil:
		return err
	case role == "send":
		return fieldError("multicastChannelId", "the phones do not listen on this channel (its role is send)")
	case codec != "PCMU" && codec != "PCMA":
		return fieldError("multicastChannelId", "Hello pages G.711 only; the channel's codec is "+codec)
	}
	return nil
}

func writePagingLists(ctx context.Context, tx *sql.Tx, id int64, in PagingGroupInput) error {
	for _, q := range []string{
		`DELETE FROM paging_group_members WHERE group_id = $1`,
		`DELETE FROM paging_group_callers WHERE group_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	for i, ext := range in.Members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO paging_group_members (group_id, extension_id, position)
			VALUES ($1, $2, $3)`, id, ext, i+1); err != nil {
			return pagingFK(err, "members")
		}
	}
	for _, ext := range in.Callers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO paging_group_callers (group_id, extension_id)
			VALUES ($1, $2)`, id, ext); err != nil {
			return pagingFK(err, "callers")
		}
	}
	return nil
}

// pagingFK turns an unknown extension id into a field error.
func pagingFK(err error, field string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return fieldError(field, "names an extension that does not exist")
	}
	return err
}

// CreatePagingGroup inserts a paging group with its lists.
func (s *Store) CreatePagingGroup(ctx context.Context, actor string, in PagingGroupInput, check Check) (PagingGroup, error) {
	var g PagingGroup
	err := s.configChange(ctx, actor, "create", "paging_group", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM paging_groups`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxPagingGroups {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d paging groups", MaxPagingGroups)}
		}
		if err := checkPageCode(ctx, tx, in.Code); err != nil {
			return 0, err
		}
		if err := checkPagingChannel(ctx, tx, in.MulticastChannelID); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO paging_groups (name, code, priority, enabled, multicast_channel_id)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, in.Name, in.Code, in.Priority, in.Enabled, in.MulticastChannelID).Scan(&id); err != nil {
			return 0, err
		}
		if err := writePagingLists(ctx, tx, id, in); err != nil {
			return id, err
		}
		gs, err := listPagingGroups(ctx, tx, ` WHERE g.id = $1`, id)
		if err == nil && len(gs) == 1 {
			g = gs[0]
		}
		return id, err
	})
	return g, err
}

// UpdatePagingGroup replaces a paging group.
func (s *Store) UpdatePagingGroup(ctx context.Context, actor string, id int64, in PagingGroupInput, check Check) (PagingGroup, error) {
	var g PagingGroup
	err := s.configChange(ctx, actor, "update", "paging_group", check, func(tx *sql.Tx) (int64, error) {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT code FROM paging_groups WHERE id = $1`, id).Scan(&cur); err != nil {
			return id, err
		}
		if cur != in.Code {
			if err := refuseIfIVRPoints(ctx, tx, "page code "+cur+", which cannot be changed while it is named by", cur, IVRPaging); err != nil {
				return id, err
			}
		}
		if err := checkPageCode(ctx, tx, in.Code); err != nil {
			return id, err
		}
		if err := checkPagingChannel(ctx, tx, in.MulticastChannelID); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE paging_groups SET name = $2, code = $3, priority = $4, enabled = $5,
			multicast_channel_id = $6, updated_at = now() WHERE id = $1`, id, in.Name, in.Code, in.Priority, in.Enabled, in.MulticastChannelID)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if err := writePagingLists(ctx, tx, id, in); err != nil {
			return id, err
		}
		gs, err := listPagingGroups(ctx, tx, ` WHERE g.id = $1`, id)
		if err == nil && len(gs) == 1 {
			g = gs[0]
		}
		return id, err
	})
	return g, err
}

// DeletePagingGroup removes a paging group and, by cascade, its lists.
func (s *Store) DeletePagingGroup(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "paging_group", check, func(tx *sql.Tx) (int64, error) {
		var code string
		if err := tx.QueryRowContext(ctx, `SELECT code FROM paging_groups WHERE id = $1`, id).Scan(&code); err != nil {
			return id, err
		}
		if err := refuseIfIVRPoints(ctx, tx, "paging group "+code, code, IVRPaging); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM paging_groups WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// checkFeatureCodesAgainstPaging refuses a feature-code list whose codes
// shadow a paging group's page code.
func checkFeatureCodesAgainstPaging(ctx context.Context, tx *sql.Tx, codes []FeatureCode) error {
	rows, err := tx.QueryContext(ctx, `SELECT code FROM paging_groups`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pc string
		if err := rows.Scan(&pc); err != nil {
			return err
		}
		for _, c := range codes {
			if codesCollide(c.Code, pc) {
				return fieldError("items", fmt.Sprintf("feature code %s collides with the page code %s", c.Code, pc))
			}
		}
	}
	return rows.Err()
}
