package fax

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Directions and statuses of a fax row (migration 00052).
const (
	DirInbound  = "inbound"
	DirOutbound = "outbound"

	StatusQueued    = "queued"    // outbound, waiting for a node
	StatusSending   = "sending"   // outbound, a node holds the lease
	StatusReceiving = "receiving" // inbound, the call is up
	StatusReceived  = "received"
	StatusSent      = "sent"
	StatusPartial   = "partial" // some pages arrived (or went) before T.30 failed
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Statuses lists every status, for filters and validation.
var Statuses = []string{StatusQueued, StatusSending, StatusReceiving, StatusReceived, StatusSent, StatusPartial, StatusFailed, StatusCancelled}

// Mail states of a received fax.
const (
	MailNone    = "none"
	MailPending = "pending"
	MailSending = "sending"
	MailSent    = "sent"
	MailFailed  = "failed"
)

// Trunk fax modes (migration 00052, pbx-gaps S-16).
const (
	ModePassthrough = "passthrough" // G.711 audio; the default
	ModeT38         = "t38"         // T.38 when the carrier offers it
	ModeOff         = "off"         // fax calls are refused with 488
)

// Outbound retry policy (pbx-gaps S-15): three attempts, five minutes apart,
// on busy, no answer or no carrier.
const (
	MaxAttempts  = 3
	RetryAfter   = 5 * time.Minute
	SendLease    = 90 * time.Second
	DefaultKeep  = 90 // days
	MinKeep      = 1
	MaxKeep      = 3650
	PurgeBatch   = 500
	MailAttempts = 6
)

// Errors of the store and the API.
var (
	ErrNotFound = errors.New("fax: not found")
	ErrNoPDF    = errors.New("fax: no document stored")
	// ErrNotCancellable means the fax is no longer queued or in progress.
	ErrNotCancellable = errors.New("fax: already finished")
)

// Fax is one fax as the API shows it.
type Fax struct {
	ID             int64      `json:"id"`
	Direction      string     `json:"direction"`
	Status         string     `json:"status"`
	PeerNumber     string     `json:"peerNumber"`
	LocalNumber    string     `json:"localNumber"`
	Destination    string     `json:"destination"`
	OwnerExtension string     `json:"ownerExtension"`
	Pages          int        `json:"pages"`
	Mode           string     `json:"mode"`
	BitRate        int        `json:"bitRate"`
	ResultCode     *int       `json:"resultCode"`
	ResultText     string     `json:"resultText"`
	HasPDF         bool       `json:"hasPdf"`
	CorrelationID  string     `json:"correlationId"`
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt"`
	MailTo         string     `json:"mailTo"`
	MailStatus     string     `json:"mailStatus"`

	// ObjectKey is the stored PDF; it never leaves the server.
	ObjectKey string `json:"-"`
	TrunkName string `json:"-"`
}

// Destination is a named fax destination: the extension that owns what
// arrives there and an optional address the PDF goes to instead of the
// owner's.
type Destination struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Extension string    `json:"extension"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListFilter selects faxes for the API.
type ListFilter struct {
	Direction string
	Status    string
	// Owners limits the result to faxes owned by these extension numbers
	// (the self-service user); nil means no limit.
	Owners []string
	Limit  int
	// Before, when positive, returns only faxes with a smaller id (the
	// cursor of the previous page).
	Before int64
}

// Finish is how a T.30 session ended, written to the row.
type Finish struct {
	Status     string
	Pages      int
	Mode       string
	BitRate    int
	ResultCode int
	ResultText string
	ObjectKey  string
	// MailTo, when set on a received fax, queues the email.
	MailTo string
}

// Store is what hello-sip needs from the fax table; *store.Store provides it.
type Store interface {
	// BeginReceive inserts the row of an answered inbound fax.
	BeginReceive(ctx context.Context, in Receive) (id int64, err error)
	// Finish completes a row (inbound or outbound).
	Finish(ctx context.Context, id int64, f Finish) error
	// ClaimOutbound takes up to limit queued faxes that are due for node,
	// under a lease, and counts the attempt.
	ClaimOutbound(ctx context.Context, node string, lease time.Duration, limit int) ([]Fax, error)
	// Renew extends node's lease on a fax it is sending; false when the fax
	// was cancelled or the lease went to another node.
	Renew(ctx context.Context, id int64, node string, lease time.Duration) (bool, error)
	// Retry puts an outbound fax back in the queue for another attempt.
	Retry(ctx context.Context, id int64, after time.Duration, resultText string) error
	// Link records the CDR correlation id of the call carrying the fax.
	Link(ctx context.Context, id int64, correlationID string) error
}

// Receive is the start of an inbound fax.
type Receive struct {
	PeerNumber     string
	LocalNumber    string
	DestinationID  int64
	OwnerExtension string
	CorrelationID  string
}

// Objects is the PDF store: the media bucket under the fax/ prefix.
type Objects interface {
	Put(ctx context.Context, key string, pdf []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Remove(ctx context.Context, key string) error
}

// ObjectKey is the stored name of fax id: fax/<yyyy>/<id>.pdf.
func ObjectKey(id int64, at time.Time) string {
	return fmt.Sprintf("fax/%04d/%d.pdf", at.UTC().Year(), id)
}

// AttachmentName is the file name of the PDF in an email:
// fax-<yyyymmdd-hhmm>-<id>.pdf.
func AttachmentName(id int64, at time.Time) string {
	return fmt.Sprintf("fax-%s-%d.pdf", at.UTC().Format("20060102-1504"), id)
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// ValidDestinationNumber reports whether n is an E.164 number, the form the
// API takes for an outbound fax.
func ValidDestinationNumber(n string) bool { return e164.MatchString(n) }

var callerID = regexp.MustCompile(`^\+?[0-9]{1,20}$`)

// ValidCallerID reports whether n can be a fax station identifier.
func ValidCallerID(n string) bool { return callerID.MatchString(n) }

// Ident turns a caller ID into the T.30 station identifier: digits and
// spaces up to 20 characters.
func Ident(n string) string {
	var b strings.Builder
	for _, r := range n {
		if r >= '0' && r <= '9' || r == '+' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 20 {
		s = s[len(s)-20:]
	}
	return s
}

// ResultFor maps a T.30 outcome to the status of the row: success is
// received or sent; some pages without success is partial; otherwise failed.
func ResultFor(direction string, success bool, pages int) string {
	switch {
	case success && direction == DirInbound:
		return StatusReceived
	case success:
		return StatusSent
	case pages > 0:
		return StatusPartial
	}
	return StatusFailed
}

// MailJob is a received fax waiting to be emailed.
type MailJob struct {
	ID int64
	// To are the recipients: the destination's address, or the enabled users
	// linked to the owner extension. Empty when neither exists.
	To        []string
	Peer      string
	Pages     int
	CreatedAt time.Time
	Object    string
	Attempts  int
	Owner     string
}
