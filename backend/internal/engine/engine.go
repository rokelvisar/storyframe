// Package engine orchestrates the asynchronous stages of a job: it consumes tus
// upload-completion events, keeps a per-job registry of received byte ranges,
// drives the bounded FFmpeg worker pool for per-segment granular contact sheets and
// calls the analysis provider for the story, transcript and segment descriptions.
package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/analysis"
	"github.com/rokelvisar/storyframe/backend/internal/ffmpeg"
	"github.com/rokelvisar/storyframe/backend/internal/immich"
	"github.com/rokelvisar/storyframe/backend/internal/models"
	"github.com/rokelvisar/storyframe/backend/internal/store"
)

// AutoInterestThreshold: segments the VLM scores at or above this are queued for
// granular analysis automatically as soon as their bytes arrive.
const AutoInterestThreshold = 0.6

type partInfo struct {
	index int
	path  string
	size  int64
}

type granularTask struct {
	jobID     string
	segmentID string
}

// Engine is safe for concurrent use.
type Engine struct {
	store           *store.Store
	provider        analysis.Provider
	log             *slog.Logger
	dataDir         string
	ffmpegBin       string
	immich          *immich.Client // nil when Immich import isn't configured
	immichWriteback bool           // write the analysis back to the source Immich asset when done

	tasks chan granularTask
	wg    sync.WaitGroup

	mu    sync.Mutex
	parts map[string]map[int]partInfo // jobID -> index -> part
	full  map[string]string           // jobID -> assembled full-file path
	done  map[string]bool             // jobID -> completion emitted
}

// New starts `workers` FFmpeg workers. immichClient may be nil when Immich
// import isn't configured (IngestFromImmich then fails fast with a clear error).
// immichWriteback additionally writes the finished analysis back onto the
// source Immich asset (description + custom metadata) when true.
func New(st *store.Store, p analysis.Provider, dataDir, ffmpegBin string, workers int, immichClient *immich.Client, immichWriteback bool, log *slog.Logger) *Engine {
	if workers <= 0 {
		workers = 2
	}
	e := &Engine{
		store:           st,
		provider:        p,
		log:             log,
		dataDir:         dataDir,
		ffmpegBin:       ffmpegBin,
		immich:          immichClient,
		immichWriteback: immichWriteback,
		tasks:           make(chan granularTask, 256),
		parts:           map[string]map[int]partInfo{},
		full:            map[string]string{},
		done:            map[string]bool{},
	}
	for i := 0; i < workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}
	return e
}

// Shutdown drains the worker pool.
func (e *Engine) Shutdown() {
	close(e.tasks)
	e.wg.Wait()
}

func (e *Engine) dir(parts ...string) string {
	return filepath.Join(append([]string{e.dataDir}, parts...)...)
}

// maxJobEvents bounds each job's activity log so a long-running job with many
// segments and fallback retries can't grow the SQLite JSON blob unboundedly;
// oldest entries are dropped first.
const maxJobEvents = 300

// LogEvent appends one line to jobID's activity log (surfaced in the UI's
// event log panel). Best-effort: a persist failure is only logged server-side,
// never propagated, since this must never be what fails a pipeline step.
func (e *Engine) LogEvent(jobID string, level models.EventLevel, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	_, err := e.store.UpdateJob(jobID, func(j *models.Job) error {
		j.Events = append(j.Events, models.JobEvent{Time: time.Now().UTC(), Level: level, Message: msg})
		if n := len(j.Events); n > maxJobEvents {
			j.Events = j.Events[n-maxJobEvents:]
		}
		return nil
	})
	if err != nil {
		e.log.Warn("log event: persist failed", "job", jobID, "err", err)
	}
}

// withEvents attaches jobID's event log to ctx so anything the analysis
// provider does under it (model fallback attempts) is captured too.
func (e *Engine) withEvents(ctx context.Context, jobID string) context.Context {
	return analysis.WithEvents(ctx, func(level, msg string) {
		e.LogEvent(jobID, models.EventLevel(level), "%s", msg)
	})
}

// --- Stage 1 & 2 -------------------------------------------------------------

