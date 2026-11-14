package store

// Desk-phone texting and contact naming for the messaging inbox (spec
// messaging-channels M-15 and M-26): a conversation assigned to a user is
// delivered to the phones of their extension as a SIP MESSAGE, a reply typed
// on the phone goes back into the conversation, and a customer's number is
// named from the address books of whoever reads it.
//
// Customer text and numbers are never logged and never put in an audit row.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// dbQuery is what *sql.DB and *sql.Tx share.
type dbQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Name sources a contact lookup reports.
const (
	NameSourceExtensions = "extensions"
	NameSourcePersonal   = "personal"
	NameSourceNamed      = "named"
	NameSourceCompany    = "company"
)

type nameBook struct {
	id     int64
	source string
}

// nameBooks lists the books that may name a peer for a reader, in lookup
// order (spec M-15): the extensions, the reader's own personal book, the
// named books of the groups of their extension's phones, the company book.
// A reader of 0 reads the shared books only.
func nameBooks(ctx context.Context, q dbQuery, userID int64) ([]nameBook, error) {
	var out []nameBook
	add := func(query, source string, args ...any) error {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		seen := map[int64]bool{}
		for _, b := range out {
			seen[b.id] = true
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, nameBook{id, source})
			}
		}
		return rows.Err()
	}
	if err := add(`SELECT id FROM phonebooks WHERE kind = 'extensions'`, NameSourceExtensions); err != nil {
		return nil, err
	}
	if userID > 0 {
		if err := add(`SELECT id FROM phonebooks WHERE kind = 'personal' AND owner_user_id = $1`, NameSourcePersonal, userID); err != nil {
			return nil, err
		}
		if err := add(`
			SELECT b.id FROM users u
			JOIN devices d ON d.extension_id = u.extension_id
			JOIN phones p ON p.device_id = d.id
			JOIN phone_group_books gb ON gb.group_id = p.group_id
			JOIN phonebooks b ON b.id = gb.book_id AND b.kind = 'named'
			WHERE u.id = $1
			GROUP BY p.group_id, gb.position, b.id ORDER BY p.group_id, gb.position`, NameSourceNamed, userID); err != nil {
			return nil, err
		}
	}
	if err := add(`SELECT id FROM phonebooks WHERE kind = 'company'`, NameSourceCompany); err != nil {
		return nil, err
	}
	return out, nil
}

