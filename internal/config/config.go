// Package config loads Hello service configuration from HELLO_* environment
// variables into typed structs and validates it before anything binds a port.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/jackc/pgx/v5"
)

// Common holds the settings every Hello service shares.
type Common struct {
	NodeID          string
	HTTPAddr        string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	// DrainDelay is how long a node keeps serving with /readyz failing after
	// shutdown begins, so load balancers stop routing to it first.
	DrainDelay time.Duration
}

// Control is hello-control's configuration.
type Control struct {
	Common
	DatabaseURL string
	// Database is DatabaseURL parsed, so a malformed DSN fails at startup.
	Database *pgx.ConnConfig
	// ValkeyAddr is read for the live registrations/calls API.
	ValkeyAddr string
	// SIPDomain is the digest realm device HA1 values are computed for; it
	// must equal hello-sip's HELLO_SIP_DOMAIN.
	SIPDomain string
	// BootstrapAdminPassword creates the first admin user when none exist.
	BootstrapAdminPassword string
	SessionTTL             time.Duration
}

// SIP is hello-sip's configuration. The bind address is where the node
// listens; the advertised address is what it puts in Via/Contact (spec §18).
type SIP struct {
	Common
	ValkeyAddr        string
	SIPBindAddr       string
	SIPAdvertisedAddr string
	// DatabaseURL is used read-only for the configuration snapshot and to
	// insert CDRs.
	DatabaseURL string
	Database    *pgx.ConnConfig
	// SIPDomain is the digest realm and the host part of every AOR.
	SIPDomain          string
	NonceSecret        string
	RegisterMinExpires time.Duration
	RegisterMaxExpires time.Duration
	RingTimeout        time.Duration
	AuthFailLimit      int
	AuthFailWindow     time.Duration
	StateTimeout       time.Duration
	// MaxCallDuration ends a connected call that has run this long (both
	// legs get BYE), so a call whose phones vanished without BYE is cleared.
	MaxCallDuration time.Duration
}

// LogValue keeps the database password out of logs.
func (c Control) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("node_id", c.NodeID),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("database_url", telemetry.RedactURL(c.DatabaseURL)),
		slog.String("valkey_addr", c.ValkeyAddr),
		slog.String("sip_domain", c.SIPDomain),
	)
}

// LogValue keeps the database password and nonce secret out of logs.
func (c SIP) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("node_id", c.NodeID),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("sip_bind", c.SIPBindAddr),
		slog.String("sip_advertised", c.SIPAdvertisedAddr),
		slog.String("sip_domain", c.SIPDomain),
		slog.String("valkey_addr", c.ValkeyAddr),
		slog.String("database_url", telemetry.RedactURL(c.DatabaseURL)),
	)
}

// LoadControl reads hello-control's configuration through getenv.
func LoadControl(getenv func(string) string) (Control, error) {
	r := reader{getenv: getenv}
	c := Control{
		Common:                 r.common(":8081"),
		DatabaseURL:            r.required("HELLO_DATABASE_URL"),
		ValkeyAddr:             r.required("HELLO_VALKEY_ADDR"),
		SIPDomain:              r.required("HELLO_SIP_DOMAIN"),
		BootstrapAdminPassword: r.optional("HELLO_BOOTSTRAP_ADMIN_PASSWORD", ""),
		SessionTTL:             r.duration("HELLO_SESSION_TTL", 12*time.Hour),
	}
	c.Database = r.database(c.DatabaseURL)
	r.hostPort("HELLO_VALKEY_ADDR", c.ValkeyAddr)
	if c.SessionTTL == 0 {
		r.fail("HELLO_SESSION_TTL", errors.New("must be positive")) // a zero TTL makes every login expire at once
	}
	return c, r.err()
}