// SubmitOverview runs the VLM overview asynchronously.
func (e *Engine) SubmitOverview(jobID, collagePath string, meta models.OverviewMeta) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		ctx = e.withEvents(ctx, jobID)

		job, err := e.store.GetJob(jobID)
		if err != nil {
			e.log.Error("overview: load job", "job", jobID, "err", err)
			return
		}
		e.LogEvent(jobID, models.EventInfo, "overview: analyzing macro collage (provider=%s)", e.provider.Name())
		res, err := e.provider.AnalyzeOverview(ctx, job, collagePath, meta)
		if err != nil {
			e.log.Error("overview: provider", "job", jobID, "err", err)
			e.LogEvent(jobID, models.EventError, "overview: analysis failed: %v", err)
			_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
				j.Error = "overview analysis failed: " + err.Error()
				return nil
			})
			return
		}
		_, err = e.store.UpdateJob(jobID, func(j *models.Job) error {
			j.OverviewStory = res.Story
			j.AnalysisProvider = e.provider.Name()
			for _, seg := range j.Segments {
				sc, ok := res.Scores[seg.Index]
				if !ok {
					continue
				}
				// A user's explicit pick outranks a later-arriving VLM score:
				// keep whichever is higher and don't demote their flag.
				if seg.Source == models.SourceUser {
					if sc > seg.InterestScore {
						seg.InterestScore = sc
					}
				} else {
					seg.InterestScore = sc
					seg.Source = models.SourceAI
				}
				seg.UpdatedAt = time.Now().UTC()
			}
			if j.Status == models.JobCreated || j.Status == models.JobOverviewReceived {
				j.Status = models.JobAnalyzing
			}
			return nil
		})
		if err != nil {
			e.log.Error("overview: persist", "job", jobID, "err", err)
			return
		}
		e.log.Info("overview complete", "job", jobID, "provider", e.provider.Name())
		e.LogEvent(jobID, models.EventInfo, "overview: story ready (%d chars), %d segments scored", len(res.Story), len(res.Scores))
		e.tryAutoQueue(jobID)
	}()
}

// SubmitAudio runs transcription asynchronously.
func (e *Engine) SubmitAudio(jobID, audioPath string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		ctx = e.withEvents(ctx, jobID)

		job, err := e.store.GetJob(jobID)
		if err != nil {
			e.log.Error("audio: load job", "job", jobID, "err", err)
			return
		}
		e.LogEvent(jobID, models.EventInfo, "audio: transcribing (provider=%s)", e.provider.Name())
		text, err := e.provider.Transcribe(ctx, job, audioPath)
		if err != nil {
			e.log.Error("audio: provider", "job", jobID, "err", err)
			e.LogEvent(jobID, models.EventError, "audio: transcription failed: %v", err)
			_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
				j.Error = "transcription failed: " + err.Error()
				return nil
			})
			return
		}
		_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
			j.Transcript = text
			j.AudioReceived = true
			return nil
		})
		e.log.Info("audio transcription complete", "job", jobID, "chars", len(text))
		if text == "" {
			e.LogEvent(jobID, models.EventInfo, "audio: transcription complete, no speech detected")
		} else {
			e.LogEvent(jobID, models.EventInfo, "audio: transcript ready (%d chars)", len(text))
		}
	}()
}

// SubmitChat answers one chat turn asynchronously. The caller (the HTTP
// handler) must already have appended the user's question to
// Job.ChatMessages via store.UpdateJob before calling this, mirroring
// SubmitOverview/SubmitAudio's split-responsibility pattern; SubmitChat reads
// it back off the job and treats everything before it as prior history.
func (e *Engine) SubmitChat(jobID, question string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ctx = e.withEvents(ctx, jobID)

		job, err := e.store.GetJob(jobID)
		if err != nil {
			e.log.Error("chat: load job", "job", jobID, "err", err)
			return
		}
		history := job.ChatMessages
		if n := len(history); n > 0 {
			history = history[:n-1] // drop the question itself; it's passed separately
		}
		collagePath := e.dir("overview", jobID, "collage.jpg")

		e.LogEvent(jobID, models.EventInfo, "chat: answering (provider=%s)", e.provider.Name())
		reply, err := e.provider.Chat(ctx, job, history, question, collagePath)
		if err != nil {
			e.log.Error("chat: provider", "job", jobID, "err", err)
			e.LogEvent(jobID, models.EventError, "chat: failed: %v", err)
			_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
				j.Error = "chat failed: " + err.Error()
				return nil
			})
			return
		}
		_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
			j.ChatMessages = append(j.ChatMessages, models.ChatMessage{
				Role: models.ChatAssistant, Content: reply, CreatedAt: time.Now().UTC(),
			})
			return nil
		})
		e.log.Info("chat reply ready", "job", jobID, "chars", len(reply))
		e.LogEvent(jobID, models.EventInfo, "chat: reply ready (%d chars)", len(reply))
	}()
}

