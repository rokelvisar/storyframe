package engine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/analysis"
	"github.com/rokelvisar/storyframe/backend/internal/immich"
	"github.com/rokelvisar/storyframe/backend/internal/models"
	"github.com/rokelvisar/storyframe/backend/internal/store"
)

func TestBuildDescription_NoExisting(t *testing.T) {
	when := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	got := buildDescription("", "job-1", when, "A story about a cat.")
	if !strings.Contains(got, "A story about a cat.") {
		t.Fatalf("story missing: %s", got)
	}
	if !strings.Contains(got, "job-1") || !strings.Contains(got, "2026-09-12") {
		t.Fatalf("job id / date missing: %s", got)
	}
	if !strings.HasPrefix(got, writebackMarkerStart) {
		t.Fatalf("expected the block to start at the beginning when there's no prior text: %s", got)
	}
}

func TestBuildDescription_PreservesUserText(t *testing.T) {
	when := time.Now()
	got := buildDescription("My own caption, written by hand.", "job-1", when, "AI story.")
	if !strings.HasPrefix(got, "My own caption, written by hand.") {
		t.Fatalf("user's own text must come first, unmodified: %s", got)
	}
	if !strings.Contains(got, "AI story.") {
		t.Fatalf("AI story missing: %s", got)
	}
}

func TestBuildDescription_ReplacesPriorSummaryInPlace(t *testing.T) {
	when := time.Now()
	first := buildDescription("User note.", "job-1", when, "First analysis.")
	second := buildDescription(first, "job-2", when, "Second, updated analysis.")

	if strings.Contains(second, "First analysis.") {
		t.Fatalf("stale summary should have been replaced, not appended: %s", second)
	}
	if strings.Count(second, writebackMarkerStart) != 1 {
		t.Fatalf("expected exactly one video-extractor block after re-analysis, got:\n%s", second)
	}
	if !strings.HasPrefix(second, "User note.") {
		t.Fatalf("user's own note must survive re-analysis: %s", second)
	}
	if !strings.Contains(second, "Second, updated analysis.") || !strings.Contains(second, "job-2") {
		t.Fatalf("new summary not present: %s", second)
	}
}

func TestStripPriorSummary_NoMarkerIsNoop(t *testing.T) {
	if got := stripPriorSummary("  just some text  "); got != "just some text" {
		t.Fatalf("got %q", got)
	}
}

func TestStripPriorSummary_UnterminatedBlockDropsFromStart(t *testing.T) {
	s := "before text\n\n" + writebackMarkerStart + " (job x) ---\nunterminated..."
	got := stripPriorSummary(s)
	if got != "before text" {
		t.Fatalf("expected everything from the marker onward dropped, got %q", got)
	}
}

func TestBuildMetadataPayload(t *testing.T) {
	when := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	job := &models.Job{
		ID: "job-1", AnalysisProvider: "litellm", OutputLanguage: "sl",
		OverviewStory: "story", Transcript: "hello",
		Segments: []*models.TimelineSegment{
			{StartSec: 0, EndSec: 30, InterestScore: 0.8, Description: "seg 1"},
			{StartSec: 30, EndSec: 60, InterestScore: 0.3, Description: "seg 2"},
		},
	}
	p := buildMetadataPayload(job, when)
	if p.JobID != "job-1" || p.OutputLanguage != "sl" || p.Story != "story" || p.Transcript != "hello" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.AnalyzedAt != "2026-09-12T10:30:00Z" {
		t.Fatalf("analyzedAt: got %q", p.AnalyzedAt)
	}
	if len(p.Segments) != 2 || p.Segments[0].Description != "seg 1" || p.Segments[1].InterestScore != 0.3 {
		t.Fatalf("segments not carried through: %+v", p.Segments)
	}
}

