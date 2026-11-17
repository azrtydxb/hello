package store

// The inbox's side of the integrations event pipeline (spec
// messaging-channels M-23): what an inbox event carries when it leaves for
// webhooks, automation triggers and the event stream, and the customer
// number and message text a webhook endpoint may opt into at delivery.

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/messaging"
)

// peerEventPurpose keys the peerKey of an event: a hash of the customer's
// number derived from HELLO_SECRET_KEY, separate from the opt-out hash, so
// the two cannot be joined.
const peerEventPurpose = "messaging-event-peer"

// PeerEventKey is the keyed hash an event carries instead of the customer's
// number: stable for one customer on one deployment, meaningless elsewhere.
func (s *Store) PeerEventKey(peerKey string) string {
	if s.box == nil || peerKey == "" {
		return ""
	}
	return "pk_" + hex.EncodeToString(s.box.MAC(peerEventPurpose, peerKey)[:16])
}

// InboxEventData reads what an inbox event carries: the conversation's
// channel, number, queue, assignee and hashed customer, and for a message
// event the message's direction and delivery state. found is false when the
// conversation no longer exists (erased or purged a moment ago); the event
// then carries only what e had.
func (s *Store) InboxEventData(ctx context.Context, e messaging.Event) (d events.InboxData, found bool, err error) {
	d = events.InboxData{ConversationID: e.ConversationID, MessageID: e.MessageID, ChannelKind: e.ChannelKind,
		Direction: e.Direction, Status: e.Status, AssigneeUserID: e.AssigneeUserID}
	var (
		convStatus, peerKey string
		assignee, queue     sql.NullInt64
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT c.channel_id, ch.kind, ch.provider, c.address_id, a.address, c.status, c.assignee_user_id, c.queue_id,
		       c.peer_kind, c.peer_key
		FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id JOIN msg_addresses a ON a.id = c.address_id
		WHERE c.id = @id`, momo.Named("id", e.ConversationID)).Scan(&d.ChannelID, &d.ChannelKind, &d.Provider, &d.AddressID, &d.Address,
		&convStatus, &assignee, &queue, &d.PeerKind, &peerKey)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.PeerKey = s.PeerEventKey(peerKey)
	d.QueueID = queue.Int64
	// An assignment event says who it went to at that moment; any other
	// event says who has it now.
	if e.Type != events.ConversationAssigned {
		d.AssigneeUserID = assignee.Int64
	}
	if e.MessageID == 0 {
		if d.Status == "" {
			d.Status = convStatus
		}
		return d, true, nil
	}
	var dir, status, code string
	err = s.db.QueryRowContext(ctx, `SELECT direction, status, error_code FROM conversation_messages WHERE id = @id AND conversation_id = @conversation_id`,
		momo.Named("id", e.MessageID), momo.Named("conversation_id", e.ConversationID)).Scan(&dir, &status, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return d, true, nil
	}
	if err != nil {
		return d, true, err
	}
	d.Direction = dir
	if d.Status == "" || e.Type == events.MessageStatus {
		d.Status = status
	}
	if status == messaging.DeliveryFailed {
		d.ErrorCode = code
	}
	return d, true, nil
}

// InboxEventContent reads the customer's number and a message's text for a
// webhook endpoint that asked for them (includePeer, includeMessageBody).
// It reads them at delivery, so an erased conversation or a redacted
// message gives nothing: the text is never kept with the event. Only a
// customer-facing message has text to give; an internal note never does.
func (s *Store) InboxEventContent(ctx context.Context, conversationID, messageID int64, peer, body bool) (peerOut, bodyOut string, err error) {
	if peer {
		err = s.db.QueryRowContext(ctx, `SELECT peer_key FROM conversations WHERE id = @id`, momo.Named("id", conversationID)).Scan(&peerOut)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
	}
	if body && messageID != 0 {
		err = s.db.QueryRowContext(ctx, `
			SELECT body FROM conversation_messages
			WHERE id = @id AND conversation_id = @conversation_id AND kind = 'message' AND redacted_at IS NULL`,
			momo.Named("id", messageID), momo.Named("conversation_id", conversationID)).Scan(&bodyOut)

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
	}
	return peerOut, bodyOut, nil
}