// IngestFromImmich pulls a resolved Immich asset's bytes onto local disk, then
// builds Stage 1 (macro collage) and Stage 2 (audio) itself with FFmpeg — no
// browser is involved for an Immich-sourced job, since Immich sends no CORS
// headers and the whole file already needs to pass through this backend once
// anyway. Once downloaded, this converges on the exact same path a completed
// tus upload takes (OnFullUploaded), so segment granular analysis, the
// timeline UI and job completion all behave identically regardless of origin.
func (e *Engine) IngestFromImmich(jobID string, asset immich.Asset) {
	go func() {
		if e.immich == nil {
			e.failJob(jobID, "immich: not configured (IMMICH_BASE_URL/IMMICH_API_KEY)")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
			j.Status = models.JobDownloading
			return nil
		})

		tmpDir := e.dir("immich", jobID)
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			e.failJob(jobID, "immich: "+err.Error())
			return
		}
		src := filepath.Join(tmpDir, "source"+extForAsset(asset))
		e.log.Info("immich: downloading", "job", jobID, "asset", asset.ID, "sizeBytes", asset.SizeBytes)
		e.LogEvent(jobID, models.EventInfo, "immich: downloading asset %s (%.0f MB)", asset.ID, float64(asset.SizeBytes)/1e6)
		if err := e.immich.Download(ctx, asset, src); err != nil {
			e.LogEvent(jobID, models.EventError, "immich: download failed: %v", err)
			e.failJob(jobID, "immich download failed: "+err.Error())
			return
		}
		e.LogEvent(jobID, models.EventInfo, "immich: download complete")

		job, err := e.store.GetJob(jobID)
		if err != nil {
			e.log.Error("immich: reload job", "job", jobID, "err", err)
			return
		}

		aspect := 16.0 / 9.0
		if w, h, perr := ffmpeg.ProbeDimensions(ctx, e.ffmpegBin, src); perr == nil && h > 0 {
			aspect = float64(w) / float64(h)
		} else if perr != nil {
			e.log.Warn("immich: probe dimensions failed, defaulting to 16:9", "job", jobID, "err", perr)
		}

		overviewDir := filepath.Join(e.dataDir, "overview", jobID)
		if err := os.MkdirAll(overviewDir, 0o755); err != nil {
			e.log.Error("immich: overview dir", "job", jobID, "err", err)
		} else {
			collagePath := filepath.Join(overviewDir, "collage.jpg")
			meta, fps := planOverviewFrames(job.DurationSec, 20, 320, aspect)
			e.LogEvent(jobID, models.EventInfo, "immich: building macro collage server-side")
			_, oerr := ffmpeg.RunOverview(ctx, ffmpeg.GranularOptions{
				InputPath: src, OutputPath: collagePath,
				StartSec: 0, EndSec: job.DurationSec,
				Cols: meta.Cols, Rows: meta.Rows, CellWidth: meta.CellWidth,
				FPSFallback: fps, Bin: e.ffmpegBin,
			})
			if oerr != nil {
				e.log.Error("immich: build overview collage", "job", jobID, "err", oerr)
				e.LogEvent(jobID, models.EventError, "immich: build overview collage failed: %v", oerr)
			} else {
				_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
					j.Frames = meta.Frames
					j.OverviewCollage = "/media/overview/" + jobID + "/collage.jpg"
					if j.Status == models.JobCreated || j.Status == models.JobDownloading {
						j.Status = models.JobOverviewReceived
					}
					return nil
				})
				e.SubmitOverview(jobID, collagePath, meta)
			}
		}

		audioDir := filepath.Join(e.dataDir, "audio", jobID)
		if err := os.MkdirAll(audioDir, 0o755); err != nil {
			e.log.Error("immich: audio dir", "job", jobID, "err", err)
		} else {
			audioPath := filepath.Join(audioDir, "audio.webm")
			e.LogEvent(jobID, models.EventInfo, "immich: extracting audio track server-side")
			if aerr := ffmpeg.ExtractAudio(ctx, e.ffmpegBin, src, audioPath); aerr != nil {
				e.log.Warn("immich: extract audio failed (continuing without transcript)", "job", jobID, "err", aerr)
				e.LogEvent(jobID, models.EventWarn, "immich: extract audio failed (continuing without transcript): %v", aerr)
			} else {
				_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
					if j.Status == models.JobCreated || j.Status == models.JobOverviewReceived || j.Status == models.JobDownloading {
						j.Status = models.JobAudioReceived
					}
					return nil
				})
				e.SubmitAudio(jobID, audioPath)
			}
		}

		e.log.Info("immich: source ready, unlocking segments", "job", jobID)
		e.LogEvent(jobID, models.EventInfo, "immich: source ready, unlocking segment analysis")
		e.OnFullUploaded(jobID, src)
	}()
}

