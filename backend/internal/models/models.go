// Package models holds the wire types shared with the Angular frontend.
// It mirrors shared/openapi.yaml by hand; keep the three in sync.
package models

import "time"

// JobStatus tracks the coarse-to-fine lifecycle of a job.
type JobStatus string

const (
	JobCreated          JobStatus = "created"
	JobDownloading      JobStatus = "downloading" // origin=immich: fetching the asset server-side
	JobOverviewReceived JobStatus = "overview_received"
	JobAudioReceived    JobStatus = "audio_received"
	JobAnalyzing        JobStatus = "analyzing"
	JobComplete         JobStatus = "complete"
	JobError            JobStatus = "error"
)

// JobOrigin records how the source bytes reached the server.
type JobOrigin string

const (
	OriginUpload JobOrigin = "upload" // browser tus upload (default)
	OriginImmich JobOrigin = "immich" // backend pulled it from Immich
)

// SegmentStatus tracks upload + granular-analysis progress of one timeline slice.
type SegmentStatus string

const (
	SegPending    SegmentStatus = "pending"
	SegUploading  SegmentStatus = "uploading"
	SegUploaded   SegmentStatus = "uploaded"
	SegProcessing SegmentStatus = "processing"
	SegDone       SegmentStatus = "done"
	SegError      SegmentStatus = "error"
)

// SegmentSource records who flagged a segment as interesting.
type SegmentSource string

const (
	SourcePlan SegmentSource = "plan"
	SourceUser SegmentSource = "user"
	SourceAI   SegmentSource = "ai"
)

// FrameMeta is one cell of the Stage-1 macro collage.
type FrameMeta struct {
	T     float64 `json:"t"`
	Row   int     `json:"row"`
	Col   int     `json:"col"`
	X     int     `json:"x"`
	Y     int     `json:"y"`
	W     int     `json:"w"`
	H     int     `json:"h"`
	Label string  `json:"label,omitempty"`
}

// OverviewMeta is the JSON blob posted alongside the macro collage.
type OverviewMeta struct {
	Cols       int         `json:"cols"`
	Rows       int         `json:"rows"`
	CellWidth  int         `json:"cellWidth"`
	CellHeight int         `json:"cellHeight"`
	Frames     []FrameMeta `json:"frames"`
	Hints      []struct {
		SegmentIndex int     `json:"segmentIndex"`
		Score        float64 `json:"score"`
	} `json:"interestHints,omitempty"`
}

// ChatRole distinguishes the two sides of a chat turn.
type ChatRole string

const (
	ChatUser      ChatRole = "user"
	ChatAssistant ChatRole = "assistant"
)

