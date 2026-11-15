package directory

// What the sign-in side needs from the engine: the Google administrator
// consent of an oauth_admin connector, and the LDAP bind login of the users
// an AD connector syncs (spec I-29, I-30).

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Consenter is a driver with a one-time administrator consent (Google
// oauth_admin): the authorization URL and the redemption of its code into a
// new credential.
type Consenter interface {
	ConsentURL(cfg json.RawMessage, redirectURI, state, verifier string) (string, error)
	Redeem(ctx context.Context, cfg json.RawMessage, secret, code, verifier, redirectURI string, o FetchOptions) (string, error)
}

// ConsentURL is the authorization URL of connector id's consent.
func (e *Engine) ConsentURL(ctx context.Context, id int64, redirectURI, state, verifier string) (string, error) {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return "", err
	}
	d, err := e.driver(c.Kind)
	if err != nil {
		return "", err
	}
	cs, ok := d.(Consenter)
	if !ok {
		return "", fieldErr("kind", "this kind of connector has no consent step")
	}
	if !c.HasCredential {
		return "", fieldErr("credential", "set the client secret before granting consent")
	}
	return cs.ConsentURL(c.Config, redirectURI, state, verifier)
}

// CompleteConsent redeems a consent code and seals the credential with its
// refresh token, audited with actor.
func (e *Engine) CompleteConsent(ctx context.Context, actor string, id int64, code, verifier, redirectURI string) error {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return err
	}
	d, err := e.driver(c.Kind)
	if err != nil {
		return err
	}
	cs, ok := d.(Consenter)
	if !ok {
		return fieldErr("kind", "this kind of connector has no consent step")
	}
	secret, err := e.Store.Secret(ctx, id)
	if err != nil {
		return &Error{Code: CodeAuthFailed, Detail: "set the client secret before granting consent"}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	next, err := cs.Redeem(ctx, c.Config, secret, code, verifier, redirectURI, e.fetchOptions(c))
	if err != nil {
		return classify(err)
	}
	if _, err := e.Store.SaveConnector(ctx, actor, c, &next, Fingerprint(d.Identity(c.Config))); err != nil {
		return err
	}
	return e.Store.Audit(ctx, actor, "consent", "directory_connector", strconv.FormatInt(id, 10))
}

// LoginMethod returns how the users of connector id sign in, and the OIDC
// provider when it is oidc. A disabled connector's users cannot sign in.
func (e *Engine) LoginMethod(ctx context.Context, id int64) (method, provider string, err error) {
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return "", "", err
	}
	if !c.Enabled {
		return "", "", ErrDisabled
	}
	return c.Options.LoginMethod, c.Options.OIDCProvider, nil
}

// ErrBindRefused is a refused LDAP bind login. It says nothing about why.
var ErrBindRefused = errors.New("directory: bind login refused")

// BindLogin checks password by an LDAP bind as the user connector id synced
// under externalID (I-30). The DN is looked up with the service account and
// the escaped stable id; the bind itself uses the user's own credentials. The
// password is never stored or logged. Any refusal is ErrBindRefused; a
// directory that cannot be reached is a classified *Error.
func (e *Engine) BindLogin(ctx context.Context, id int64, externalID, password string) error {
	if password == "" || externalID == "" {
		return ErrBindRefused
	}
	c, err := e.Store.GetConnector(ctx, id)
	if err != nil {
		return err
	}
	if c.Kind != KindLDAP || !c.Enabled || c.Options.LoginMethod != LoginLDAPBind {
		return ErrBindRefused
	}
	var cfg LDAPConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return &Error{Code: CodeInternal, Detail: "the stored config is unreadable"}
	}
	secret, err := e.Store.Secret(ctx, id)
	if err != nil {
		return &Error{Code: CodeAuthFailed, Detail: "no credential is set"}
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	return bindLogin(ctx, cfg, secret, externalID, password, e.fetchOptions(c))
}

// idFilter selects the entry with stable id ext: an AD objectGUID (every byte
// hex-escaped), or an entryUUID or nsUniqueId (escaped).
func idFilter(ext string) string {
	esc := ldap.EscapeFilter(ext)
	alts := "(entryUUID=" + esc + ")(nsUniqueId=" + esc + ")"
	if b, ok := guidFromString(ext); ok {
		var sb strings.Builder
		for _, x := range b {
			fmt.Fprintf(&sb, "\\%02x", x)
		}
		alts = "(objectGUID=" + sb.String() + ")" + alts
	}
	return "(|" + alts + ")"
}

// guidFromString is guidString reversed: the mixed-endian bytes of a canonical GUID.
func guidFromString(s string) ([]byte, bool) {
	if !guidRe.MatchString(s) {
		return nil, false
	}
	h, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(h) != 16 {
		return nil, false
	}
	b := make([]byte, 16)
	b[0], b[1], b[2], b[3] = h[3], h[2], h[1], h[0]
	b[4], b[5] = h[5], h[4]
	b[6], b[7] = h[7], h[6]
	copy(b[8:], h[8:])
	return b, true
}

func bindLogin(ctx context.Context, cfg LDAPConfig, secret, ext, password string, o FetchOptions) error {
	conn, err := connect(ctx, cfg, secret, o)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	filter := "(&" + cfg.UserFilter + idFilter(ext) + ")"
	req := ldap.NewSearchRequest(cfg.BaseDN, cfg.scope(), ldap.NeverDerefAliases, 2, int(cfg.timeout().Seconds()), false,
		filter, []string{"objectGUID", "entryUUID", "nsUniqueId"}, nil)
	res, err := conn.Search(req)
	if err != nil {
		return classifyLDAP(err)
	}
	if len(res.Entries) != 1 || stableID(res.Entries[0]) != ext {
		return ErrBindRefused
	}
	// The bind as the user is the check. A locked-out, expired or disabled
	// AD account fails it like a wrong password.
	if err := conn.Bind(res.Entries[0].DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) || ldap.IsErrorWithCode(err, ldap.LDAPResultInappropriateAuthentication) ||
			ldap.IsErrorWithCode(err, ldap.LDAPResultUnwillingToPerform) || ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights) {
			return ErrBindRefused
		}
		return classifyLDAP(err)
	}
	return nil
}