// newWritebackTestEngine wires an Engine against a fake Immich server that
// records description/metadata PUTs, without running the full FFmpeg
// pipeline — these tests only exercise the write-back gating logic.
func newWritebackTestEngine(t *testing.T) (*Engine, *store.Store, *struct {
	mu          sync.Mutex
	description string
}) {
	t.Helper()
	writeback := &struct {
		mu          sync.Mutex
		description string
	}{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/assets/vid-1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"vid-1","type":"VIDEO","originalFileName":"x.mp4"}`))
		case http.MethodPut:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeback.mu.Lock()
			writeback.description = body["description"]
			writeback.mu.Unlock()
			w.Write([]byte(`{}`))
		}
	})
	mux.HandleFunc("/api/assets/vid-1/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	log := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
	immichClient := immich.New(immich.Config{BaseURL: srv.URL, APIKey: "test-key"})
	e := New(st, &analysis.NoopProvider{}, t.TempDir(), "ffmpeg", 1, immichClient, true /* master switch on */, log)
	t.Cleanup(e.Shutdown)
	return e, st, writeback
}

func waitForDescription(t *testing.T, writeback *struct {
	mu          sync.Mutex
	description string
}) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		writeback.mu.Lock()
		got := writeback.description
		writeback.mu.Unlock()
		if got != "" {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ""
}

func TestMaybeComplete_AutoWritebackOnlyWhenJobOptedIn(t *testing.T) {
	e, st, writeback := newWritebackTestEngine(t)

	job := &models.Job{
		ID: "j-no-auto", Status: models.JobCreated, Origin: models.OriginImmich,
		ImmichAssetID: "vid-1", ImmichAutoWriteback: false,
		OverviewStory: "a story",
		Segments: []*models.TimelineSegment{
			{ID: "s0", JobID: "j-no-auto", Status: models.SegDone},
		},
	}
	if err := st.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(t.TempDir(), "source.mp4")
	e.mu.Lock()
	e.full[job.ID] = full
	e.mu.Unlock()

	e.maybeComplete(job.ID)

	got, err := st.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.JobComplete {
		t.Fatalf("expected job complete, got %q", got.Status)
	}
	if got.ImmichWrittenBack {
		t.Error("expected no automatic write-back when ImmichAutoWriteback is false")
	}
	writeback.mu.Lock()
	desc := writeback.description
	writeback.mu.Unlock()
	if desc != "" {
		t.Errorf("expected no description PUT to the fake Immich server, got %q", desc)
	}
}

func TestMaybeComplete_AutoWritebackWhenJobOptedIn(t *testing.T) {
	e, st, writeback := newWritebackTestEngine(t)

	job := &models.Job{
		ID: "j-auto", Status: models.JobCreated, Origin: models.OriginImmich,
		ImmichAssetID: "vid-1", ImmichAutoWriteback: true,
		OverviewStory: "a story",
		Segments: []*models.TimelineSegment{
			{ID: "s0", JobID: "j-auto", Status: models.SegDone},
		},
	}
	if err := st.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.full[job.ID] = filepath.Join(t.TempDir(), "source.mp4")
	e.mu.Unlock()

	e.maybeComplete(job.ID)

	if got := waitForDescription(t, writeback); got == "" {
		t.Fatal("expected an automatic write-back to have PUT a description")
	}
}

func TestTriggerImmichWriteback_ManualWorksRegardlessOfAutoFlag(t *testing.T) {
	e, st, writeback := newWritebackTestEngine(t)

	job := &models.Job{
		ID: "j-manual", Status: models.JobComplete, Origin: models.OriginImmich,
		ImmichAssetID: "vid-1", ImmichAutoWriteback: false,
		OverviewStory: "a story",
	}
	if err := st.CreateJob(job); err != nil {
		t.Fatal(err)
	}

	if err := e.TriggerImmichWriteback(job.ID); err != nil {
		t.Fatalf("TriggerImmichWriteback: %v", err)
	}
	if got := waitForDescription(t, writeback); got == "" {
		t.Fatal("expected a manually triggered write-back to have PUT a description")
	}
}

func TestTriggerImmichWriteback_RejectsNonImmichOrigin(t *testing.T) {
	e, st, _ := newWritebackTestEngine(t)

	job := &models.Job{ID: "j-upload", Status: models.JobComplete, Origin: models.OriginUpload}
	if err := st.CreateJob(job); err != nil {
		t.Fatal(err)
	}

	if err := e.TriggerImmichWriteback(job.ID); err == nil {
		t.Fatal("expected an error for a non-immich-origin job")
	}
}
