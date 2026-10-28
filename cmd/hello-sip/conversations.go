package main

// The messaging inbox's gateway (spec messaging-channels M-4): a text a SIP
// trunk delivers to one of the inbox's numbers is stored as a conversation
// message instead of going to an extension. It runs on the MESSAGE path, which
// may use PostgreSQL; the call path never does. Text is never logged.

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/store"
)

// maxInboundTextBytes is the longest trunk text the inbox keeps.
const maxInboundTextBytes = 2000

// conversations implements sip.ConversationGateway over the control store.
type conversations struct {
	st      *store.Store
	dial    phonebook.Dialing
	log     *slog.Logger
	metrics *messaging.Metrics
}

var _ sip.ConversationGateway = conversations{}

// peerKey reduces a number to the key conversations are filed under; a
// number the address books cannot key (a short code) is used as it came.
func (c conversations) peerKey(number string) string {
	if k, ok := c.dial.Key(number, nil); ok {
		return k
	}
	return strings.TrimSpace(number)
}

func (c conversations) InboundFromTrunk(ctx context.Context, in sip.TrunkText) (bool, error) {
	toKey, ok := c.dial.Key(in.To, nil)
	if !ok {
		return false, nil
	}
	suffix, _ := phonebook.Suffix(toKey)
	text := store.IsTextContentType(in.ContentType) && utf8.Valid(in.Body) &&
		len(in.Body) <= maxInboundTextBytes && !strings.ContainsRune(string(in.Body), 0)
	body := strings.TrimSpace(string(in.Body))
	res, err := c.st.IngestTrunkText(ctx, store.TrunkText{
		TrunkID: in.TrunkID, ToKey: toKey, ToSuffix: suffix, FromKey: c.peerKey(in.From), FromName: in.FromName,
		Body: body, EventKey: in.EventKey, Skip: !text || body == "",
	})
	if err != nil {
		c.metrics.Inbound.WithLabelValues(messaging.ProviderSIPTrunk, "failed").Inc()
		return false, err
	}
	switch {
	case !res.Handled:
	case res.Duplicate:
		c.metrics.Inbound.WithLabelValues(messaging.ProviderSIPTrunk, "duplicate").Inc()
	case !text:
		c.metrics.Inbound.WithLabelValues(messaging.ProviderSIPTrunk, "not_text").Inc()
	default:
		c.metrics.Inbound.WithLabelValues(messaging.ProviderSIPTrunk, "stored").Inc()
	}
	if res.Keyword != "" {
		c.metrics.OptOut.WithLabelValues("keyword").Inc()
	}
	// The opt-out confirmation and the help text are Hello's own replies,
	// sent best effort: the customer's text is already stored.
	for _, r := range res.Replies {
		if _, err := c.st.SendMessage(ctx, store.MessageInput{
			Sender: r.From, SenderKind: store.MsgSenderSystem, TargetKind: store.MsgKindExternal, Target: r.To,
			ContentType: "text/plain", Body: []byte(r.Body), TrunkID: r.TrunkID,
		}); err != nil {
			c.log.Warn("inbox reply not queued", "conversation", res.ConversationID, "error", err)
		}
	}
	return res.Handled, nil
}
