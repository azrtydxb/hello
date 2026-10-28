package main

// The phone LDAP directory (spec pbx-gaps S-24): LDAPS on HELLO_LDAP_ADDR and
// optionally StartTLS on HELLO_LDAP_STARTTLS_ADDR, read-only.

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/ldapdir"
	provbook "github.com/azrtydxb/hello/internal/prov/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

// startLDAP binds the listeners and serves until ctx ends. Nothing starts
// while LDAP is off.
func startLDAP(ctx context.Context, cfg config.Control, st *store.Store, vk *api.LazyValkey, log *slog.Logger) error {
	if !cfg.LDAP.Enabled {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.LDAP.TLSCert, cfg.LDAP.TLSKey)
	if err != nil {
		return err
	}
	llog := log.With("component", "ldap")
	srv := ldapdir.New(ldapdir.Config{
		Base: cfg.LDAP.Base, Anon: cfg.LDAP.Anon, Backend: st,
		TLS:     &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Cache:   provbook.NewCache(vk.Client, llog),
		Limiter: ldapdir.NewValkeyLimiter(vk.Client),
		Log:     llog,
	})
	lc := &net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", cfg.LDAP.Addr)
	if err != nil {
		return err
	}
	var stls net.Listener
	if cfg.LDAP.StartTLSAddr != "" {
		if stls, err = lc.Listen(ctx, "tcp", cfg.LDAP.StartTLSAddr); err != nil {
			_ = ln.Close()
			return err
		}
		go func() { _ = srv.ServeStartTLS(stls) }()
	}
	go func() { _ = srv.ServeLDAPS(ln) }()
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	llog.Info("ldap directory listening", "ldaps", cfg.LDAP.Addr, "starttls", cfg.LDAP.StartTLSAddr, "anonymous", cfg.LDAP.Anon)
	return nil
}