func (e *Engine) failJob(jobID, msg string) {
	e.log.Error("job failed", "job", jobID, "err", msg)
	e.LogEvent(jobID, models.EventError, "job failed: %s", msg)
	_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
		j.Status = models.JobError
		j.Error = msg
		return nil
	})
}

func extForAsset(a immich.Asset) string {
	if i := strings.LastIndex(a.OriginalFileName, "."); i >= 0 {
		return strings.ToLower(a.OriginalFileName[i:])
	}
	switch a.OriginalMimeType {
	case "video/quicktime":
		return ".mov"
	case "video/mp4":
		return ".mp4"
	default:
		return ".mp4"
	}
}

// planOverviewFrames computes the same near-square grid + evenly spaced frame
// timestamps the browser's frontend/src/app/core/collage-plan.ts uses, so a
// server-built macro collage carries an equivalent FrameMeta[] grid for the VLM
// prompt. Timestamps land at k*(duration/count) for k in [0,count) — what
// ffmpeg's fps=count/duration filter samples — rather than collage-plan.ts's
// slice-center + edge-skip convention; the difference is immaterial for VLM
// framing context.
func planOverviewFrames(durationSec float64, targetFrames, cellWidth int, aspect float64) (models.OverviewMeta, float64) {
	if durationSec <= 0 {
		durationSec = 1
	}
	if aspect <= 0 {
		aspect = 16.0 / 9.0
	}
	count := targetFrames
	cols := int(math.Ceil(math.Sqrt(float64(count))))
	rows := int(math.Ceil(float64(count) / float64(cols)))
	cellHeight := int(math.Round(float64(cellWidth) / aspect))

	frames := make([]models.FrameMeta, 0, count)
	for i := 0; i < count; i++ {
		t := durationSec / float64(count) * float64(i)
		row, col := i/cols, i%cols
		frames = append(frames, models.FrameMeta{
			T: t, Row: row, Col: col,
			X: col * cellWidth, Y: row * cellHeight, W: cellWidth, H: cellHeight,
			Label: formatTimecode(t),
		})
	}
	return models.OverviewMeta{Cols: cols, Rows: rows, CellWidth: cellWidth, CellHeight: cellHeight, Frames: frames},
		float64(count) / durationSec
}

func formatTimecode(totalSeconds float64) string {
	s := int64(totalSeconds)
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}

// --- Stage 3: tus completion ----------------------------------------------------