// ChatMessage is one turn in a job's Q&A chat.
type ChatMessage struct {
	Role      ChatRole  `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// EventLevel classifies a JobEvent for the UI (color-coding, filtering).
type EventLevel string

const (
	EventInfo  EventLevel = "info"
	EventWarn  EventLevel = "warn"
	EventError EventLevel = "error"
)

// JobEvent is one entry in a job's running activity log: pipeline steps
// ("building macro collage", "job complete") and AI-provider decisions
// ("VLM: trying gemini-free/gemini-3.6-flash", "... failed, falling back").
type JobEvent struct {
	Time    time.Time  `json:"time"`
	Level   EventLevel `json:"level"`
	Message string     `json:"message"`
}

// TimelineSegment is a contiguous slice of the video bound to the timeline.
type TimelineSegment struct {
	ID               string        `json:"id"`
	JobID            string        `json:"jobId"`
	Index            int           `json:"index"`
	StartSec         float64       `json:"startSec"`
	EndSec           float64       `json:"endSec"`
	InterestScore    float64       `json:"interestScore"`
	Source           SegmentSource `json:"source"`
	Status           SegmentStatus `json:"status"`
	UploadedBytes    int64         `json:"uploadedBytes"`
	TotalBytes       int64         `json:"totalBytes"`
	ByteStart        int64         `json:"byteStart"`
	ByteEnd          int64         `json:"byteEnd"`
	GranularCollage  string        `json:"granularCollageUrl,omitempty"`
	SceneChangeTimes []float64     `json:"sceneChangeTimes,omitempty"`
	Description      string        `json:"description,omitempty"`
	Error            string        `json:"error,omitempty"`
	UpdatedAt        time.Time     `json:"updatedAt"`
}

// Job is the whole unit of work.
type Job struct {
	ID            string    `json:"id"`
	Filename      string    `json:"filename"`
	SizeBytes     int64     `json:"sizeBytes"`
	DurationSec   float64   `json:"durationSec"`
	MimeType      string    `json:"mimeType,omitempty"`
	Status        JobStatus `json:"status"`
	Origin        JobOrigin `json:"origin"`
	ImmichAssetID string    `json:"immichAssetId,omitempty"`
	// ImmichAutoWriteback opts an origin=immich job into writing its analysis
	// back onto the source asset automatically on completion. Default false —
	// the user triggers it manually (POST .../immich-writeback) unless they
	// turned this on at import time. ImmichWrittenBack records whether a
	// write-back has completed at least once (manual or automatic).
	ImmichAutoWriteback bool               `json:"immichAutoWriteback,omitempty"`
	ImmichWrittenBack   bool               `json:"immichWrittenBack,omitempty"`
	CreatedAt           time.Time          `json:"createdAt"`
	UpdatedAt           time.Time          `json:"updatedAt"`
	OverviewCollage     string             `json:"overviewCollageUrl,omitempty"`
	OverviewStory       string             `json:"overviewStory,omitempty"`
	AudioReceived       bool               `json:"audioReceived"`
	Transcript          string             `json:"transcript,omitempty"`
	AnalysisProvider    string             `json:"analysisProvider,omitempty"`
	Error               string             `json:"error,omitempty"`
	Frames              []FrameMeta        `json:"frames,omitempty"`
	Segments            []*TimelineSegment `json:"segments"`
	ArchiveURL          string             `json:"archiveUrl,omitempty"` // unset for origin=immich after cleanup; original lives in Immich
	ChatMessages        []ChatMessage      `json:"chatMessages,omitempty"`
	Events              []JobEvent         `json:"events,omitempty"`

	// TranscribeLanguageHint / OutputLanguage are ISO-639-1 codes (or a
	// comma-separated candidate list for TranscribeLanguageHint, e.g. "en,sl").
	// Empty means "use the server default" — see analysis.FromEnv.
	TranscribeLanguageHint string `json:"transcribeLanguageHint,omitempty"`
	OutputLanguage         string `json:"outputLanguage,omitempty"`
}

// CreateJobRequest is the POST /jobs body.
type CreateJobRequest struct {
	Filename       string  `json:"filename"`
	SizeBytes      int64   `json:"sizeBytes"`
	DurationSec    float64 `json:"durationSec"`
	MimeType       string  `json:"mimeType"`
	SegmentSeconds float64 `json:"segmentSeconds"`
	SegmentPlan    []struct {
		StartSec float64 `json:"startSec"`
		EndSec   float64 `json:"endSec"`
	} `json:"segmentPlan"`
	// LanguageHint / OutputLanguage override the server defaults (see
	// analysis.FromEnv) for this job only. Both optional.
	LanguageHint   string `json:"languageHint"`
	OutputLanguage string `json:"outputLanguage"`
}

// CreateJobResponse is the POST /jobs reply.
type CreateJobResponse struct {
	Job         *Job   `json:"job"`
	TusEndpoint string `json:"tusEndpoint"`
}

// JobSummary is the lightweight projection of a Job used by GET /jobs (the
// "previous videos" timeline) — cheap to list without shipping every job's
// full segments/events/chat history over the wire.
type JobSummary struct {
	ID                 string    `json:"id"`
	Filename           string    `json:"filename"`
	Status             JobStatus `json:"status"`
	Origin             JobOrigin `json:"origin"`
	DurationSec        float64   `json:"durationSec"`
	OverviewCollageUrl string    `json:"overviewCollageUrl,omitempty"`
	OverviewStory      string    `json:"overviewStory,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// ListJobsResponse is the GET /jobs reply.
type ListJobsResponse struct {
	Jobs []JobSummary `json:"jobs"`
}

// CreateJobFromImmichRequest is the POST /jobs/from-immich body. Exactly one of
// AssetID / ShareLink is required; ShareLink accepts a raw share key or a full
// https://<host>/share/<key> URL. When a share link contains several assets,
// AssetID disambiguates which one to import (otherwise the first VIDEO wins).
type CreateJobFromImmichRequest struct {
	AssetID        string  `json:"assetId"`
	ShareLink      string  `json:"shareLink"`
	SegmentSeconds float64 `json:"segmentSeconds"`
	LanguageHint   string  `json:"languageHint"`
	OutputLanguage string  `json:"outputLanguage"`
	// AutoWriteback opts this job into writing the analysis back onto the
	// source Immich asset automatically on completion (default false — a
	// manual POST .../immich-writeback is needed otherwise).
	AutoWriteback bool `json:"autoWriteback"`
}

// ProcessSegmentRequest is the optional POST .../process body.
type ProcessSegmentRequest struct {
	Source        SegmentSource `json:"source"`
	InterestScore *float64      `json:"interestScore"`
	Reason        string        `json:"reason"`
}

// ChatRequest is the POST /jobs/{id}/chat body.
type ChatRequest struct {
	Message string `json:"message"`
}
