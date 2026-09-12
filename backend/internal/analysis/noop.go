package analysis

import (
	"context"
	"fmt"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

// NoopProvider produces deterministic, dependency-free output. It is used when no
// AI gateway is configured and as a graceful degradation path. Interest scores are
// a mild heuristic: segments nearer the middle of the video and those carrying
// client-supplied hints score a little higher, so the UI still has something to
// prioritise.
type NoopProvider struct{}

func (p *NoopProvider) Name() string { return "noop" }

func (p *NoopProvider) AnalyzeOverview(_ context.Context, job *models.Job, _ string, meta models.OverviewMeta) (OverviewResult, error) {
	scores := make(map[int]float64, len(job.Segments))
	n := len(job.Segments)
	for i := range job.Segments {
		// triangular weighting: peak in the middle
		var w float64
		if n > 1 {
			d := float64(i) - float64(n-1)/2
			w = 1 - (2*abs(d))/float64(n)
		} else {
			w = 0.5
		}
		scores[i] = clamp01(0.2 + 0.5*w)
	}
	for _, h := range meta.Hints {
		if h.SegmentIndex >= 0 && h.SegmentIndex < n {
			scores[h.SegmentIndex] = clamp01(scores[h.SegmentIndex] + h.Score)
		}
	}
	story := fmt.Sprintf(
		"Automatic overview unavailable (no VLM configured). %d-frame macro collage received for %q, %.0fs long, split into %d segments.",
		len(meta.Frames), job.Filename, job.DurationSec, n,
	)
	return OverviewResult{Story: story, Scores: scores}, nil
}

func (p *NoopProvider) Transcribe(_ context.Context, _ *models.Job, _ string) (string, error) {
	return "", nil
}

func (p *NoopProvider) AnalyzeSegment(_ context.Context, _ *models.Job, seg *models.TimelineSegment, _ string) (string, error) {
	if len(seg.SceneChangeTimes) == 0 {
		return fmt.Sprintf("Segment %d (%.0fs-%.0fs): no scene cuts detected.", seg.Index, seg.StartSec, seg.EndSec), nil
	}
	return fmt.Sprintf("Segment %d (%.0fs-%.0fs): %d scene cut(s) detected.", seg.Index, seg.StartSec, seg.EndSec, len(seg.SceneChangeTimes)), nil
}

func (p *NoopProvider) Chat(_ context.Context, _ *models.Job, _ []models.ChatMessage, _, _ string) (string, error) {
	return "Chat needs a real VLM: set ANALYSIS_PROVIDER=litellm (and a working LITELLM_BASE_URL) to ask questions about this video.", nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
