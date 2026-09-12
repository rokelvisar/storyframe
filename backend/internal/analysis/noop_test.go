package analysis

import (
	"context"
	"testing"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

func TestNoopOverview_ScoresInRangeAndHintsApplied(t *testing.T) {
	p := &NoopProvider{}
	job := &models.Job{
		Filename:    "clip.mp4",
		DurationSec: 120,
		Segments: []*models.TimelineSegment{
			{Index: 0}, {Index: 1}, {Index: 2}, {Index: 3},
		},
	}
	meta := models.OverviewMeta{Frames: make([]models.FrameMeta, 16)}
	meta.Hints = append(meta.Hints, struct {
		SegmentIndex int     `json:"segmentIndex"`
		Score        float64 `json:"score"`
	}{SegmentIndex: 3, Score: 0.9})

	res, err := p.AnalyzeOverview(context.Background(), job, "", meta)
	if err != nil {
		t.Fatal(err)
	}
	if res.Story == "" {
		t.Error("expected a non-empty fallback story")
	}
	if len(res.Scores) != 4 {
		t.Fatalf("expected 4 scores, got %d", len(res.Scores))
	}
	for idx, sc := range res.Scores {
		if sc < 0 || sc > 1 {
			t.Errorf("segment %d score %v out of [0,1]", idx, sc)
		}
	}
	if res.Scores[3] <= res.Scores[0] {
		t.Errorf("hint on segment 3 should raise its score above segment 0 (%v vs %v)", res.Scores[3], res.Scores[0])
	}
}

func TestNoopTranscribeEmpty(t *testing.T) {
	got, err := (&NoopProvider{}).Transcribe(context.Background(), &models.Job{}, "x.webm")
	if err != nil || got != "" {
		t.Fatalf("want empty,nil got %q,%v", got, err)
	}
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("ANALYSIS_PROVIDER", "")
	t.Setenv("LITELLM_VLM_MODEL", "")
	t.Setenv("LITELLM_WHISPER_MODEL", "")
	t.Setenv("TRANSCRIBE_LANGUAGE_HINT", "")
	t.Setenv("OUTPUT_LANGUAGE", "")
	c := FromEnv()
	if c.ProviderName != "litellm" || len(c.WhisperModels) != 1 || c.WhisperModels[0] != "whisper-1" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.DefaultLanguageHint != "en,sl" || c.DefaultOutputLanguage != "sl" {
		t.Fatalf("unexpected language defaults: %+v", c)
	}
}

func TestLanguageName(t *testing.T) {
	if LanguageName("sl") != "Slovenian" || LanguageName("EN") != "English" {
		t.Fatalf("known codes not mapped")
	}
	if LanguageName(" fr ") != "fr" {
		t.Fatalf("unknown code should fall back to itself (trimmed): got %q", LanguageName(" fr "))
	}
}

func TestNoopChat(t *testing.T) {
	reply, err := (&NoopProvider{}).Chat(context.Background(), &models.Job{}, nil, "what happens?", "")
	if err != nil || reply == "" {
		t.Fatalf("want a non-empty canned reply, got %q, %v", reply, err)
	}
}

func TestSplitModels(t *testing.T) {
	got := splitModels(" a , b ,,c ")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("splitModels parsed wrong: %#v", got)
	}
	if len(splitModels("")) != 0 {
		t.Fatalf("empty string should yield no models")
	}
}
