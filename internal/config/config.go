// Package config loads Hello service configuration from HELLO_* environment
// variables into typed structs and validates it before anything binds a port.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/azrtydxb/hello/internal/telemetry"
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
}

// SIP is hello-sip's configuration. The bind address is where the node
// listens; the advertised address is what it puts in Via/Contact (spec §18).
type SIP struct {
	Common
	ValkeyAddr        string
	SIPBindAddr       string
	SIPAdvertisedAddr string
}

// LogValue keeps the database password out of logs.
func (c Control) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("node_id", c.NodeID),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("database_url", telemetry.RedactURL(c.DatabaseURL)),
	)
}

// LoadControl reads hello-control's configuration through getenv.
func LoadControl(getenv func(string) string) (Control, error) {
	r := reader{getenv: getenv}
	c := Control{
		Common:      r.common(":8081"),
		DatabaseURL: r.required("HELLO_DATABASE_URL"),
	}
	return c, r.err()
}

// LoadSIP reads hello-sip's configuration through getenv.
func LoadSIP(getenv func(string) string) (SIP, error) {
	r := reader{getenv: getenv}
	c := SIP{
		Common:            r.common(":8082"),
		ValkeyAddr:        r.required("HELLO_VALKEY_ADDR"),
		SIPBindAddr:       r.optional("HELLO_SIP_BIND_ADDR", "0.0.0.0:5060"),
		SIPAdvertisedAddr: r.optional("HELLO_SIP_ADVERTISED_ADDR", ""),
	}
	if c.SIPAdvertisedAddr == "" {
		host, _, err := net.SplitHostPort(c.SIPBindAddr)
		switch {
		case err != nil:
			r.fail("HELLO_SIP_BIND_ADDR", err)
		case host == "" || net.ParseIP(host).IsUnspecified():
			r.fail("HELLO_SIP_ADVERTISED_ADDR", errors.New("required when HELLO_SIP_BIND_ADDR is an unspecified address"))
		default:
			c.SIPAdvertisedAddr = c.SIPBindAddr
		}
	}
	return c, r.err()
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
