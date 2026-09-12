// Package tusd wires the resumable-upload handler and routes each completed
// upload (partial per-segment, final concatenation, or plain whole-file) to the
// engine.
package tusd

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/filestore"
)

// Sink receives routed upload completions.
type Sink interface {
	OnPartUploaded(jobID, segmentID string, index int, srcPath string, size int64)
	OnFullUploaded(jobID, srcPath string)
}

// Handler bundles the tus HTTP handler and its completion consumer.
type Handler struct {
	http *tusd.Handler
	log  *slog.Logger
}

// New builds a tus handler storing uploads under dir/uploads and spawns the
// completion consumer goroutine.
func New(dataDir string, sink Sink, log *slog.Logger) (*Handler, error) {
	uploadDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return nil, err
	}
	fs := filestore.New(uploadDir)
	composer := tusd.NewStoreComposer()
	fs.UseIn(composer)

	// tusd's DefaultCorsConfig already allows any origin and every tus header, so
	// no Cors override is needed here. The API's own CORS middleware skips /files.
	h, err := tusd.NewHandler(tusd.Config{
		BasePath:                "/files/",
		StoreComposer:           composer,
		NotifyCompleteUploads:   true,
		NotifyTerminatedUploads: true,
		DisableDownload:         true,
		RespectForwardedHeaders: true,
	})
	if err != nil {
		return nil, err
	}

	handler := &Handler{http: h, log: log}
	go handler.consume(h, sink)
	return handler, nil
}

// ServeHTTP exposes the underlying tus handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.http.ServeHTTP(w, r)
}

func (h *Handler) consume(th *tusd.Handler, sink Sink) {
	for {
		select {
		case ev := <-th.CompleteUploads:
			h.route(ev, sink)
		case ev := <-th.TerminatedUploads:
			h.log.Info("upload terminated", "id", ev.Upload.ID)
		}
	}
}

func (h *Handler) route(ev tusd.HookEvent, sink Sink) {
	up := ev.Upload
	path := up.Storage["Path"]
	if path == "" {
		h.log.Warn("completed upload without storage path", "id", up.ID)
		return
	}
	md := up.MetaData
	jobID := md["jobId"]
	if jobID == "" {
		h.log.Warn("completed upload without jobId metadata", "id", up.ID)
		return
	}
	segmentID := md["segmentId"]
	idx, _ := strconv.Atoi(md["partIndex"])

	switch {
	case up.IsFinal:
		h.log.Info("final concatenated upload complete", "job", jobID, "id", up.ID, "size", up.Size)
		sink.OnFullUploaded(jobID, path)
	case up.IsPartial || segmentID != "":
		h.log.Info("partial upload complete", "job", jobID, "segment", segmentID, "index", idx, "size", up.Size)
		sink.OnPartUploaded(jobID, segmentID, idx, path, up.Size)
	default:
		h.log.Info("whole-file upload complete", "job", jobID, "id", up.ID, "size", up.Size)
		sink.OnFullUploaded(jobID, path)
	}
}
