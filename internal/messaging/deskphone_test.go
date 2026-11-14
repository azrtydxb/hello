package messaging_test

// Desk-phone texting and contact naming against PostgreSQL (spec
// messaging-channels M-15, M-26). They skip without HELLO_TEST_DATABASE_URL.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

// deskRig is the inbox rig plus an extension for the agent, with delivery to
// the desk phone turned on at the number and for the user.
type deskRig struct {
	*rig
	ext, extOther string
	extID         int64
	peer          string
}

func newDeskRig(t *testing.T) *deskRig {
	t.Helper()
	r := newRig(t)
	ctx := context.Background()
	e, err := r.st.CreateExtension(ctx, "test", "101", "Agent", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.st.CreateExtension(ctx, "test", "102", "Other", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetUserExtension(ctx, "test", r.agent, &e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetUserExtension(ctx, "test", r.other, &o.ID); err != nil {
		t.Fatal(err)
	}
	d := &deskRig{rig: r, ext: "101", extOther: "102", extID: e.ID}
	d.peer, _ = phonebook.Key(customer)
	d.setDesk(true, true)
	return d
}

// setDesk turns delivery on or off at the number and for the agent.
func (d *deskRig) setDesk(address, user bool) {
	d.t.Helper()
	ctx := context.Background()
	in := store.MsgAddressInput{ChannelID: d.addr.ChannelID, Address: d.addr.Address, Key: d.addr.Key, Enabled: true,
		DeskPhoneDelivery: address, OwnerExtensionID: d.addr.OwnerExtensionID}
	if _, err := d.st.UpdateMsgAddress(ctx, "test", d.addr.ID, in); err != nil {
		d.t.Fatal(err)
	}
	if _, err := d.st.UpdateProfile(ctx, "test", d.agent, store.ProfileChange{DeskphoneDelivery: &user}); err != nil {
		d.t.Fatal(err)
	}
}

// queued is the number of SIP texts queued for the extension.
func (d *deskRig) queued(ext string) int {
	return d.count(`SELECT count(*) FROM messages WHERE recipient = $1 AND recipient_kind = 'extension' AND conversation_message_id IS NOT NULL`, ext)
}

// TestDeskPhoneDelivery fails if a conversation assigned to a user does not
// queue exactly one SIP text for their extension when the number and the user
// both allow it, if a later text of the customer does not follow it, if
// assigning again queues the same text twice, if either switch off still
// delivers, if the row names anyone but the customer as sender, if a long
// text is not split into numbered parts, or if a text the phone cannot take
// (SIP text messaging off) fails the ingest.
func TestDeskPhoneDelivery(t *testing.T) {
	d := newDeskRig(t)
	ctx := context.Background()

	conv := d.ingest(customer, "I need help").ConversationID
	if n := d.queued(d.ext); n != 0 {
		t.Fatalf("an unassigned conversation queued %d texts for the desk phone", n)
	}
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), conv, d.agent); err != nil {
		t.Fatal(err)
	}
	if n := d.queued(d.ext); n != 1 {
		t.Fatalf("assigning queued %d texts, want 1", n)
	}
	var sender, senderKind, scope, body string
	var cm int64
	if err := d.db.QueryRowContext(ctx, `SELECT sender, sender_kind, scope, body_text, conversation_message_id FROM messages
		WHERE recipient = '101'`).Scan(&sender, &senderKind, &scope, &body, &cm); err != nil {
		t.Fatal(err)
	}
	if sender != d.peer || senderKind != "external" || scope != "direct" || body != "I need help" || cm == 0 {
		t.Fatalf("queued sender %q kind %q scope %q body %q cm %d", sender, senderKind, scope, body, cm)
	}
	// The same assignment again does not repeat the text.
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), conv, d.agent); err != nil {
		t.Fatal(err)
	}
	if n := d.queued(d.ext); n != 1 {
		t.Fatalf("assigning twice queued %d texts, want 1", n)
	}
	// A new text of the customer follows.
	d.ingest(customer, "are you there?")
	if n := d.queued(d.ext); n != 2 {
		t.Fatalf("a new customer text queued %d in all, want 2", n)
	}
	// A long text is split into numbered parts.
	d.ingest(customer, strings.Repeat("lorem ipsum ", 120))
	var parts int
	var first string
	if err := d.db.QueryRowContext(ctx, `SELECT count(*), min(body_text) FILTER (WHERE body_text LIKE '(1/%') FROM messages
		WHERE recipient = '101' AND body_text LIKE '(%/%) %'`).Scan(&parts, &first); err != nil || parts < 2 || first == "" {
		t.Fatalf("long text parts = %d, %q, %v", parts, first, err)
	}
	// Another user's conversation never reaches this phone.
	other := d.ingest("+32475999999", "for someone else").ConversationID
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), other, d.other); err != nil {
		t.Fatal(err)
	}
	if n := d.queued(d.extOther); n != 0 {
		t.Fatalf("a user who did not turn delivery on got %d texts", n)
	}

	before := d.queued(d.ext)
	d.setDesk(true, false)
	d.ingest(customer, "user switch off")
	d.setDesk(false, true)
	d.ingest(customer, "address switch off")
	if n := d.queued(d.ext); n != before {
		t.Fatalf("a switch that is off still delivered: %d texts, was %d", n, before)
	}

	d.setDesk(true, true)
	if _, err := d.db.ExecContext(ctx, `UPDATE message_settings SET enabled = FALSE`); err != nil {
		t.Fatal(err)
	}
	res, err := d.st.IngestTrunkText(ctx, d.text(customer, "while text messaging is off"))
	if err != nil || res.MessageID == 0 {
		t.Fatalf("an ingest with text messaging off = %+v, %v; the inbox must still store the text", res, err)
	}
	if n := d.queued(d.ext); n != before {
		t.Fatalf("a text was queued while text messaging is off: %d, was %d", n, before)
	}
}

