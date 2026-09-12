// Package api exposes the REST surface and mounts the tus handler, the media file
// server and the embedded Angular SPA.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/rokelvisar/storyframe/backend/internal/engine"
	"github.com/rokelvisar/storyframe/backend/internal/immich"
	"github.com/rokelvisar/storyframe/backend/internal/models"
	"github.com/rokelvisar/storyframe/backend/internal/store"
)

const (
	defaultSegmentSeconds = 30.0
	maxSegments           = 240
	maxOverviewBytes      = 12 << 20 // 12 MiB
	maxAudioBytes         = 64 << 20 // 64 MiB
)

// Deps are the API server dependencies.
type Deps struct {
	Store   *store.Store
	Engine  *engine.Engine
	Tus     http.Handler
	DataDir string
	Immich  *immich.Client // nil when Immich import isn't configured
	WebFS   fs.FS          // embedded Angular dist (may be nil in dev)
	Log     *slog.Logger

	// DefaultLanguageHint / DefaultOutputLanguage seed Job.TranscribeLanguageHint
	// / Job.OutputLanguage when a create-job request doesn't specify one
	// (mirrors analysis.Config's defaults; passed separately so api doesn't
	// need to import analysis just for two strings).
	DefaultLanguageHint   string
	DefaultOutputLanguage string
}

// NewRouter builds the full HTTP handler.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger(d.Log))
	r.Use(middleware.Recoverer)
	r.Use(cors)

	s := &server{d: d}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/jobs", s.createJob)
		r.Get("/jobs", s.listJobs)
		r.Post("/jobs/from-immich", s.postJobFromImmich)
		r.Get("/jobs/{id}", s.getJob)
		r.Post("/jobs/{id}/overview", s.postOverview)
		r.Post("/jobs/{id}/audio", s.postAudio)
		r.Post("/jobs/{id}/segments/{segmentId}/process", s.processSegment)
		r.Post("/jobs/{id}/finalize", s.finalizeJob)
		r.Post("/jobs/{id}/chat", s.postChat)
		r.Post("/jobs/{id}/immich-writeback", s.postImmichWriteback)
	})

	// tus resumable uploads. tusd's routed mux matches on a root-relative path
	// (it ignores BasePath), so the /files prefix must be stripped before it;
	// BasePath "/files/" is still used by tusd to build Location headers.
	tus := http.StripPrefix("/files", d.Tus)
	r.Handle("/files", tus)
	r.Handle("/files/*", tus)

	// generated media (collages, assembled archive)
	mediaDir := filepath.Join(d.DataDir)
	r.Handle("/media/*", http.StripPrefix("/media/", http.FileServer(http.Dir(mediaDir))))

	// SPA
	r.NotFound(s.spa)
	return r
}

type server struct{ d Deps }

// --- handlers -----------------------------------------------------------------

// listJobs is the "previous videos" timeline: every job ever created (newest
// first), as lightweight summaries — not the full segments/events/chat that
// GET /jobs/{id} carries, so browsing history stays cheap.
func (s *server) listJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.d.Store.ListJobs(limit)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, models.ListJobsResponse{Jobs: jobs})
}

func (s *server) createJob(w http.ResponseWriter, r *http.Request) {
	var req models.CreateJobRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Filename == "" || req.SizeBytes <= 0 || req.DurationSec <= 0 {
		httpError(w, http.StatusBadRequest, "filename, sizeBytes and durationSec are required")
		return
	}

	segments := buildSegments(req)
	job := &models.Job{
		ID:                     uuid.NewString(),
		Filename:               req.Filename,
		SizeBytes:              req.SizeBytes,
		DurationSec:            req.DurationSec,
		MimeType:               req.MimeType,
		Status:                 models.JobCreated,
		Origin:                 models.OriginUpload,
		Segments:               segments,
		TranscribeLanguageHint: firstNonEmpty(req.LanguageHint, s.d.DefaultLanguageHint),
		OutputLanguage:         firstNonEmpty(req.OutputLanguage, s.d.DefaultOutputLanguage),
		Events: []models.JobEvent{{
			Time: time.Now().UTC(), Level: models.EventInfo,
			Message: fmt.Sprintf("job created: %s (%d segments)", req.Filename, len(segments)),
		}},
	}
	for _, seg := range job.Segments {
		seg.JobID = job.ID
	}
	if err := s.d.Store.CreateJob(job); err != nil {
		httpError(w, http.StatusInternalServerError, "create job: "+err.Error())
		return
	}
	s.d.Log.Info("job created", "job", job.ID, "file", job.Filename, "segments", len(job.Segments))
	writeJSON(w, http.StatusCreated, models.CreateJobResponse{Job: job, TusEndpoint: "/files/"})
}

