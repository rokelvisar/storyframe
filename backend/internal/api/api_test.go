package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/engine"
	"github.com/rokelvisar/storyframe/backend/internal/models"
	"github.com/rokelvisar/storyframe/backend/internal/store"
	"github.com/rokelvisar/storyframe/backend/internal/analysis"
)

func TestBuildSegments_ByDuration(t *testing.T) {
	segs := buildSegments(models.CreateJobRequest{
		SizeBytes: 1000, DurationSec: 100, SegmentSeconds: 25,
	})
	if len(segs) != 4 {
		t.Fatalf("want 4 segments, got %d", len(segs))
	}
	if segs[0].ByteStart != 0 || segs[3].ByteEnd != 1000 {
		t.Fatalf("byte range not anchored: %+v ... %+v", segs[0], segs[3])
	}
	for i, s := range segs {
		if s.Index != i || s.Status != models.SegPending || s.Source != models.SourcePlan {
			t.Fatalf("segment %d wrong defaults: %+v", i, s)
		}
		if s.TotalBytes != s.ByteEnd-s.ByteStart {
			t.Fatalf("segment %d TotalBytes mismatch", i)
		}
	}
	if segs[1].ByteStart != segs[0].ByteEnd {
		t.Fatalf("segments must be contiguous in bytes")
	}
}

func TestBuildSegments_ClampsCount(t *testing.T) {
	segs := buildSegments(models.CreateJobRequest{SizeBytes: 1 << 30, DurationSec: 100000, SegmentSeconds: 1})
	if len(segs) > maxSegments {
		t.Fatalf("segment count %d exceeds cap %d", len(segs), maxSegments)
	}
}

func TestBuildSegments_ExplicitPlan(t *testing.T) {
	req := models.CreateJobRequest{SizeBytes: 900, DurationSec: 90}
	req.SegmentPlan = append(req.SegmentPlan, struct {
		StartSec float64 `json:"startSec"`
		EndSec   float64 `json:"endSec"`
	}{0, 30}, struct {
		StartSec float64 `json:"startSec"`
		EndSec   float64 `json:"endSec"`
	}{30, 90})
	segs := buildSegments(req)
	if len(segs) != 2 || segs[1].EndSec != 90 || segs[1].ByteEnd != 900 {
		t.Fatalf("explicit plan not honoured: %+v", segs)
	}
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	h, _ := newTestServerWithStore(t)
	return h
}

func newTestServerWithStore(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
	eng := engine.New(st, &analysis.NoopProvider{}, t.TempDir(), "ffmpeg", 1, nil, false, log)
	t.Cleanup(eng.Shutdown)
	h := NewRouter(Deps{Store: st, Engine: eng, Tus: http.NotFoundHandler(), DataDir: t.TempDir(), Log: log})
	return h, st
}

func TestCreateAndGetJob(t *testing.T) {
	h := newTestServer(t)

	body, _ := json.Marshal(models.CreateJobRequest{
		Filename: "movie.mp4", SizeBytes: 2_000_000_000, DurationSec: 600, SegmentSeconds: 60,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	var created models.CreateJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.TusEndpoint != "/files/" || created.Job == nil || len(created.Job.Segments) != 10 {
		t.Fatalf("unexpected create response: %+v", created)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+created.Job.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
	var got models.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ID != created.Job.ID || got.Status != models.JobCreated {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for unknown job, got %d", rec.Code)
	}
}

func TestListJobs_NewestFirst(t *testing.T) {
	h, st := newTestServerWithStore(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := st.CreateJob(&models.Job{ID: id, Filename: id + ".mp4", Status: models.JobCreated}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got models.ListJobsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Jobs) != 3 {
		t.Fatalf("want 3 jobs, got %d", len(got.Jobs))
	}
	if got.Jobs[0].ID != "c" || got.Jobs[2].ID != "a" {
		t.Fatalf("want newest-first order, got %+v", got.Jobs)
	}
}

func TestPostChat_RequiresOverviewFirst(t *testing.T) {
	h, _ := newTestServerWithStore(t)

	body, _ := json.Marshal(models.CreateJobRequest{Filename: "a.mp4", SizeBytes: 100, DurationSec: 10})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body)))
	var created models.CreateJobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = httptest.NewRecorder()
	chatBody, _ := json.Marshal(models.ChatRequest{Message: "what happens?"})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+created.Job.ID+"/chat", bytes.NewReader(chatBody)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 before overview is ready, got %d: %s", rec.Code, rec.Body)
	}
}

func TestPostChat_AppendsQuestionAndGetsAReply(t *testing.T) {
	h, st := newTestServerWithStore(t)

	body, _ := json.Marshal(models.CreateJobRequest{Filename: "a.mp4", SizeBytes: 100, DurationSec: 10})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body)))
	var created models.CreateJobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	if _, err := st.UpdateJob(created.Job.ID, func(j *models.Job) error {
		j.OverviewStory = "A cat chases a laser pointer."
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	chatBody, _ := json.Marshal(models.ChatRequest{Message: "what happens?"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+created.Job.ID+"/chat", bytes.NewReader(chatBody)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body)
	}
	var afterPost models.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &afterPost)
	if len(afterPost.ChatMessages) != 1 || afterPost.ChatMessages[0].Role != models.ChatUser {
		t.Fatalf("expected the question persisted synchronously: %+v", afterPost.ChatMessages)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final models.Job
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+created.Job.ID, nil))
		_ = json.Unmarshal(rec.Body.Bytes(), &final)
		if len(final.ChatMessages) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(final.ChatMessages) != 2 || final.ChatMessages[1].Role != models.ChatAssistant || final.ChatMessages[1].Content == "" {
		t.Fatalf("expected an assistant reply to land async: %+v", final.ChatMessages)
	}

	// empty message rejected
	rec = httptest.NewRecorder()
	badBody, _ := json.Marshal(models.ChatRequest{Message: "   "})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+created.Job.ID+"/chat", bytes.NewReader(badBody)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a blank message, got %d", rec.Code)
	}
}

func TestCreateJob_LanguageDefaultsAndOverride(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
	eng := engine.New(st, &analysis.NoopProvider{}, t.TempDir(), "ffmpeg", 1, nil, false, log)
	t.Cleanup(eng.Shutdown)
	h := NewRouter(Deps{
		Store: st, Engine: eng, Tus: http.NotFoundHandler(), DataDir: t.TempDir(), Log: log,
		DefaultLanguageHint: "en,sl", DefaultOutputLanguage: "sl",
	})

	body, _ := json.Marshal(models.CreateJobRequest{Filename: "a.mp4", SizeBytes: 100, DurationSec: 10})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body)))
	var created models.CreateJobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Job.TranscribeLanguageHint != "en,sl" || created.Job.OutputLanguage != "sl" {
		t.Fatalf("expected server defaults applied: %+v", created.Job)
	}

	body, _ = json.Marshal(models.CreateJobRequest{
		Filename: "b.mp4", SizeBytes: 100, DurationSec: 10,
		LanguageHint: "de", OutputLanguage: "en",
	})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body)))
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Job.TranscribeLanguageHint != "de" || created.Job.OutputLanguage != "en" {
		t.Fatalf("expected per-request override to win: %+v", created.Job)
	}
}

func TestHealthz(t *testing.T) {
	h := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz %d", rec.Code)
	}
}
