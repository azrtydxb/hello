package main

// The LAN chat widget listener (spec messaging-channels M-28): its own
// http.Server in hello-control, serving only /chat/* and /healthz. It starts
// only when HELLO_MSG_CHAT_LAN_ADDR is set, so a deployment that does not
// want the widget has no extra port. The admin API, the UI, OAuth, MCP and
// the provisioning and webhook paths are not routed on it at all.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/chat"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

// startChat starts the LAN chat listener and reports whether it did: an unset
// HELLO_MSG_CHAT_LAN_ADDR starts nothing and touches nothing.
func startChat(ctx context.Context, cfg config.Control, st *store.Store, box *secret.Box, vk *api.LazyValkey,
	smtp func() (mailer.Settings, error), reg prometheus.Registerer, log *slog.Logger) (bool, error) {
	if cfg.Chat.LANAddr == "" {
		log.Info("LAN chat widget disabled (HELLO_MSG_CHAT_LAN_ADDR not set)")
		return false, nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Chat.LANAddr)
	if err != nil {
		return false, err
	}
	clog := log.With("component", "chat")
	srv := &http.Server{
		Handler: chat.New(chat.Config{
			Backend:        st,
			Limiter:        chat.NewLimiter(vk.Client, clog),
			Key:            box.MAC("chat-pow", "v1"),
			Enabled:        cfg.Chat.Enabled,
			TrustedProxies: cfg.Chat.TrustedProxies,
			MaxInFlight:    cfg.Chat.MaxInFlight,
			MaxStreams:     cfg.Chat.MaxStreams,
			Mail:           chatMail(smtp),
			Turnstile:      chat.NewTurnstile(),
			Metrics:        chat.NewMetrics(reg),
			Log:            clog,
		}),
		// The listener's own timeouts (M-29a.8); the event stream clears its
		// write deadline itself.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("chat listener stopped", "error", err)
		}
	}()
	log.Info("LAN chat widget listening", "addr", cfg.Chat.LANAddr, "enabled", cfg.Chat.Enabled)
	return true, nil
}

// chatMail sends a transcript through the SMTP settings in force. The body is
// the visitor's own chat, sent to the address the visitor typed.
func chatMail(smtp func() (mailer.Settings, error)) func(ctx context.Context, to, subject, body string) error {
	return func(ctx context.Context, to, subject, body string) error {
		s, err := smtp()
		if err != nil {
			return err
		}
		msg, err := mailer.BuildNotice(s.From, to, subject, body, time.Now())
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return mailer.Deliver(cctx, s, to, msg)
	}
}

// effectiveSMTP returns the settings sends use now, or an error when email is
// not configured.
func effectiveSMTP(st *store.Store, env mailer.Settings) func() (mailer.Settings, error) {
	return func() (mailer.Settings, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, source, err := api.EffectiveSMTP(ctx, st, env)
		if err != nil {
			return mailer.Settings{}, err
		}
		if source == api.SMTPNone || s.Validate() != nil {
			return mailer.Settings{}, fmt.Errorf("email is not configured")
		}
		return s, nil
	}
}
