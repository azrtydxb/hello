//go:build race

package media

import "errors"

// OpusAvailable reports whether this build can encode and decode Opus. It
// cannot under the race detector: see opus.go.
const OpusAvailable = false

var errOpusRace = errors.New("media: opus is not built into race-detector builds")

func newOpusEncoder(int) (opusEncoder, error) { return nil, errOpusRace }

func newOpusDecoder(int) (opusDecoder, error) { return nil, errOpusRace }