// postJobFromImmich creates a job sourced from a self-hosted Immich instance,
// identified by asset id (needs IMMICH_API_KEY read access) or a public share
// link (works with no key; subject to the link's own allowDownload flag). No
// browser upload is involved: Immich sends no CORS headers, so the backend
// downloads the asset itself and builds Stage 1/2 with FFmpeg
// (engine.IngestFromImmich), then converges on the normal pipeline.
func (s *server) postJobFromImmich(w http.ResponseWriter, r *http.Request) {
	if s.d.Immich == nil {
		httpError(w, http.StatusServiceUnavailable, "immich import is not configured on this server")
		return
	}
	var req models.CreateJobFromImmichRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AssetID == "" && req.ShareLink == "" {
		httpError(w, http.StatusBadRequest, "one of assetId or shareLink is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var (
		asset immich.Asset
		err   error
	)
	if req.ShareLink != "" {
		asset, err = s.d.Immich.ResolveShareLink(ctx, req.ShareLink, req.AssetID)
	} else {
		asset, err = s.d.Immich.ResolveAsset(ctx, req.AssetID)
	}
	if err != nil {
		httpError(w, http.StatusBadGateway, "immich: "+err.Error())
		return
	}
	if asset.Type != "VIDEO" {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("immich asset %s is a %s, not a video", asset.ID, asset.Type))
		return
	}
	if asset.DurationSec <= 0 {
		httpError(w, http.StatusBadRequest, "immich asset has no duration")
		return
	}
	if asset.SizeBytes <= 0 {
		httpError(w, http.StatusBadGateway, "immich asset has no known size")
		return
	}

	segments := buildSegments(models.CreateJobRequest{
		SizeBytes:      asset.SizeBytes,
		DurationSec:    asset.DurationSec,
		SegmentSeconds: req.SegmentSeconds,
	})
	job := &models.Job{
		ID:                     uuid.NewString(),
		Filename:               asset.OriginalFileName,
		SizeBytes:              asset.SizeBytes,
		DurationSec:            asset.DurationSec,
		MimeType:               asset.OriginalMimeType,
		Status:                 models.JobCreated,
		Origin:                 models.OriginImmich,
		ImmichAssetID:          asset.ID,
		ImmichAutoWriteback:    req.AutoWriteback,
		Segments:               segments,
		TranscribeLanguageHint: firstNonEmpty(req.LanguageHint, s.d.DefaultLanguageHint),
		OutputLanguage:         firstNonEmpty(req.OutputLanguage, s.d.DefaultOutputLanguage),
		Events: []models.JobEvent{{
			Time: time.Now().UTC(), Level: models.EventInfo,
			Message: fmt.Sprintf("job created from immich asset %s: %s (%d segments)", asset.ID, asset.OriginalFileName, len(segments)),
		}},
	}
	for _, seg := range job.Segments {
		seg.JobID = job.ID
	}
	if err := s.d.Store.CreateJob(job); err != nil {
		httpError(w, http.StatusInternalServerError, "create job: "+err.Error())
		return
	}
	s.d.Log.Info("job created from immich", "job", job.ID, "asset", asset.ID, "file", job.Filename, "segments", len(job.Segments))
	s.d.Engine.IngestFromImmich(job.ID, asset)
	writeJSON(w, http.StatusCreated, models.CreateJobResponse{Job: job})
}

