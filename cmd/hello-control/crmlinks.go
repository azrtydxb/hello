package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/store"
)

// crmLinks mints the recording links the CRM connectors write into their
// records (spec integrations I-33, I-53) with the recording-link store of
// P4. A link carries the connector that minted it, so disabling or deleting
// the connector revokes them all.
type crmLinks struct {
	Store     *store.Store
	PublicURL string
}

// Mint implements crm.LinkMinter. label is crm.LinkLabel: "crm:<id>".
func (l *crmLinks) Mint(ctx context.Context, recordingID int64, label string, ttl time.Duration) (string, time.Time, error) {
	if l.PublicURL == "" {
		return "", time.Time{}, errors.New("HELLO_PUBLIC_URL is not set, so a recording link has no address")
	}
	id, err := connectorOf(label)
	if err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	expires := time.Now().Add(ttl)
	if _, err := l.Store.CreateConnectorRecordingLink(ctx, id, recordingID, sum[:], expires); err != nil {
		return "", time.Time{}, err
	}
	return strings.TrimRight(l.PublicURL, "/") + "/r/" + token + ".wav", expires, nil
}

// RevokeAll implements crm.LinkMinter.
func (l *crmLinks) RevokeAll(ctx context.Context, label string) error {
	id, err := connectorOf(label)
	if err != nil {
		return err
	}
	_, err = l.Store.RevokeConnectorRecordingLinks(ctx, id)
	return err
}

func connectorOf(label string) (int64, error) {
	rest, ok := strings.CutPrefix(label, "crm:")
	id, err := strconv.ParseInt(rest, 10, 64)
	if !ok || err != nil || id < 1 {
		return 0, errors.New("not a connector link label")
	}
	return id, nil
}