// resolveContactName names the peer for the reader: an exact match on the
// lookup key in book order, else the one entry whose number ends in the same
// nine digits. found is false when no book names it.
func resolveContactName(ctx context.Context, q dbQuery, userID int64, peerKey string) (name, source string, err error) {
	if peerKey == "" {
		return "", "", nil
	}
	books, err := nameBooks(ctx, q, userID)
	if err != nil {
		return "", "", err
	}
	for _, b := range books {
		var in phonebook.EntryInput
		if b.source == NameSourceExtensions {
			err = q.QueryRowContext(ctx, `SELECT name FROM extensions WHERE number = $1 AND NOT directory_hidden`, peerKey).Scan(&in.LastName)
		} else {
			err = q.QueryRowContext(ctx, `
				SELECT e.first_name, e.last_name, e.company
				FROM phonebook_entries e, jsonb_array_elements(e.numbers) n
				WHERE e.book_id = $1 AND n->>'key' = $2 ORDER BY e.id LIMIT 1`, b.id, peerKey).Scan(&in.FirstName, &in.LastName, &in.Company)
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return "", "", err
		}
		if n := strings.TrimSpace(in.DisplayName()); n != "" {
			return n, b.source, nil
		}
	}
	suffix, ok := phonebook.Suffix(peerKey)
	if !ok {
		return "", "", nil
	}
	var ids []int64
	for _, b := range books {
		if b.source != NameSourceExtensions {
			ids = append(ids, b.id)
		}
	}
	if len(ids) == 0 {
		return "", "", nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT DISTINCT e.id, e.book_id, e.first_name, e.last_name, e.company
		FROM phonebook_entries e, jsonb_array_elements(e.numbers) n
		WHERE e.book_id = ANY($1::bigint[]) AND length(ltrim(n->>'key', '+')) >= 9 AND right(ltrim(n->>'key', '+'), 9) = $2
		ORDER BY e.id LIMIT 2`, int64Array(ids), suffix)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rows.Close() }()
	type hit struct {
		book int64
		name string
	}
	var hits []hit
	for rows.Next() {
		var id, book int64
		var in phonebook.EntryInput
		if err := rows.Scan(&id, &book, &in.FirstName, &in.LastName, &in.Company); err != nil {
			return "", "", err
		}
		hits = append(hits, hit{book, strings.TrimSpace(in.DisplayName())})
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if len(hits) != 1 || hits[0].name == "" {
		return "", "", nil // none, or ambiguous: no name
	}
	for _, b := range books {
		if b.id == hits[0].book {
			return hits[0].name, b.source, nil
		}
	}
	return "", "", nil
}

// int64Array is a bigint[] argument.
func int64Array(v []int64) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// ContactName names a customer's number for one reader (spec M-15). A
// personal contact names it for its owner only.
func (s *Store) ContactName(ctx context.Context, userID int64, peerKey string) (name, source string, err error) {
	return resolveContactName(ctx, s.db, userID, peerKey)
}

// nameConversations replaces the stored peer name by what the reader's own
// address books call the number, when they call it anything. The name is
// computed at read time and never stored from a personal book.
func (s *Store) nameConversations(ctx context.Context, userID int64, cs []Conversation) error {
	type named struct{ name, source string }
	cache := map[string]named{}
	for i := range cs {
		n, ok := cache[cs[i].Peer]
		if !ok {
			name, source, err := resolveContactName(ctx, s.db, userID, cs[i].Peer)
			if err != nil {
				return err
			}
			n = named{name, source}
			cache[cs[i].Peer] = n
		}
		if n.name != "" {
			cs[i].PeerName, cs[i].PeerNameSource = n.name, n.source
		}
	}
	return nil
}

// UserPhoneVendors lists the vendors of the phones of the user's extension,
// for the capability check of the profile (spec M-26).
func (s *Store) UserPhoneVendors(ctx context.Context, userID int64) ([]prov.Vendor, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT p.vendor FROM phones p
		JOIN devices d ON d.id = p.device_id
		JOIN users u ON u.extension_id = d.extension_id
		WHERE u.id = $1 ORDER BY 1`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []prov.Vendor
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, prov.Vendor(v))
	}
	return out, rows.Err()
}

// deliverToDeskPhone queues the customer's message cm of a conversation as a
// SIP MESSAGE to the phones of the user's extension, in the caller's
// transaction, when the number and the user both have desk-phone delivery on
// (spec M-26). It never fails the caller for a reason that only concerns the
// phone: text messaging off, a door station, a rate limit or a user without
// an extension just mean nothing is queued. A message already queued for the
// extension is not queued again, so a retry or a second assignment is quiet.
func (s *Store) deliverToDeskPhone(ctx context.Context, tx *sql.Tx, convID, cm, userID int64) error {
	var (
		chKind, peerKey, peerName, body, ext, extKind string
		addrOn, userOn                                bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT ch.kind, c.peer_key, c.peer_name, m.body, e.number, e.kind, a.desk_phone_delivery, u.deskphone_delivery
		FROM conversations c
		JOIN msg_channels ch ON ch.id = c.channel_id
		JOIN msg_addresses a ON a.id = c.address_id
		JOIN conversation_messages m ON m.id = $2 AND m.conversation_id = c.id AND m.kind = 'message' AND m.direction = 'in'
		JOIN users u ON u.id = $3 AND u.enabled
		JOIN extensions e ON e.id = u.extension_id
		WHERE c.id = $1`, convID, cm, userID).Scan(&chKind, &peerKey, &peerName, &body, &ext, &extKind, &addrOn, &userOn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !addrOn || !userOn || extKind == ExtensionDoor || strings.TrimSpace(body) == "" {
		return nil
	}
	set, err := getMessageSettings(ctx, tx)
	if err != nil {
		return err
	}
	if !set.Enabled {
		return nil
	}
	var queued bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE conversation_message_id = $1 AND recipient = $2
		AND recipient_kind = 'extension')`, cm, ext).Scan(&queued); err != nil {
		return err
	}
	if queued {
		return nil
	}
	name, _, err := resolveContactName(ctx, tx, userID, peerKey)
	if err != nil {
		return err
	}
	if name == "" {
		name = peerName
	}
	for _, part := range messaging.DeskPhoneParts(chKind, body) {
		text, raw := splitBody(messageDefaultCType, []byte(part))
		var sent MessageSent
		err := s.sendMessageTx(ctx, tx, MessageInput{
			Sender: peerKey, SenderKind: MsgKindExternal, SenderName: name,
			TargetKind: MsgKindExtension, Target: ext, ContentType: messageDefaultCType, Body: []byte(part),
			ConversationMessageID: cm,
		}, text, raw, &sent)
		switch {
		case err == nil:
		case errors.Is(err, ErrMessageRate), errors.Is(err, ErrMessagesDisabled), errors.Is(err, ErrMessageTarget),
			errors.Is(err, ErrMessageForbidden):
			return nil // the phone is not reachable now; the inbox has the text
		default:
			return err
		}
	}
	return nil
}