// OnPartUploaded is called when a per-segment (partial) upload finishes.
func (e *Engine) OnPartUploaded(jobID, segmentID string, index int, srcPath string, size int64) {
	dst := e.dir("parts", jobID)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		e.log.Error("part: mkdir", "err", err)
		return
	}
	partPath := filepath.Join(dst, fmt.Sprintf("%06d.part", index))
	if err := copyFile(srcPath, partPath); err != nil {
		e.log.Error("part: copy", "err", err)
		return
	}

	e.mu.Lock()
	if e.parts[jobID] == nil {
		e.parts[jobID] = map[int]partInfo{}
	}
	e.parts[jobID][index] = partInfo{index: index, path: partPath, size: size}
	e.mu.Unlock()

	_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
		if seg := store.FindSegment(j, segmentID); seg != nil {
			seg.UploadedBytes = size
			if seg.Status == models.SegPending || seg.Status == models.SegUploading {
				seg.Status = models.SegUploaded
			}
			seg.UpdatedAt = time.Now().UTC()
		}
		return nil
	})
	e.log.Info("part uploaded", "job", jobID, "segment", segmentID, "index", index, "size", size)
	e.tryAutoQueue(jobID)
	e.maybeAssembleFull(jobID)
}

// OnFullUploaded is called when a whole-file (or final concatenated) upload
// finishes. Every remaining segment becomes processable.
func (e *Engine) OnFullUploaded(jobID, srcPath string) {
	dst := e.dir("assembled", jobID)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		e.log.Error("full: mkdir", "err", err)
		return
	}
	full := filepath.Join(dst, "source"+filepath.Ext(srcPath))
	if full == "" || filepath.Ext(srcPath) == "" {
		full = filepath.Join(dst, "source.mp4")
	}
	if err := copyFile(srcPath, full); err != nil {
		e.log.Error("full: copy", "err", err)
		return
	}
	e.mu.Lock()
	e.full[jobID] = full
	e.mu.Unlock()

	job, err := e.store.GetJob(jobID)
	if err != nil {
		return
	}
	for _, seg := range job.Segments {
		if seg.Status != models.SegDone && seg.Status != models.SegProcessing {
			e.enqueue(jobID, seg.ID)
		}
	}
	e.log.Info("full upload received", "job", jobID, "path", full)
	e.LogEvent(jobID, models.EventInfo, "source video ready, unlocking segment analysis")
}

// RequestSegment marks a segment interesting and queues it if its bytes are here.
func (e *Engine) RequestSegment(jobID, segmentID string) {
	e.enqueue(jobID, segmentID)
}

// maybeAssembleFull builds the full source once every segment's part is present.
func (e *Engine) maybeAssembleFull(jobID string) {
	job, err := e.store.GetJob(jobID)
	if err != nil {
		return
	}
	e.mu.Lock()
	have := len(e.parts[jobID])
	_, haveFull := e.full[jobID]
	e.mu.Unlock()
	if haveFull || have < len(job.Segments) || len(job.Segments) == 0 {
		return
	}
	full := e.dir("assembled", jobID, "source.mp4")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return
	}
	if err := e.concatParts(jobID, full, have); err != nil {
		e.log.Error("assemble full", "job", jobID, "err", err)
		return
	}
	e.mu.Lock()
	e.full[jobID] = full
	e.mu.Unlock()
	for _, seg := range job.Segments {
		if seg.Status != models.SegDone {
			e.enqueue(jobID, seg.ID)
		}
	}
}

// concatParts writes parts [0,count) in order into dst (raw byte concatenation:
// the client slices the original container linearly, so this reproduces it).
func (e *Engine) concatParts(jobID, dst string, count int) error {
	e.mu.Lock()
	parts := make([]partInfo, 0, count)
	for i := 0; i < count; i++ {
		p, ok := e.parts[jobID][i]
		if !ok {
			e.mu.Unlock()
			return fmt.Errorf("missing part %d", i)
		}
		parts = append(parts, p)
	}
	e.mu.Unlock()
	sort.Slice(parts, func(a, b int) bool { return parts[a].index < parts[b].index })

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, p := range parts {
		in, err := os.Open(p.path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			return err
		}
		in.Close()
	}
	return nil
}

// contiguousBytes returns how many bytes from offset 0 are available as an
// unbroken run of parts.
func (e *Engine) contiguousBytes(jobID string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var total int64
	for i := 0; ; i++ {
		p, ok := e.parts[jobID][i]
		if !ok {
			break
		}
		total += p.size
	}
	return total
}