func (s *server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.d.Store.GetJob(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) postOverview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.d.Store.GetJob(id); errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	if err := r.ParseMultipartForm(maxOverviewBytes); err != nil {
		httpError(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	metaRaw := r.FormValue("meta")
	var meta models.OverviewMeta
	if err := json.Unmarshal([]byte(metaRaw), &meta); err != nil {
		httpError(w, http.StatusBadRequest, "meta is not valid OverviewMeta JSON")
		return
	}
	collagePath, err := s.saveUpload(r, "collage", filepath.Join(s.d.DataDir, "overview", id), "collage.jpg", maxOverviewBytes)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	job, err := s.d.Store.UpdateJob(id, func(j *models.Job) error {
		j.Frames = meta.Frames
		j.OverviewCollage = "/media/overview/" + id + "/collage.jpg"
		if j.Status == models.JobCreated {
			j.Status = models.JobOverviewReceived
		}
		return nil
	})
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.d.Engine.SubmitOverview(id, collagePath, meta)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *server) postAudio(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.d.Store.GetJob(id); errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	if err := r.ParseMultipartForm(maxAudioBytes); err != nil {
		httpError(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	audioPath, err := s.saveUpload(r, "audio", filepath.Join(s.d.DataDir, "audio", id), "audio.webm", maxAudioBytes)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.d.Store.UpdateJob(id, func(j *models.Job) error {
		if j.Status == models.JobCreated || j.Status == models.JobOverviewReceived {
			j.Status = models.JobAudioReceived
		}
		return nil
	})
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.d.Engine.SubmitAudio(id, audioPath)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *server) processSegment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	segID := chi.URLParam(r, "segmentId")

	var req models.ProcessSegmentRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)

	job, err := s.d.Store.UpdateJob(id, func(j *models.Job) error {
		seg := store.FindSegment(j, segID)
		if seg == nil {
			return store.ErrNotFound
		}
		if req.Source != "" {
			seg.Source = req.Source
		} else {
			seg.Source = models.SourceUser
		}
		if req.InterestScore != nil {
			seg.InterestScore = clamp01(*req.InterestScore)
		} else if seg.InterestScore < 0.9 {
			seg.InterestScore = 0.9 // explicit user pick outranks the VLM
		}
		seg.UpdatedAt = time.Now().UTC()
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job or segment")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.d.Engine.RequestSegment(id, segID)
	writeJSON(w, http.StatusAccepted, store.FindSegment(job, segID))
}

func (s *server) finalizeJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := s.d.Store.GetJob(id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, seg := range job.Segments {
		s.d.Engine.RequestSegment(id, seg.ID)
	}
	writeJSON(w, http.StatusAccepted, job)
}

// postChat appends the user's question, then asks engine.SubmitChat to answer
// it asynchronously (same split-responsibility shape as postOverview/postAudio:
// this handler persists synchronously, the engine goroutine appends the reply).
func (s *server) postChat(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req models.ChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		httpError(w, http.StatusBadRequest, "message is required")
		return
	}

	job, err := s.d.Store.UpdateJob(id, func(j *models.Job) error {
		if j.OverviewStory == "" {
			return errChatNotReady
		}
		j.ChatMessages = append(j.ChatMessages, models.ChatMessage{
			Role: models.ChatUser, Content: req.Message, CreatedAt: time.Now().UTC(),
		})
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	if errors.Is(err, errChatNotReady) {
		httpError(w, http.StatusConflict, "chat isn't available yet: the video overview hasn't finished analyzing")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.d.Engine.SubmitChat(id, req.Message)
	writeJSON(w, http.StatusAccepted, job)
}

var errChatNotReady = errors.New("chat not ready")

// postImmichWriteback manually (re-)triggers writing the finished analysis
// back onto the source Immich asset — the button a user clicks when the job
// wasn't imported with autoWriteback on. engine.TriggerImmichWriteback
// validates origin/config and runs the same best-effort goroutine as the
// automatic path in engine.maybeComplete.
func (s *server) postImmichWriteback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := s.d.Store.GetJob(id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "unknown job")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.d.Engine.TriggerImmichWriteback(id); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// --- SPA --------------------------------------------------------------------

func (s *server) spa(w http.ResponseWriter, r *http.Request) {
	if s.d.WebFS == nil || strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.d.WebFS.Open(p); err == nil {
		defer f.Close()
		if st, err := f.Stat(); err == nil && !st.IsDir() {
			http.ServeContent(w, r, p, st.ModTime(), f.(io.ReadSeeker))
			return
		}
	}
	// SPA fallback
	f, err := s.d.WebFS.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, "index.html", modTime(st), f.(io.ReadSeeker))
}

func modTime(st fs.FileInfo) time.Time {
	if st == nil {
		return time.Time{}
	}
	return st.ModTime()
}

// --- helpers ---------------------------------------------------------------

func buildSegments(req models.CreateJobRequest) []*models.TimelineSegment {
	var bounds [][2]float64
	if len(req.SegmentPlan) > 0 {
		for _, p := range req.SegmentPlan {
			bounds = append(bounds, [2]float64{p.StartSec, p.EndSec})
		}
	} else {
		step := req.SegmentSeconds
		if step <= 0 {
			step = defaultSegmentSeconds
		}
		if n := int(math.Ceil(req.DurationSec / step)); n > maxSegments {
			step = req.DurationSec / float64(maxSegments)
		}
		for t := 0.0; t < req.DurationSec-0.001; t += step {
			end := math.Min(t+step, req.DurationSec)
			bounds = append(bounds, [2]float64{t, end})
		}
	}

	segs := make([]*models.TimelineSegment, 0, len(bounds))
	for i, b := range bounds {
		byteStart := int64(math.Round(float64(req.SizeBytes) * b[0] / req.DurationSec))
		byteEnd := int64(math.Round(float64(req.SizeBytes) * b[1] / req.DurationSec))
		if i == len(bounds)-1 {
			byteEnd = req.SizeBytes
		}
		segs = append(segs, &models.TimelineSegment{
			ID:            uuid.NewString(),
			Index:         i,
			StartSec:      b[0],
			EndSec:        b[1],
			InterestScore: 0,
			Source:        models.SourcePlan,
			Status:        models.SegPending,
			ByteStart:     byteStart,
			ByteEnd:       byteEnd,
			TotalBytes:    byteEnd - byteStart,
			UpdatedAt:     time.Now().UTC(),
		})
	}
	return segs
}

func (s *server) saveUpload(r *http.Request, field, dir, name string, max int64) (string, error) {
	file, _, err := r.FormFile(field)
	if err != nil {
		return "", errors.New("missing form file: " + field)
	}
	defer file.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(file, max)); err != nil {
		return "", err
	}
	return dst, out.Sync()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if !strings.HasPrefix(r.URL.Path, "/files") { // tus is chatty
				log.Info("http",
					"method", r.Method, "path", r.URL.Path,
					"status", ww.Status(), "bytes", ww.BytesWritten(),
					"dur", time.Since(start).String())
			}
		})
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files") {
			next.ServeHTTP(w, r) // tusd manages its own CORS headers
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, HEAD, OPTIONS, DELETE")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Access-Control-Expose-Headers", "Location, Upload-Offset, Upload-Length, Tus-Resumable")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
