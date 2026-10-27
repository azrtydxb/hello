package main

// The phone syslog sink (phone-feature-parity S-27): UDP and TLS listeners
// in hello-control, off unless HELLO_SYSLOG_UDP_ADDR or _TLS_ADDR is set.

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/syslogsink"
	"github.com/prometheus/client_golang/prometheus"
)

// startSyslog binds the listeners now, so a bad address fails startup, and
// starts receiving once Valkey is connected; lines that arrive earlier wait
// in the socket buffer or are lost, as UDP syslog allows.
func startSyslog(ctx context.Context, cfg config.Syslog, phones syslogsink.Phones, vk *api.LazyValkey, reg prometheus.Registerer, log *slog.Logger) error {
	if !cfg.Enabled() {
		return nil
	}
	sc := syslogsink.Config{
		UDPAddr: cfg.UDPAddr, TLSAddr: cfg.TLSAddr, RatePerSec: float64(cfg.Rate), Burst: cfg.Burst,
		Phones: phones, Metrics: syslogsink.NewMetrics(reg), Log: log.With("component", "syslog"),
	}
	if cfg.TLSAddr != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return fmt.Errorf("tls certificate: %w", err)
		}
		sc.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	sc.Lines = &lazyLines{vk: vk}
	sink := syslogsink.New(sc)
	if err := sink.Start(ctx); err != nil {
		return err
	}
	log.Info("syslog sink listening", "udp", cfg.UDPAddr, "tls", cfg.TLSAddr)
	return nil
}

// lazyLines stores lines through the Valkey client once it exists; until
// then a line fails with api.ErrValkeyConnecting, which the sink logs.
type lazyLines struct{ vk *api.LazyValkey }

func (l *lazyLines) store() (*livestate.Store, error) {
	c := l.vk.Client()
	if c == nil {
		return nil, api.ErrValkeyConnecting
	}
	return livestate.New(c), nil
}

func (l *lazyLines) AppendSyslogPhone(ctx context.Context, id int64, line livestate.SyslogLine) error {
	s, err := l.store()
	if err != nil {
		return err
	}
	return s.AppendSyslogPhone(ctx, id, line)
}

func (l *lazyLines) AppendSyslogUnknown(ctx context.Context, line livestate.SyslogLine) error {
	s, err := l.store()
	if err != nil {
		return err
	}
	return s.AppendSyslogUnknown(ctx, line)
}