// LoadSIP reads hello-sip's configuration through getenv.
func LoadSIP(getenv func(string) string) (SIP, error) {
	r := reader{getenv: getenv}
	c := SIP{
		Common:             r.common(":8082"),
		ValkeyAddr:         r.required("HELLO_VALKEY_ADDR"),
		SIPBindAddr:        r.optional("HELLO_SIP_BIND_ADDR", "0.0.0.0:5060"),
		SIPAdvertisedAddr:  r.optional("HELLO_SIP_ADVERTISED_ADDR", ""),
		DatabaseURL:        r.required("HELLO_DATABASE_URL"),
		SIPDomain:          r.required("HELLO_SIP_DOMAIN"),
		NonceSecret:        r.required("HELLO_SIP_NONCE_SECRET"),
		RegisterMinExpires: r.duration("HELLO_SIP_REGISTER_MIN_EXPIRES", 60*time.Second),
		RegisterMaxExpires: r.duration("HELLO_SIP_REGISTER_MAX_EXPIRES", time.Hour),
		RingTimeout:        r.duration("HELLO_SIP_RING_TIMEOUT", 30*time.Second),
		AuthFailLimit:      r.positiveInt("HELLO_SIP_AUTH_FAIL_LIMIT", 10),
		AuthFailWindow:     r.duration("HELLO_SIP_AUTH_FAIL_WINDOW", 5*time.Minute),
		StateTimeout:       r.duration("HELLO_SIP_STATE_TIMEOUT", 200*time.Millisecond),
		MaxCallDuration:    r.duration("HELLO_SIP_MAX_CALL_DURATION", 4*time.Hour),
	}
	if c.MaxCallDuration == 0 {
		r.fail("HELLO_SIP_MAX_CALL_DURATION", errors.New("must be positive"))
	}
	c.Database = r.database(c.DatabaseURL)
	r.hostPort("HELLO_VALKEY_ADDR", c.ValkeyAddr)
	if c.NonceSecret != "" && len(c.NonceSecret) < 32 {
		r.fail("HELLO_SIP_NONCE_SECRET", errors.New("must be at least 32 bytes"))
	}
	if c.RegisterMinExpires > c.RegisterMaxExpires {
		r.fail("HELLO_SIP_REGISTER_MIN_EXPIRES", errors.New("must not exceed HELLO_SIP_REGISTER_MAX_EXPIRES"))
	}
	bindHost, err := splitHost(c.SIPBindAddr)
	if err != nil {
		r.fail("HELLO_SIP_BIND_ADDR", err)
	}
	if c.SIPAdvertisedAddr == "" {
		if err == nil && unspecified(bindHost) {
			r.fail("HELLO_SIP_ADVERTISED_ADDR", errors.New("required when HELLO_SIP_BIND_ADDR is an unspecified address"))
		}
		c.SIPAdvertisedAddr = c.SIPBindAddr
	} else if host, err := splitHost(c.SIPAdvertisedAddr); err != nil {
		r.fail("HELLO_SIP_ADVERTISED_ADDR", err)
	} else if unspecified(host) {
		r.fail("HELLO_SIP_ADVERTISED_ADDR", errors.New("must be a reachable address, not unspecified"))
	}
	return c, r.err()
}

func splitHost(hostport string) (string, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err == nil && port == "" {
		err = errors.New("missing port")
	}
	return host, err
}

func unspecified(host string) bool {
	return host == "" || net.ParseIP(host).IsUnspecified()
}

type reader struct {
	getenv func(string) string
	errs   []error
}

func (r *reader) fail(key string, err error) {
	r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
}

func (r *reader) err() error { return errors.Join(r.errs...) }

func (r *reader) required(key string) string {
	v := r.getenv(key)
	if v == "" {
		r.fail(key, errors.New("required"))
	}
	return v
}

func (r *reader) optional(key, def string) string {
	if v := r.getenv(key); v != "" {
		return v
	}
	return def
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v := r.getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(key, err)
	} else if d < 0 {
		r.fail(key, errors.New("must not be negative"))
	}
	return d
}

// database parses a DSN; pgx's redaction of parse errors is best effort, so
// none of the input is echoed back.
func (r *reader) database(dsn string) *pgx.ConnConfig {
	if dsn == "" {
		return nil
	}
	db, err := pgx.ParseConfig(dsn)
	if err != nil {
		r.fail("HELLO_DATABASE_URL", errors.New("malformed connection string"))
	}
	return db
}

func (r *reader) hostPort(key, v string) {
	if v == "" {
		return
	}
	if _, err := splitHost(v); err != nil {
		r.fail(key, err)
	}
}

func (r *reader) positiveInt(key string, def int) int {
	v := r.getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		r.fail(key, errors.New("must be a positive integer"))
	}
	return n
}

func (r *reader) common(defaultHTTP string) Common {
	c := Common{
		NodeID:          r.required("HELLO_NODE_ID"),
		HTTPAddr:        r.optional("HELLO_HTTP_ADDR", defaultHTTP),
		ShutdownTimeout: r.duration("HELLO_SHUTDOWN_TIMEOUT", 30*time.Second),
		DrainDelay:      r.duration("HELLO_DRAIN_DELAY", 5*time.Second),
	}
	if err := c.LogLevel.UnmarshalText([]byte(r.optional("HELLO_LOG_LEVEL", "info"))); err != nil {
		r.fail("HELLO_LOG_LEVEL", err)
	}
	return c
}
