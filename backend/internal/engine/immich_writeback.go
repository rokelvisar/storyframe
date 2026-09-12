package engine

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

// writebackMarkerStart / writebackMarkerEnd delimit the block this app owns
// inside an asset's Immich description, so re-analyzing the same asset
// replaces its own summary instead of appending duplicates forever, while
// never touching whatever the user wrote there themselves.
const (
	writebackMarkerStart = "--- video-extractor AI summary"
	writebackMarkerEnd   = "--- end video-extractor summary ---"
)

// buildDescription merges a fresh AI story into an asset's existing Immich
// description, replacing any prior video-extractor block in place.
func buildDescription(existing, jobID string, analyzedAt time.Time, story string) string {
	base := stripPriorSummary(existing)
	block := writebackMarkerStart + " (job " + jobID + ", " + analyzedAt.Format("2006-01-02") + ") ---\n" +
		strings.TrimSpace(story) + "\n" + writebackMarkerEnd
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}

func stripPriorSummary(s string) string {
	start := strings.Index(s, writebackMarkerStart)
	if start < 0 {
		return strings.TrimSpace(s)
	}
	rest := s[start:]
	end := strings.Index(rest, writebackMarkerEnd)
	if end < 0 {
		return strings.TrimSpace(s[:start])
	}
	after := rest[end+len(writebackMarkerEnd):]
	return strings.TrimSpace(s[:start] + after)
}

// immichMetadataSegment / immichMetadataPayload are the structured value
// written to Immich's custom per-asset metadata sidecar (key "video-extractor"),
// a separate, machine-readable channel from the human-readable description.
type immichMetadataSegment struct {
	StartSec      float64 `json:"startSec"`
	EndSec        float64 `json:"endSec"`
	InterestScore float64 `json:"interestScore"`
	Description   string  `json:"description,omitempty"`
}

type immichMetadataPayload struct {
	JobID            string                  `json:"jobId"`
	AnalyzedAt       string                  `json:"analyzedAt"`
	AnalysisProvider string                  `json:"analysisProvider,omitempty"`
	OutputLanguage   string                  `json:"outputLanguage,omitempty"`
	Story            string                  `json:"story,omitempty"`
	Transcript       string                  `json:"transcript,omitempty"`
	Segments         []immichMetadataSegment `json:"segments,omitempty"`
}

const immichMetadataKey = "video-extractor"

func buildMetadataPayload(job *models.Job, analyzedAt time.Time) immichMetadataPayload {
	segs := make([]immichMetadataSegment, 0, len(job.Segments))
	for _, s := range job.Segments {
		segs = append(segs, immichMetadataSegment{
			StartSec: s.StartSec, EndSec: s.EndSec,
			InterestScore: s.InterestScore, Description: s.Description,
		})
	}
	return immichMetadataPayload{
		JobID:            job.ID,
		AnalyzedAt:       analyzedAt.UTC().Format(time.RFC3339),
		AnalysisProvider: job.AnalysisProvider,
		OutputLanguage:   job.OutputLanguage,
		Story:            job.OverviewStory,
		Transcript:       job.Transcript,
		Segments:         segs,
	}
}

// writeBackToImmich enriches the source asset in the user's own Immich once a
// job is done: the human-readable story goes into the asset's description
// (visible in Immich's own UI), and the full structured analysis (story,
// transcript, per-segment descriptions) into Immich's custom-metadata sidecar
// under the "video-extractor" key, for anything else that wants to query it.
// Best-effort and non-blocking: runs in its own goroutine, never affects job
// completion, and waits briefly for the overview to land if it hasn't yet
// (job completion doesn't wait on it — see maybeComplete).
func (e *Engine) writeBackToImmich(jobID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		job, err := e.waitForOverview(ctx, jobID, 60*time.Second)
		if err != nil {
			e.log.Warn("immich write-back: no overview to write back", "job", jobID, "err", err)
			e.LogEvent(jobID, models.EventWarn, "immich write-back: no overview available, skipping")
			return
		}
		if job.ImmichAssetID == "" {
			return
		}

		asset, err := e.immich.ResolveAsset(ctx, job.ImmichAssetID)
		if err != nil {
			e.log.Warn("immich write-back: resolve asset", "job", jobID, "asset", job.ImmichAssetID, "err", err)
			e.LogEvent(jobID, models.EventWarn, "immich write-back: resolve asset failed: %v", err)
			return
		}

		now := time.Now()
		desc := buildDescription(asset.Description, job.ID, now, job.OverviewStory)
		if err := e.immich.UpdateDescription(ctx, job.ImmichAssetID, desc); err != nil {
			e.log.Warn("immich write-back: update description", "job", jobID, "err", err)
			e.LogEvent(jobID, models.EventWarn, "immich write-back: update description failed: %v", err)
		}

		payload := buildMetadataPayload(job, now)
		if err := e.immich.UpsertMetadata(ctx, job.ImmichAssetID, immichMetadataKey, payload); err != nil {
			e.log.Warn("immich write-back: upsert metadata", "job", jobID, "err", err)
			e.LogEvent(jobID, models.EventWarn, "immich write-back: upsert metadata failed: %v", err)
			return
		}
		_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
			j.ImmichWrittenBack = true
			return nil
		})
		e.log.Info("immich write-back complete", "job", jobID, "asset", job.ImmichAssetID)
		e.LogEvent(jobID, models.EventInfo, "immich write-back complete (asset %s)", job.ImmichAssetID)
	}()
}

// TriggerImmichWriteback starts a manual write-back for an origin=immich job
// (the API handler for POST /jobs/{id}/immich-writeback). Unlike the
// automatic path in maybeComplete, this can be called at any time — the user
// clicked the button — and isn't gated on Job.ImmichAutoWriteback, only on
// the server-wide master switch and the job actually being Immich-sourced.
func (e *Engine) TriggerImmichWriteback(jobID string) error {
	if !e.immichWriteback || e.immich == nil {
		return errors.New("immich write-back is not enabled on this server")
	}
	job, err := e.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if job.Origin != models.OriginImmich {
		return errors.New("job is not sourced from immich")
	}
	if job.ImmichAssetID == "" {
		return errors.New("job has no immich asset id")
	}
	e.LogEvent(jobID, models.EventInfo, "immich: writing analysis back to source asset (manual)")
	e.writeBackToImmich(jobID)
	return nil
}

// waitForOverview polls the store until Job.OverviewStory is populated (every
// provider, including noop, eventually sets a non-empty one) or the timeout
// elapses — job completion doesn't wait on the overview goroutine, so it can
// still be in flight when write-back starts.
func (e *Engine) waitForOverview(ctx context.Context, jobID string, timeout time.Duration) (*models.Job, error) {
	deadline := time.Now().Add(timeout)
	for {
		job, err := e.store.GetJob(jobID)
		if err != nil {
			return nil, err
		}
		if job.OverviewStory != "" {
			return job, nil
		}
		if time.Now().After(deadline) {
			return job, errors.New("overview story did not become available in time")
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
