// Package analysis abstracts the VLM / speech-to-text calls behind a small
// interface with two implementations: a dependency-free heuristic ("noop") and one
// that talks to an OpenAI-compatible gateway ("litellm"). The provider is chosen by
// the ANALYSIS_PROVIDER env var (default "litellm", auto-falling back to "noop"
// when the gateway is not configured).
package analysis

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

// OverviewResult is what a provider extracts from the Stage-1 macro collage.
type OverviewResult struct {
	Story  string
	Scores map[int]float64 // segment index -> interest score 0..1
}

// Provider performs the AI steps of the pipeline. Implementations must be safe for
// concurrent use.
type Provider interface {
	// Name identifies the implementation ("noop" | "litellm").
	Name() string
	// AnalyzeOverview describes the whole video from the macro collage and scores
	// each segment's interest.
	AnalyzeOverview(ctx context.Context, job *models.Job, collagePath string, meta models.OverviewMeta) (OverviewResult, error)
	// Transcribe returns a plain-text transcript of the compressed audio track.
	Transcribe(ctx context.Context, job *models.Job, audioPath string) (string, error)
	// AnalyzeSegment describes one granular per-segment collage.
	AnalyzeSegment(ctx context.Context, job *models.Job, seg *models.TimelineSegment, collagePath string) (string, error)
	// Chat answers a follow-up question about job, grounded in its analysis
	// (story, transcript, segment descriptions) plus prior chat history.
	// overviewCollagePath is the on-disk macro collage (attached for visual
	// grounding on the first turn only, i.e. when history is empty); pass ""
	// if unavailable.
	Chat(ctx context.Context, job *models.Job, history []models.ChatMessage, question, overviewCollagePath string) (string, error)
}

// Config is read from the environment. VLMModels / WhisperModels are ordered
// fallback chains: the litellm provider tries each in turn and moves to the next
// on a retryable failure (HTTP 429 / 5xx / rate-limit), so a value like
// "gemini-free/gemini-3.6-flash,gemini-paid/gemini-3.6-flash,gemini-free/gemini-2.5-flash"
// gives free-tier-first with paid + older-model fallback without any gateway config.
type Config struct {
	ProviderName  string
	BaseURL       string
	APIKey        string
	VLMModels     []string
	WhisperModels []string

	// DefaultLanguageHint seeds Job.TranscribeLanguageHint when a create-job
	// request doesn't specify one. A comma-separated list (e.g. "en,sl") is a
	// soft multi-candidate hint; a single code forces that language. See
	// LiteLLMProvider.transcribeOnce.
	DefaultLanguageHint string
	// DefaultOutputLanguage seeds Job.OutputLanguage similarly (single code).
	DefaultOutputLanguage string
}

// FromEnv builds a Config from ANALYSIS_PROVIDER and LITELLM_* variables.
func FromEnv() Config {
	return Config{
		ProviderName:          envOr("ANALYSIS_PROVIDER", "litellm"),
		BaseURL:               strings.TrimRight(os.Getenv("LITELLM_BASE_URL"), "/"),
		APIKey:                os.Getenv("LITELLM_API_KEY"),
		VLMModels:             splitModels(envOr("LITELLM_VLM_MODEL", "vision-model-placeholder")),
		WhisperModels:         splitModels(envOr("LITELLM_WHISPER_MODEL", "whisper-1")),
		DefaultLanguageHint:   envOr("TRANSCRIBE_LANGUAGE_HINT", "en,sl"),
		DefaultOutputLanguage: envOr("OUTPUT_LANGUAGE", "sl"),
	}
}

// splitModels parses a comma-separated fallback chain, trimming blanks.
func splitModels(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// New returns the configured provider, transparently falling back to the noop
// heuristic provider when "litellm" is selected but not usably configured.
func New(cfg Config, log *slog.Logger) Provider {
	if cfg.ProviderName == "noop" {
		return &NoopProvider{}
	}
	if cfg.BaseURL == "" {
		log.Warn("analysis: litellm selected but LITELLM_BASE_URL is empty; falling back to noop provider")
		return &NoopProvider{}
	}
	log.Info("analysis: using litellm provider", "baseURL", cfg.BaseURL, "vlmModels", cfg.VLMModels, "whisperModels", cfg.WhisperModels)
	return NewLiteLLMProvider(cfg, log)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// languageNames maps a few known ISO-639-1 codes to a readable name for
// prompts ("Respond in Slovenian." reads better and is less ambiguous to a
// model than "Respond in sl."). Unknown codes are used as-is.
var languageNames = map[string]string{
	"sl": "Slovenian",
	"en": "English",
	"de": "German",
	"it": "Italian",
	"hr": "Croatian",
}

// LanguageName returns a human-readable name for an ISO-639-1 code, falling
// back to the code itself if unknown.
func LanguageName(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if name, ok := languageNames[code]; ok {
		return name
	}
	return code
}
