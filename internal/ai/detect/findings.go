package detect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/azrtydxb/hello/internal/store"
)

// persist stores one pass: every candidate becomes or refreshes a finding
// (spec S-21), and the types of detectors that failed keep their findings
// open, since a failed detector proves nothing about its candidates.
func persist(ctx context.Context, r *Run, res Result) error {
	in := make([]store.AIFindingInput, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		ev, err := json.Marshal(c.Evidence)
		if err != nil {
			return fmt.Errorf("encode evidence of %s: %w", c.ID, err)
		}
		in = append(in, store.AIFindingInput{CandidateID: c.ID, Type: c.Type, Subject: c.Subject,
			Severity: c.Severity, Title: c.Title, Evidence: ev})
	}
	skip := make([]string, 0, len(res.Errors))
	for name := range res.Errors {
		skip = append(skip, name)
	}
	slices.Sort(skip)
	return r.Store.UpsertAIFindings(ctx, r.Now, in, skip)
}

// setFingerprint identifies the open set: its candidate ids and severities.
// The model is asked again only when it changes.
func setFingerprint(open []store.AIFinding) string {
	lines := make([]string, len(open))
	for i, f := range open {
		lines[i] = f.CandidateID + "|" + f.Severity
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}