// TestDeskPhoneClaimDelivers fails if an agent who takes a conversation from
// the pool does not get its latest customer text on the desk phone.
func TestDeskPhoneClaimDelivers(t *testing.T) {
	d := newDeskRig(t)
	conv := d.ingest(customer, "first").ConversationID
	d.ingest(customer, "second")
	if _, err := d.st.ClaimConversation(context.Background(), store.InboxAccess{UserID: d.agent, All: true, Actor: "user:agent", Name: "agent"}, conv); err != nil {
		t.Fatal(err)
	}
	if n := d.count(`SELECT count(*) FROM messages WHERE recipient = '101' AND body_text = 'second'`); n != 1 {
		t.Fatalf("claim queued the latest text %d times, want 1", n)
	}
	if n := d.queued(d.ext); n != 1 {
		t.Fatalf("claim queued %d texts, want only the latest", n)
	}
}

// TestDeskPhoneBridge fails if a reply typed on a phone is sent as anyone but
// the phone's own user, goes into a conversation the user may not use, reaches
// an opted-out customer, ignores the rate limit, or if a number that is not a
// customer of the inbox is claimed by the gateway.
func TestDeskPhoneBridge(t *testing.T) {
	d := newDeskRig(t)
	ctx := context.Background()
	conv := d.ingest(customer, "I need help").ConversationID
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), conv, d.agent); err != nil {
		t.Fatal(err)
	}

	r, err := d.st.ReplyFromDevice(ctx, d.ext, d.peer, "on my way")
	if err != nil || !r.Handled || r.Refusal != "" || r.ConversationID != conv {
		t.Fatalf("reply = %+v, %v", r, err)
	}
	var uid int64
	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT sender_user_id, status FROM conversation_messages
		WHERE conversation_id = $1 AND direction = 'out'`, conv).Scan(&uid, &status); err != nil || uid != d.agent || status != "sending" {
		t.Fatalf("stored reply by %d status %q, %v; want the agent, sending", uid, status, err)
	}
	if n := d.count(`SELECT count(*) FROM messages WHERE recipient = $1 AND recipient_kind = 'external' AND body_text = 'on my way'`, d.peer); n != 1 {
		t.Fatalf("the reply is %d texts in the trunk queue, want 1", n)
	}

	// Not the inbox's: nobody the inbox knows, and an extension the user does not own.
	if r, err := d.st.ReplyFromDevice(ctx, d.ext, "+32470000000", "hi"); err != nil || r.Handled {
		t.Fatalf("a number with no conversation = %+v, %v; want it left to the ordinary path", r, err)
	}
	if r, err := d.st.ReplyFromDevice(ctx, "999", d.peer, "hi"); err != nil || r.Handled {
		t.Fatalf("an extension with no user = %+v, %v", r, err)
	}
	// The other user's phone cannot reply into the agent's conversation.
	if r, err := d.st.ReplyFromDevice(ctx, d.extOther, d.peer, "I will take it"); err != nil || !r.Handled || r.Refusal == "" {
		t.Fatalf("a reply into another agent's conversation = %+v, %v; want a refusal", r, err)
	}
	if n := d.count(`SELECT count(*) FROM messages WHERE body_text = 'I will take it'`); n != 0 {
		t.Fatalf("a refused reply was queued %d times", n)
	}

	// Opt-out: no one can text the customer.
	d.ingest(customer, "STOP")
	if r, err := d.st.ReplyFromDevice(ctx, d.ext, d.peer, "wait"); err != nil || !r.Handled || r.Refusal == "" {
		t.Fatalf("a reply to an opted-out customer = %+v, %v", r, err)
	}
	if n := d.count(`SELECT count(*) FROM messages WHERE body_text = 'wait'`); n != 0 {
		t.Fatal("a reply to an opted-out customer reached the queue")
	}
	if _, err := d.st.Reconsent(ctx, "user:admin", d.optOutID(), "verbal"); err != nil {
		t.Fatal(err)
	}

	// The rate limit.
	if _, err := d.db.ExecContext(ctx, `UPDATE message_settings SET rate_per_minute = 1`); err != nil {
		t.Fatal(err)
	}
	r, err = d.st.ReplyFromDevice(ctx, d.ext, d.peer, "again")
	if err != nil || !r.Handled || !r.Limited || r.Refusal == "" {
		t.Fatalf("a reply over the rate limit = %+v, %v", r, err)
	}
}

func (d *deskRig) optOutID() int64 {
	d.t.Helper()
	var id int64
	if err := d.db.QueryRowContext(context.Background(), `SELECT id FROM sms_opt_outs WHERE state = 'out' LIMIT 1`).Scan(&id); err != nil {
		d.t.Fatal(err)
	}
	return id
}

// TestDeskPhoneOwnerExtension fails if the phone of the extension that owns a
// number cannot reply into an unassigned or its own conversation on it, or
// can reply into one assigned to someone else.
func TestDeskPhoneOwnerExtension(t *testing.T) {
	d := newDeskRig(t)
	ctx := context.Background()
	in := store.MsgAddressInput{ChannelID: d.addr.ChannelID, Address: d.addr.Address, Key: d.addr.Key, Enabled: true, OwnerExtensionID: &d.extID}
	if _, err := d.st.UpdateMsgAddress(ctx, "test", d.addr.ID, in); err != nil {
		t.Fatal(err)
	}
	unassigned := d.ingest(customer, "hello?").ConversationID
	r, err := d.st.ReplyFromDevice(ctx, d.ext, d.peer, "reception here")
	if err != nil || !r.Handled || r.Refusal != "" || r.ConversationID != unassigned {
		t.Fatalf("the owner's reply into an unassigned conversation = %+v, %v", r, err)
	}
	// Now the conversation is the agent's (a reply claims it). Give it to the
	// other user: the owner may no longer use it.
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), unassigned, d.other); err != nil {
		t.Fatal(err)
	}
	r, err = d.st.ReplyFromDevice(ctx, d.ext, d.peer, "still me")
	if err != nil || !r.Handled || r.Refusal == "" {
		t.Fatalf("a reply into someone else's conversation on the owned number = %+v, %v", r, err)
	}
	// An owner that is not an extension is refused at configuration time.
	bad := int64(999999)
	in.OwnerExtensionID = &bad
	var v *store.ValidationError
	if _, err := d.st.UpdateMsgAddress(ctx, "test", d.addr.ID, in); !errors.As(err, &v) {
		t.Fatalf("an unknown owner extension = %v, want a validation error", err)
	}
}

// TestContactNaming fails if a personal contact names the peer for anyone but
// its owner, if a stored peer name names a personal book, or if the name is
// not refreshed after a contact is added.
func TestContactNaming(t *testing.T) {
	d := newDeskRig(t)
	ctx := context.Background()
	conv := d.ingest(customer, "hello").ConversationID
	if _, err := d.st.AssignConversation(ctx, d.admAcc(), conv, d.agent); err != nil {
		t.Fatal(err)
	}
	entry := phonebook.EntryInput{FirstName: "Carla", LastName: "Peeters", Numbers: []phonebook.Number{{Label: phonebook.LabelMobile, Number: customer}}}
	if errs := entry.Normalize(phonebook.Dialing{}); len(errs) > 0 {
		t.Fatal(errs)
	}
	got, err := d.st.AddConversationContact(ctx, d.agentAcc(), conv, "personal", entry)
	if err != nil {
		t.Fatal(err)
	}
	if got.PeerName != "Carla Peeters" || got.PeerNameSource != "personal" {
		t.Fatalf("the owner sees %q (%s) after adding the contact", got.PeerName, got.PeerNameSource)
	}
	adm, err := d.st.GetConversation(ctx, d.admAcc(), conv)
	if err != nil || adm.PeerName != "" {
		t.Fatalf("a supervisor sees %q, %v; a personal contact names the peer for its owner only", adm.PeerName, err)
	}
	var stored, src string
	if err := d.db.QueryRowContext(ctx, `SELECT peer_name, peer_name_source FROM conversations WHERE id = $1`, conv).Scan(&stored, &src); err != nil || stored != "" || src == "personal" {
		t.Fatalf("stored name %q source %q, %v; a personal book must never be stored", stored, src, err)
	}
	// Texts to the desk phone are named for that user.
	d.ingest(customer, "second")
	var name string
	if err := d.db.QueryRowContext(ctx, `SELECT sender_name FROM messages WHERE body_text = 'second' AND recipient = '101'`).Scan(&name); err != nil || name != "Carla Peeters" {
		t.Fatalf("the desk phone text is from %q, %v", name, err)
	}

	// The company book: a supervisor adds it, the name is stored and shared.
	other := d.ingest("+32475888888", "hi").ConversationID
	co := phonebook.EntryInput{Company: "Acme", LastName: "Support", Numbers: []phonebook.Number{{Label: phonebook.LabelWork, Number: "+32475888888"}}}
	if errs := co.Normalize(phonebook.Dialing{}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := d.st.AddConversationContact(ctx, d.agentAcc(), other, "company", co); err == nil {
		t.Fatal("an agent without supervisor rights added to the company book")
	}
	shared, err := d.st.AddConversationContact(ctx, d.admAcc(), other, "company", co)
	if err != nil || shared.PeerName == "" || shared.PeerNameSource != "company" {
		t.Fatalf("company contact = %+v, %v", shared, err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT peer_name_source FROM conversations WHERE id = $1`, other).Scan(&src); err != nil || src != "company" {
		t.Fatalf("stored source = %q, %v; want company", src, err)
	}
	if _, err := d.st.AddConversationContact(ctx, d.admAcc(), other, "bogus", co); err == nil {
		t.Fatal("an unknown book was accepted")
	}
}
