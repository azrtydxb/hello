package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/store"
)

// crmAudio opens a call recording's audio from object storage for the CRM
// connectors that upload the file themselves (Pipedrive).
type crmAudio struct {
	st   *store.Store
	objs *api.MinioObjects
}

// Open implements crm.AudioSource.
func (a crmAudio) Open(ctx context.Context, recordingID int64) (crm.RecordingFile, error) {
	rec, err := a.st.GetRecording(ctx, recordingID)
	if errors.Is(err, store.ErrNotFound) {
		return crm.RecordingFile{}, crm.ErrAudioGone
	}
	if err != nil {
		return crm.RecordingFile{}, err
	}
	o, err := a.objs.OpenAudio(ctx, api.RecordingsBucket, rec.Object)
	if err != nil {
		return crm.RecordingFile{}, err
	}
	return crm.RecordingFile{Body: o.Body, Size: o.Size, Name: fmt.Sprintf("recording-%d.wav", recordingID), ContentType: "audio/wav"}, nil
}
