package prov

import (
	"regexp"
	"strings"
)

// CodecInfo is the audio codec plan a built-in template renders: HD voice
// order Opus first where the model has it, then G.722, PCMU and PCMA.
// Every other codec (G.729, G.726, G.723, iLBC and so on) is left out:
// Hello offers phones Opus, G.722, PCMU and PCMA only, and the relay passes
// what two phones agree on between them.
type CodecInfo struct {
	// Opus is set when the phone model supports Opus, so the template
	// lists it; a model that does not know the key would at best ignore it.
	Opus bool
}

// opusModels are the models known to support Opus, per vendor, from the
// vendors' datasheets and administrator guides. A model not matched here
// (or a vendor without an entry) gets G.722, PCMU and PCMA: the codec order
// is still HD, and nothing is sent that a phone could mis-read.
var opusModels = map[Vendor]*regexp.Regexp{
	// Yealink: the T5x, T4xS and T4xU families, the CP9xx conference
	// phones and the VP59. The T2x/T3x/T19 range has no Opus.
	Yealink: regexp.MustCompile(`^(SIP-?)?(T5\d|T4\d[SU]|CP9\d\d|VP59)`),
	// Grandstream: the GRP, WP and GXP16xx/GXP213x-217x families.
	Grandstream: regexp.MustCompile(`^(GRP|WP\d|GXP16|GXP21[3-9]|GXV|GSC)`),
	// Fanvil: the models on the 2.x firmware (X1, X210, X3U, X4U, X5U,
	// X6, X7, V6x, H2U); the older X3S/X4/X5S and i-series do not.
	Fanvil: regexp.MustCompile(`^(X1|X210|X3U|X4U|X5U|X6|X7|V6|H2U)`),
	// Snom: the D7xx and D8xx desk phones.
	Snom: regexp.MustCompile(`^(D7|D8|SNOMD7|SNOMD8)`),
	// Poly: VVX 2xx to 6xx, CCX, Edge E and Trio.
	Poly: regexp.MustCompile(`^(POLY)?(VVX[2-6]\d\d|CCX|EDGE|TRIO)`),
}

// NewCodecInfo is the codec plan for a vendor and model.
func NewCodecInfo(v Vendor, model string) CodecInfo {
	re := opusModels[v]
	if re == nil {
		return CodecInfo{}
	}
	m := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(model))
	return CodecInfo{Opus: re.MatchString(m)}
}
