// Command hello-fax is Hello's fax engine: a T.30 terminator for receive and a
// sender for transmit, built on spandsp through cgo (decision D13). It lives in
// its own image so hello-sip stays pure Go. It holds no state worth keeping:
// hello-sip relays a fax call's media to a UDP port this service opens and
// collects the result over a small HTTP API (internal/fax/client.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/telemetry"
	"github.com/azrtydxb/hello/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hello-fax:", err)
		os.Exit(1)
	}
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s: must be a port number", key)
	}
	return n, nil
}

func run(args []string) error {
	if len(args) != 1 || args[0] != "serve" {
		return errors.New("usage: hello-fax serve")
	}
	listen := os.Getenv("HELLO_FAX_LISTEN")
	if listen == "" {
		listen = ":8083"
	}
	pmin, err := envInt("HELLO_FAX_RTP_PORT_MIN", 40000)
	if err != nil {
		return err
	}
	pmax, err := envInt("HELLO_FAX_RTP_PORT_MAX", 40999)
	if err != nil {
		return err
	}
	if pmin > pmax {
		return errors.New("HELLO_FAX_RTP_PORT_MIN must not exceed HELLO_FAX_RTP_PORT_MAX")
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(os.Getenv("HELLO_LOG_LEVEL"))); err != nil {
		level = slog.LevelInfo
	}
	log := telemetry.NewLogger(os.Stderr, level, "hello-fax", os.Getenv("HELLO_NODE_ID"))
	slog.SetDefault(log)
	if !engineAvailable {
		log.Warn("built without the spandsp engine: every fax session will be refused")
	}
	srv := newServer(newEngine(), fax.Rasteriser{Path: os.Getenv("HELLO_FAX_PDFTOPPM")}, os.Getenv("HELLO_FAX_TOKEN"), pmin, pmax, log)
	srv.mediaHost = os.Getenv("HELLO_FAX_MEDIA_HOST")
	hs := &http.Server{Addr: listen, Handler: srv.handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("hello-fax listening", "addr", listen, "rtp", fmt.Sprintf("%d-%d", pmin, pmax), "version", version.Version)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	srv.Shutdown()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}
