package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/analysis"
	"github.com/rokelvisar/storyframe/backend/internal/immich"
	"github.com/rokelvisar/storyframe/backend/internal/models"
	"github.com/rokelvisar/storyframe/backend/internal/store"
)

// TestIngestFromImmich_EndToEnd drives the whole Immich-origin path against a
// fake Immich server and real ffmpeg/ffprobe: resolve -> download -> build the
// macro collage + audio server-side -> unlock every segment -> job complete.
// No browser or tus upload is involved anywhere in this path.
func TestIngestFromImmich_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	dir := t.TempDir()
	sample := filepath.Join(dir, "sample.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:d=12",
		"-f", "lavfi", "-i", "sine=f=440:d=12",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart",
		sample,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, out)
	}
	info, err := os.Stat(sample)
	if err != nil {
		t.Fatal(err)
	}

	var writeback struct {
		mu          sync.Mutex
		description string
		metadata    map[string]any
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/assets/vid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key on %s /api/assets/vid-1", r.Method)
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"vid-1","type":"VIDEO","originalFileName":"sample.mp4","originalMimeType":"video/mp4","duration":12000,"exifInfo":{"fileSizeInByte":` +
				strconv.FormatInt(info.Size(), 10) + `}}`))
		case http.MethodPut:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeback.mu.Lock()
			writeback.description = body["description"]
			writeback.mu.Unlock()
			w.Write([]byte(`{}`))
		}
	})
	mux.HandleFunc("/api/assets/vid-1/original", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key on download call")
		}
		http.ServeFile(w, r, sample)
	})
	mux.HandleFunc("/api/assets/vid-1/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items []struct {
				Key   string         `json:"key"`
				Value map[string]any `json:"value"`
			} `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeback.mu.Lock()
		if len(body.Items) == 1 {
			writeback.metadata = body.Items[0].Value
		}
		writeback.mu.Unlock()
		w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	log := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
	immichClient := immich.New(immich.Config{BaseURL: srv.URL, APIKey: "test-key"})
	dataDir := t.TempDir()
	eng := New(st, &analysis.NoopProvider{}, dataDir, "ffmpeg", 2, immichClient, true, log) // writeback ON
	t.Cleanup(eng.Shutdown)

	asset, err := immichClient.ResolveAsset(context.Background(), "vid-1")
	if err != nil {
		t.Fatal(err)
	}
	if asset.DurationSec != 12 {
		t.Fatalf("duration: got %v", asset.DurationSec)
	}

	job := &models.Job{
		ID: "job-immich-1", Status: models.JobCreated, Origin: models.OriginImmich,
		Filename: asset.OriginalFileName, SizeBytes: asset.SizeBytes, DurationSec: asset.DurationSec,
		ImmichAssetID: asset.ID, ImmichAutoWriteback: true,
		Segments: []*models.TimelineSegment{
			{ID: "s0", JobID: "job-immich-1", Index: 0, StartSec: 0, EndSec: 6, Status: models.SegPending, Source: models.SourcePlan, ByteStart: 0, ByteEnd: asset.SizeBytes / 2},
			{ID: "s1", JobID: "job-immich-1", Index: 1, StartSec: 6, EndSec: 12, Status: models.SegPending, Source: models.SourcePlan, ByteStart: asset.SizeBytes / 2, ByteEnd: asset.SizeBytes},
		},
	}
	if err := st.CreateJob(job); err != nil {
		t.Fatal(err)
	}

	eng.IngestFromImmich(job.ID, asset)

	deadline := time.Now().Add(60 * time.Second)
	var final *models.Job
	for time.Now().Before(deadline) {
		got, err := st.GetJob(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == models.JobComplete || got.Status == models.JobError {
			final = got
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if final == nil {
		got, _ := st.GetJob(job.ID)
		t.Fatalf("job did not finish in time, last state: %+v", got)
	}
	if final.Status != models.JobComplete {
		t.Fatalf("job ended in status %q, error=%q", final.Status, final.Error)
	}
	if final.OverviewCollage == "" {
		t.Error("expected a server-built overview collage URL")
	}
	if final.OverviewStory == "" {
		t.Error("expected a (noop-fallback) overview story")
	}
	if !final.AudioReceived {
		t.Error("expected audio to have been extracted and submitted")
	}
	// origin=immich: the raw video is not kept locally (the original lives in
	// Immich, Job.ImmichAssetID), so no local archive is offered...
	if final.ArchiveURL != "" {
		t.Errorf("expected no local archive for an immich-origin job, got %q", final.ArchiveURL)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "immich", job.ID)); !os.IsNotExist(err) {
		t.Errorf("expected the immich download-staging dir to be cleaned up, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "assembled", job.ID)); !os.IsNotExist(err) {
		t.Errorf("expected the assembled raw-video dir to be cleaned up, stat err = %v", err)
	}
	// ...but every derived artifact the UI needs is still there.
	if _, err := os.Stat(filepath.Join(dataDir, "overview", job.ID, "collage.jpg")); err != nil {
		t.Errorf("overview collage should survive cleanup: %v", err)
	}
	for _, seg := range final.Segments {
		if seg.Status != models.SegDone {
			t.Errorf("segment %s not done: %+v", seg.ID, seg)
		}
		if seg.GranularCollage == "" {
			t.Errorf("segment %s missing granular collage", seg.ID)
		}
	}

	// write-back runs in its own goroutine after maybeComplete; give it a
	// moment to reach the fake Immich server.
	wbDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(wbDeadline) {
		writeback.mu.Lock()
		got := writeback.description
		writeback.mu.Unlock()
		if got != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	writeback.mu.Lock()
	defer writeback.mu.Unlock()
	if writeback.description == "" || !strings.Contains(writeback.description, "video-extractor AI summary") {
		t.Errorf("expected the story written back to the asset's Immich description, got %q", writeback.description)
	}
	if writeback.metadata == nil {
		t.Fatal("expected structured analysis written to the asset's custom metadata")
	}
	if writeback.metadata["jobId"] != job.ID {
		t.Errorf("metadata jobId: got %v", writeback.metadata["jobId"])
	}
	if segs, _ := writeback.metadata["segments"].([]any); len(segs) != 2 {
		t.Errorf("expected 2 segments in the written-back metadata, got %+v", writeback.metadata["segments"])
	}

	// The activity log should carry a human-readable trail of the whole run:
	// download, collage/audio build, per-segment analysis, completion, write-back.
	withEvents, err := st.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !withEvents.ImmichWrittenBack {
		t.Error("expected ImmichWrittenBack to be true after a successful write-back")
	}
	joined := ""
	for _, ev := range withEvents.Events {
		joined += ev.Message + "\n"
	}
	for _, want := range []string{"downloading asset", "download complete", "building macro collage", "extracting audio track", "job complete", "write-back complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected an event containing %q; got:\n%s", want, joined)
		}
	}
}