// tryAutoQueue enqueues segments the VLM flagged as interesting whose bytes are in.
func (e *Engine) tryAutoQueue(jobID string) {
	job, err := e.store.GetJob(jobID)
	if err != nil {
		return
	}
	contig := e.contiguousBytes(jobID)
	e.mu.Lock()
	_, haveFull := e.full[jobID]
	e.mu.Unlock()

	// higher interest first
	segs := append([]*models.TimelineSegment(nil), job.Segments...)
	sort.SliceStable(segs, func(a, b int) bool { return segs[a].InterestScore > segs[b].InterestScore })
	for _, seg := range segs {
		if seg.Status == models.SegProcessing || seg.Status == models.SegDone {
			continue
		}
		if seg.InterestScore < AutoInterestThreshold {
			continue
		}
		if haveFull || seg.ByteEnd <= contig {
			e.enqueue(jobID, seg.ID)
		}
	}
}

func (e *Engine) enqueue(jobID, segmentID string) {
	// flip to processing under lock so we don't double-queue
	updated, err := e.store.UpdateJob(jobID, func(j *models.Job) error {
		seg := store.FindSegment(j, segmentID)
		if seg == nil {
			return fmt.Errorf("segment %s not found", segmentID)
		}
		if seg.Status == models.SegProcessing || seg.Status == models.SegDone {
			return errAlready
		}
		seg.Status = models.SegProcessing
		seg.Error = ""
		seg.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		if err != errAlready {
			e.log.Warn("enqueue", "job", jobID, "segment", segmentID, "err", err)
		}
		return
	}
	_ = updated
	select {
	case e.tasks <- granularTask{jobID: jobID, segmentID: segmentID}:
	default:
		e.log.Warn("task queue full, running inline", "job", jobID, "segment", segmentID)
		go e.process(granularTask{jobID: jobID, segmentID: segmentID})
	}
}

var errAlready = fmt.Errorf("already queued")

func (e *Engine) worker(id int) {
	defer e.wg.Done()
	for t := range e.tasks {
		e.process(t)
	}
}

func (e *Engine) process(t granularTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	ctx = e.withEvents(ctx, t.jobID)

	job, err := e.store.GetJob(t.jobID)
	if err != nil {
		e.log.Error("granular: load job", "err", err)
		return
	}
	seg := store.FindSegment(job, t.segmentID)
	if seg == nil {
		return
	}

	src := e.bestSource(t.jobID, seg)
	if src == "" {
		e.failSegment(t.jobID, t.segmentID, "no source bytes available yet")
		return
	}

	outDir := e.dir("collages", t.jobID)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		e.failSegment(t.jobID, t.segmentID, err.Error())
		return
	}
	outPath := filepath.Join(outDir, fmt.Sprintf("seg-%03d.jpg", seg.Index))

	opts := ffmpeg.GranularOptions{
		InputPath:  src,
		OutputPath: outPath,
		StartSec:   seg.StartSec,
		EndSec:     seg.EndSec,
		Cols:       6,
		Rows:       6,
		CellWidth:  320,
		Bin:        e.ffmpegBin,
	}
	e.LogEvent(t.jobID, models.EventInfo, "segment %d: building granular collage", seg.Index)
	res, err := ffmpeg.RunGranular(ctx, opts, 4)
	if err != nil {
		e.failSegment(t.jobID, t.segmentID, err.Error())
		return
	}

	_, _ = e.store.UpdateJob(t.jobID, func(j *models.Job) error {
		s := store.FindSegment(j, t.segmentID)
		if s == nil {
			return nil
		}
		s.GranularCollage = "/media/collages/" + t.jobID + "/" + filepath.Base(outPath)
		s.SceneChangeTimes = res.SceneTimes
		s.UpdatedAt = time.Now().UTC()
		return nil
	})

	e.LogEvent(t.jobID, models.EventInfo, "segment %d: analyzing (provider=%s)", seg.Index, e.provider.Name())
	desc, derr := e.provider.AnalyzeSegment(ctx, job, seg, outPath)
	if derr != nil {
		e.LogEvent(t.jobID, models.EventError, "segment %d: analysis failed: %v", seg.Index, derr)
	} else {
		e.LogEvent(t.jobID, models.EventInfo, "segment %d: description ready (%d chars)", seg.Index, len(desc))
	}
	_, _ = e.store.UpdateJob(t.jobID, func(j *models.Job) error {
		s := store.FindSegment(j, t.segmentID)
		if s == nil {
			return nil
		}
		if derr == nil {
			s.Description = desc
		}
		s.Status = models.SegDone
		s.UpdatedAt = time.Now().UTC()
		return nil
	})
	e.log.Info("granular done", "job", t.jobID, "segment", seg.Index, "frames", res.FrameCount, "fallback", res.UsedFallback)
	e.maybeComplete(t.jobID)
}

