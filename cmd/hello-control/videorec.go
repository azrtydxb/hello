package main

import (
	"context"
	"io"
	"log/slog"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/vidrec"
	"github.com/prometheus/client_golang/prometheus"
)

// videoObjects is the recordings bucket as the video worker uses it.
type videoObjects struct{ o api.Objects }

var _ vidrec.Objects = videoObjects{}

func (v videoObjects) Open(ctx context.Context, object string) (io.ReadCloser, error) {
	a, err := v.o.OpenAudio(ctx, api.RecordingsBucket, object)
	if err != nil {
		return nil, err
	}
	return a.Body, nil
}

func (v videoObjects) Size(ctx context.Context, object string) (int64, error) {
	a, err := v.o.OpenAudio(ctx, api.RecordingsBucket, object)
	if err != nil {
		return 0, err
	}
	defer func() { _ = a.Body.Close() }()
	return a.Size, nil
}

func (v videoObjects) Put(ctx context.Context, object string, r io.Reader, size int64) error {
	return v.o.PutRecording(ctx, object, r, size)
}

func (v videoObjects) Remove(ctx context.Context, object string) error {
	return v.o.RemoveRecording(ctx, object)
}

// startVideoRecording runs the video recording jobs (S-42): finalizing the
// fragments of ended streams, ending the streams of a node that died, the
// retention and the storage gauge. Every replica runs them; the claims and the
// locks keep one at a time on a stream.
func startVideoRecording(ctx context.Context, quotaGB int, st *store.Store, objs api.Objects, reg prometheus.Registerer, log *slog.Logger) {
	w := &vidrec.Worker{Store: st, Objects: videoObjects{objs}, QuotaBytes: int64(quotaGB) << 30, Log: log.With("component", "video-recording")}
	w.Metrics = vidrec.NewMetrics(reg, w.Usage)
	go w.Run(ctx)
}