// deliverLastToDeskPhone hands the customer's latest message of a conversation
// to the phones of the user it was just assigned to.
func (s *Store) deliverLastToDeskPhone(ctx context.Context, tx *sql.Tx, convID, userID int64) error {
	var cm int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id = $1
		AND direction = 'in' AND kind = 'message' ORDER BY id DESC LIMIT 1`, convID).Scan(&cm)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.deliverToDeskPhone(ctx, tx, convID, cm, userID)
}

// DeviceReply is the outcome of a reply typed on a desk phone.
type DeviceReply struct {
	// Handled is false when the number is not a customer of the inbox: the
	// caller carries on with the ordinary external-number path.
	Handled bool
	// Refusal is why a handled reply was not sent, in words the phone can
	// show; Limited marks a rate limit (answered 429 rather than 403).
	Refusal string
	Limited bool
	// ConversationID is the conversation the reply went into.
	ConversationID int64
}

// ReplyFromDevice sends a text an extension's phone typed to a customer
// number into the inbox (spec M-26). In order: the most recently active open
// conversation assigned to the extension's user with that peer; else an
// existing conversation with that peer on a number the extension owns
// (unassigned or assigned to the user). A peer in another agent's open
// conversation is refused, never texted around the inbox, and a peer with no
// conversation is not the inbox's: Handled=false, so first contact is never
// made from a phone. Opt-out, the channel and the per-minute limit apply as
// for a reply from the web.
func (s *Store) ReplyFromDevice(ctx context.Context, ext, peerKey, body string) (DeviceReply, error) {
	var out DeviceReply
	var (
		userID, extID int64
		name          string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, e.id, coalesce(NULLIF(u.display_name, ''), u.username)
		FROM extensions e JOIN users u ON u.enabled AND u.role IN ('user', 'operator', 'admin')
			AND (u.extension_id = e.id OR EXISTS (SELECT 1 FROM user_extensions ue WHERE ue.user_id = u.id AND ue.extension_id = e.id))
		WHERE e.number = $1 ORDER BY (u.extension_id = e.id) DESC, u.id LIMIT 1`, ext).Scan(&userID, &extID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var convID int64
	err = s.db.QueryRowContext(ctx, `
		SELECT c.id FROM conversations c
		JOIN msg_channels ch ON ch.id = c.channel_id AND ch.provider = 'sip_trunk'
		JOIN msg_addresses a ON a.id = c.address_id
		WHERE c.peer_key = $1 AND c.status <> 'spam' AND (
			(c.assignee_user_id = $2 AND c.status <> 'resolved')
			OR (a.owner_extension_id = $3 AND (c.assignee_user_id IS NULL OR c.assignee_user_id = $2)))
		ORDER BY coalesce(c.assignee_user_id = $2, FALSE) DESC, (c.status <> 'resolved') DESC, c.last_message_at DESC, c.id DESC LIMIT 1`,
		peerKey, userID, extID).Scan(&convID)
	if errors.Is(err, sql.ErrNoRows) {
		var other bool
		if err := s.db.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id AND ch.provider = 'sip_trunk'
				WHERE c.peer_key = $1 AND c.status NOT IN ('resolved', 'spam'))`, peerKey).Scan(&other); err != nil {
			return out, err
		}
		if other {
			out.Handled, out.Refusal = true, "That customer is in another agent's conversation. Ask them to take over or reassign it."
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Handled, out.ConversationID = true, convID
	text, ok := messaging.CleanBody(body, messaging.MaxBodyBytes)
	if !ok {
		out.Refusal = "The text could not be sent: it is empty or not plain text."
		return out, nil
	}
	set, err := getMessageSettings(ctx, s.db)
	if err != nil {
		return out, err
	}
	var recent int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE sender_user_id = $1 AND direction = 'out'
		AND kind = 'message' AND created_at > now() - interval '1 minute'`, userID).Scan(&recent); err != nil {
		return out, err
	}
	if recent >= set.RatePerMinute {
		out.Refusal, out.Limited = "You are texting too fast. Try again in a minute.", true
		return out, nil
	}
	// The authority checks are above: this phone's user is the sender, and the
	// conversation is theirs or on a number their extension owns.
	acc := InboxAccess{UserID: userID, All: true, Actor: "extension:" + ext, Name: name}
	_, _, err = s.SendConversationText(ctx, acc, SendInput{ConversationID: convID, Body: text})
	switch {
	case err == nil:
	case errors.Is(err, ErrOptedOut):
		out.Refusal = "The customer opted out of this number. Nobody can text them until they reply START."
	case errors.Is(err, ErrChannelUnavailable):
		out.Refusal = "The number or its channel is off, so the text was not sent."
	case errors.Is(err, ErrConversationState):
		out.Refusal = "That conversation is closed as spam."
	case errors.Is(err, ErrMessagesDisabled), errors.Is(err, ErrNoExternalTrunk):
		out.Refusal = "Text messaging is turned off."
	case errors.Is(err, ErrMessageRate):
		out.Refusal, out.Limited = "You are texting too fast. Try again in a minute.", true
	default:
		return out, err
	}
	return out, nil
}

