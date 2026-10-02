package sipua

import (
	"context"
	"strconv"
	"time"

	"github.com/emiago/sipgo/sip"
)

// Probe sends one REGISTER without credentials and returns the first final
// response, so a test can tell a challenge (401) from an outright 403.
func (p *Phone) Probe(ctx context.Context, expires time.Duration) (*sip.Response, error) {
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: p.opts.Domain})
	aor := p.AOR()
	req.AppendHeader(&sip.ToHeader{Address: aor})
	req.AppendHeader(&sip.FromHeader{Address: aor, Params: tagParams()})
	req.AppendHeader(sip.NewHeader("Contact", "<"+p.contact.Address.String()+">"))
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(int(expires/time.Second))))
	req.SetDestination(p.opts.Proxy)
	return p.client.Do(ctx, req)
}

// RegisterContact registers an explicit contact URI with this phone's
// credentials — e.g. refreshing another phone's binding through a different
// node.
func (p *Phone) RegisterContact(ctx context.Context, contact string, expires time.Duration) (*sip.Response, error) {
	return p.register(ctx, contact, expires, p.opts.Password)
}

// ContactString is the phone's Contact URI as it appears in bindings.
func (p *Phone) ContactString() string { return p.contact.Address.String() }
