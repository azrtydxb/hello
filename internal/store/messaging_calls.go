package store

import (
	"context"
	"strings"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// cdrDigits is the SQL expression that reduces a CDR number column x to its
// digits: a SIP or tel URI loses its scheme and host, anything else loses
// its separators.
func cdrDigits(x string) string {
	return `regexp_replace(split_part(regexp_replace(` + x + `, '^(sips?|tel):', '', 'i'), '@', 1), '[^0-9]', '', 'g')`
}

// ConversationCalls returns the most recent calls to or from the number of
// conversation convID, newest first, at most limit. The conversation must be
// one the caller reaches (ErrNotFound otherwise). A supervisor (acc.All) sees
// every such call; an agent only the calls in which one of their own
// extensions took part, the rule of UserCDRs, so the panel never shows a call
// the agent could not find in their own history. A chat visitor has no number
// and so no calls.
func (s *Store) ConversationCalls(ctx context.Context, acc InboxAccess, convID int64, limit int) ([]CDR, error) {
	var peerKey, peerKind string
	err := s.db.QueryRowContext(ctx, `SELECT c.peer_key, c.peer_kind FROM conversations c WHERE c.id = $3 AND `+visible,
		acc.UserID, acc.All, convID).Scan(&peerKey, &peerKind)
	if err != nil {
		return nil, mapErr(err)
	}
	out := []CDR{}
	if peerKind != "phone" {
		return out, nil
	}
	key, ok := phonebook.Key(peerKey)
	if !ok {
		return out, nil
	}
	digits := strings.TrimPrefix(key, "+")
	// A number of nine digits or more matches by its last nine, as the
	// phonebook's fallback key does, so 0475 12 34 56 and +32475123456 are one
	// number; a shorter one must match whole.
	suffix, long := phonebook.Suffix(key)
	match := func(col string) string {
		if long {
			return `(length(` + cdrDigits(col) + `) >= 9 AND right(` + cdrDigits(col) + `, 9) = $1)`
		}
		return cdrDigits(col) + ` = $1`
	}
	arg := digits
	if long {
		arg = suffix
	}
	where := `(` + match("c.source") + ` OR ` + match("c.destination") + ` OR ` + match("c.original_destination") +
		` OR ` + match("c.rewritten_destination") + `)`
	args := []any{arg, limit}
	if !acc.All {
		where += ` AND EXISTS (
			SELECT 1 FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id
			WHERE ue.user_id = $3 AND (c.source = e.number OR c.destination = e.number OR c.original_destination = e.number))`
		args = append(args, acc.UserID)
	}
	//nolint:gosec // G202: the prefix, the digit expressions and the conditions are constants
	rows, err := s.db.QueryContext(ctx, cdrQuery+`FROM cdrs c WHERE `+where+` ORDER BY c.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		c, err := scanCDR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