// bestSource returns the assembled full file if present, else a contiguous prefix
// file covering this segment, else "".
func (e *Engine) bestSource(jobID string, seg *models.TimelineSegment) string {
	e.mu.Lock()
	full := e.full[jobID]
	e.mu.Unlock()
	if full != "" {
		return full
	}
	if e.contiguousBytes(jobID) < seg.ByteEnd {
		return ""
	}
	e.mu.Lock()
	count := 0
	for i := 0; ; i++ {
		if _, ok := e.parts[jobID][i]; !ok {
			break
		}
		count++
	}
	e.mu.Unlock()
	prefix := e.dir("assembled", jobID, "prefix.mp4")
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		return ""
	}
	if err := e.concatParts(jobID, prefix, count); err != nil {
		e.log.Error("prefix assemble", "job", jobID, "err", err)
		return ""
	}
	return prefix
}

func (e *Engine) failSegment(jobID, segmentID, msg string) {
	e.log.Error("granular failed", "job", jobID, "segment", segmentID, "err", msg)
	e.LogEvent(jobID, models.EventError, "segment %s: failed: %s", segmentID, msg)
	_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
		if s := store.FindSegment(j, segmentID); s != nil {
			s.Status = models.SegError
			s.Error = msg
			s.UpdatedAt = time.Now().UTC()
		}
		return nil
	})
}

func (e *Engine) maybeComplete(jobID string) {
	e.mu.Lock()
	if e.done[jobID] {
		e.mu.Unlock()
		return
	}
	full := e.full[jobID]
	e.mu.Unlock()

	job, err := e.store.GetJob(jobID)
	if err != nil {
		return
	}
	for _, seg := range job.Segments {
		if seg.Status != models.SegDone && seg.Status != models.SegError {
			return
		}
	}
	if full == "" {
		return
	}
	e.mu.Lock()
	e.done[jobID] = true
	e.mu.Unlock()

	archiveURL := "/media/assembled/" + jobID + "/" + filepath.Base(full)
	if job.Origin == models.OriginImmich {
		// The original is safe in Immich (job.ImmichAssetID); don't keep a
		// redundant local copy of the raw video, only the small derived
		// artifacts (collages, audio, chat) that make the UI useful.
		archiveURL = ""
		e.cleanupImmichSource(jobID)
		if e.immichWriteback && e.immich != nil && job.ImmichAutoWriteback {
			e.LogEvent(jobID, models.EventInfo, "immich: writing analysis back to source asset (auto)")
			e.writeBackToImmich(jobID)
		}
	}
	_, _ = e.store.UpdateJob(jobID, func(j *models.Job) error {
		j.Status = models.JobComplete
		j.ArchiveURL = archiveURL
		return nil
	})
	e.log.Info("job complete", "job", jobID, "origin", job.Origin)
	e.LogEvent(jobID, models.EventInfo, "job complete")
}

// cleanupImmichSource removes the locally staged/assembled copies of an
// Immich-origin job's raw video once analysis is done — the original remains
// safely in the user's Immich (Job.ImmichAssetID), so a duplicate here only
// costs disk. Derived artifacts (collages, audio, chat) are untouched.
func (e *Engine) cleanupImmichSource(jobID string) {
	e.LogEvent(jobID, models.EventInfo, "immich: cleaning up local copy of source video (original kept in Immich)")
	for _, dir := range []string{e.dir("immich", jobID), e.dir("assembled", jobID)} {
		if err := os.RemoveAll(dir); err != nil {
			e.log.Warn("immich: cleanup failed", "job", jobID, "dir", dir, "err", err)
		}
	}
	e.mu.Lock()
	delete(e.full, jobID)
	e.mu.Unlock()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