// AddConversationContact saves the conversation's peer as a contact and
// returns the conversation with its refreshed name (spec M-15). book is
// "personal" (the caller's own book) or "company" (supervisors only, since
// the company book is shared). The entry's numbers are the peer's number.
func (s *Store) AddConversationContact(ctx context.Context, acc InboxAccess, convID int64, book string, in phonebook.EntryInput) (Conversation, error) {
	c, err := s.GetConversation(ctx, acc, convID)
	if err != nil {
		return Conversation{}, err
	}
	switch book {
	case NameSourcePersonal:
		if _, err := s.CreateContact(ctx, acc.Actor, acc.UserID, in); err != nil {
			return Conversation{}, err
		}
	case NameSourceCompany:
		if !acc.All {
			return Conversation{}, ErrConversationState
		}
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM phonebooks WHERE kind = 'company'`).Scan(&id); err != nil {
			return Conversation{}, mapErr(err)
		}
		if _, err := s.CreatePhonebookEntry(ctx, acc.Actor, id, in); err != nil {
			return Conversation{}, err
		}
		// A shared name is kept on the conversations of the number that have
		// no better one, so every reader and the desk phones see it.
		if _, err := s.db.ExecContext(ctx, `UPDATE conversations SET peer_name = $2, peer_name_source = 'company'
			WHERE peer_key = $1 AND (peer_name = '' OR peer_name_source IN ('provider', 'extensions', 'company'))`,
			c.Peer, strings.TrimSpace(in.DisplayName())); err != nil {
			return Conversation{}, err
		}
	default:
		return Conversation{}, fieldError("book", "must be personal or company")
	}
	return s.conversationByID(ctx, acc.UserID, acc.All, convID)
}
