package analysis

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

// LiteLLMProvider talks to any OpenAI-compatible gateway (LiteLLM, vLLM, OpenAI,
// OpenRouter, ...). Vision requests use /v1/chat/completions with an inline
// base64 image; transcription uses /v1/audio/transcriptions.
//
// VLMModels / WhisperModels are ordered fallback chains: each call walks the list
// and advances to the next entry on a retryable failure (HTTP 429 / 5xx / a
// gateway "RateLimitError" body / an empty completion). This gives
// free-tier-first-then-paid behaviour without any gateway-side config.
type LiteLLMProvider struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client
}

// NewLiteLLMProvider constructs the provider.
func NewLiteLLMProvider(cfg Config, log *slog.Logger) *LiteLLMProvider {
	return &LiteLLMProvider{
		cfg:  cfg,
		log:  log,
		http: &http.Client{Timeout: 4 * time.Minute},
	}
}

func (p *LiteLLMProvider) Name() string { return "litellm" }

// outputLanguage resolves the effective output language for a job, falling
// back to the provider's configured default (never empty in practice — the
// API layer already fills Job.OutputLanguage from the server default at
// create-job time, but a zero-value Job in a test is still handled).
func (p *LiteLLMProvider) outputLanguage(job *models.Job) string {
	if job.OutputLanguage != "" {
		return job.OutputLanguage
	}
	if p.cfg.DefaultOutputLanguage != "" {
		return p.cfg.DefaultOutputLanguage
	}
	return "en"
}

func overviewSystemPrompt(lang string) string {
	return fmt.Sprintf(`You are a video analyst. You receive a single contact-sheet image: a grid of frames sampled evenly across one video, each stamped with its timestamp (HH:MM:SS). Return STRICT JSON, no prose, matching:
{"story": string, "segments": [{"index": number, "interestScore": number (0..1), "reason": string}]}
Keep the JSON keys exactly as shown (in English) — only translate the natural-language VALUES of "story" and "reason" into %s. "story" is a 3-6 sentence coarse narrative of the whole video. Provide one segments entry per segment index given by the user, scoring how much a human would want a detailed look at that time range.`, LanguageName(lang))
}

func (p *LiteLLMProvider) AnalyzeOverview(ctx context.Context, job *models.Job, collagePath string, meta models.OverviewMeta) (OverviewResult, error) {
	img, err := dataURI(collagePath, "image/jpeg")
	if err != nil {
		return OverviewResult{}, err
	}

	var segLines strings.Builder
	for _, s := range job.Segments {
		fmt.Fprintf(&segLines, "index %d: %.0fs-%.0fs\n", s.Index, s.StartSec, s.EndSec)
	}
	userText := fmt.Sprintf("Video file: %s\nDuration: %.0fs\nSegments:\n%s", job.Filename, job.DurationSec, segLines.String())

	build := func(model string) chatRequest {
		return chatRequest{
			Model: model,
			Messages: []chatMessage{
				{Role: "system", Content: []contentPart{{Type: "text", Text: overviewSystemPrompt(p.outputLanguage(job))}}},
				{Role: "user", Content: []contentPart{
					{Type: "text", Text: userText},
					{Type: "image_url", ImageURL: &imageURL{URL: img}},
				}},
			},
			Temperature: 0.2,
			// Generous: "thinking" gateways (e.g. gemini-2.5-flash) spend reasoning
			// tokens against this budget and return empty content on finish_reason=length.
			MaxTokens: 4000,
		}
	}

	raw, err := p.chat(ctx, p.cfg.VLMModels, build)
	if err != nil {
		return OverviewResult{}, err
	}

	var parsed struct {
		Story    string `json:"story"`
		Segments []struct {
			Index         int     `json:"index"`
			InterestScore float64 `json:"interestScore"`
			Reason        string  `json:"reason"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &parsed); err != nil {
		return OverviewResult{}, fmt.Errorf("parse overview JSON: %w (raw: %.200s)", err, raw)
	}
	scores := make(map[int]float64, len(parsed.Segments))
	for _, s := range parsed.Segments {
		scores[s.Index] = clamp01(s.InterestScore)
	}
	return OverviewResult{Story: strings.TrimSpace(parsed.Story), Scores: scores}, nil
}

func (p *LiteLLMProvider) AnalyzeSegment(ctx context.Context, job *models.Job, seg *models.TimelineSegment, collagePath string) (string, error) {
	img, err := dataURI(collagePath, "image/jpeg")
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf(
		"This contact sheet shows high-frequency frames from ONE %.0fs-%.0fs slice of the video %q. In 2-4 sentences describe what happens, focusing on motion, people and notable events. Plain text only, no headings or markdown. Respond in %s.",
		seg.StartSec, seg.EndSec, job.Filename, LanguageName(p.outputLanguage(job)),
	)
	build := func(model string) chatRequest {
		return chatRequest{
			Model: model,
			Messages: []chatMessage{
				{Role: "user", Content: []contentPart{
					{Type: "text", Text: prompt},
					{Type: "image_url", ImageURL: &imageURL{URL: img}},
				}},
			},
			Temperature: 0.3,
			MaxTokens:   2000, // headroom for "thinking" gateways; see AnalyzeOverview
		}
	}
	raw, err := p.chat(ctx, p.cfg.VLMModels, build)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

func chatSystemPrompt(job *models.Job, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are answering questions about a specific video (%q, %.0fs long) on behalf of its owner. Ground every answer in the analysis below; say so plainly if something isn't covered by it rather than guessing. Respond in %s, in plain prose (no markdown headings).\n\n", job.Filename, job.DurationSec, LanguageName(lang))
	if job.OverviewStory != "" {
		fmt.Fprintf(&b, "Overview: %s\n\n", job.OverviewStory)
	}
	if job.Transcript != "" {
		fmt.Fprintf(&b, "Audio transcript: %s\n\n", job.Transcript)
	}
	if len(job.Segments) > 0 {
		b.WriteString("Timeline segments:\n")
		for _, s := range job.Segments {
			if s.Description == "" {
				continue
			}
			fmt.Fprintf(&b, "- %.0fs-%.0fs: %s\n", s.StartSec, s.EndSec, s.Description)
		}
	}
	return b.String()
}

// Chat answers a follow-up question grounded in the job's existing analysis.
// The macro collage is attached on the first turn only (history empty) for a
// one-time visual grounding pass, not resent on every message.
func (p *LiteLLMProvider) Chat(ctx context.Context, job *models.Job, history []models.ChatMessage, question, overviewCollagePath string) (string, error) {
	msgs := []chatMessage{
		{Role: "system", Content: []contentPart{{Type: "text", Text: chatSystemPrompt(job, p.outputLanguage(job))}}},
	}

	firstTurn := len(history) == 0
	if firstTurn && overviewCollagePath != "" {
		if img, err := dataURI(overviewCollagePath, "image/jpeg"); err == nil {
			msgs = append(msgs, chatMessage{Role: "user", Content: []contentPart{
				{Type: "text", Text: "Here is the video's macro collage for visual reference."},
				{Type: "image_url", ImageURL: &imageURL{URL: img}},
			}})
			msgs = append(msgs, chatMessage{Role: "assistant", Content: []contentPart{{Type: "text", Text: "Got it, I can see the collage."}}})
		} else {
			p.log.Warn("chat: could not attach overview collage", "job", job.ID, "err", err)
		}
	}

	for _, m := range history {
		role := string(m.Role)
		msgs = append(msgs, chatMessage{Role: role, Content: []contentPart{{Type: "text", Text: m.Content}}})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: []contentPart{{Type: "text", Text: question}}})

	build := func(model string) chatRequest {
		return chatRequest{Model: model, Messages: msgs, Temperature: 0.4, MaxTokens: 2000}
	}
	raw, err := p.chat(ctx, p.cfg.VLMModels, build)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

func (p *LiteLLMProvider) Transcribe(ctx context.Context, job *models.Job, audioPath string) (string, error) {
	chain := p.cfg.WhisperModels
	if len(chain) == 0 {
		chain = []string{"whisper-1"}
	}
	hint := job.TranscribeLanguageHint
	if hint == "" {
		hint = p.cfg.DefaultLanguageHint
	}
	var lastErr error
	for i, model := range chain {
		emitEvent(ctx, "info", "Whisper: trying %s", model)
		text, err := p.transcribeOnce(ctx, model, audioPath, hint)
		if err == nil {
			if i > 0 {
				p.log.Info("transcription fell back", "model", model, "afterErr", lastErr)
			}
			emitEvent(ctx, "info", "Whisper: %s transcribed %d chars", model, len(text))
			return text, nil
		}
		lastErr = err
		var re retryErr
		if !errors.As(err, &re) || i == len(chain)-1 {
			emitEvent(ctx, "error", "Whisper: %s failed: %v", model, err)
			return "", err
		}
		p.log.Warn("transcription model failed, trying next", "model", model, "err", err)
		emitEvent(ctx, "warn", "Whisper: %s failed, falling back", model)
	}
	return "", lastErr
}

func (p *LiteLLMProvider) transcribeOnce(ctx context.Context, model, audioPath, languageHint string) (string, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "audio.webm")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "json")
	// Whisper's `language` field takes exactly one ISO-639-1 code (forces
	// decoding into it) — there's no multi-candidate form of it. So a single
	// hint ("sl") is sent as `language`; a candidate list ("en,sl", the
	// default) has no direct equivalent and is sent as a soft `prompt` steer
	// instead, which is Whisper's documented mechanism for biasing language
	// guesses without forcing one.
	if codes := splitModels(languageHint); len(codes) == 1 {
		_ = mw.WriteField("language", codes[0])
	} else if len(codes) > 1 {
		names := make([]string, len(codes))
		for i, c := range codes {
			names[i] = LanguageName(c)
		}
		_ = mw.WriteField("prompt", "The speech is likely in "+strings.Join(names, " or ")+".")
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	p.auth(req)

	resp, err := p.http.Do(req)
	if err != nil {
		return "", retryErr{err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		e := fmt.Errorf("transcription %q HTTP %d: %.300s", model, resp.StatusCode, data)
		if isRetryable(resp.StatusCode, string(data)) {
			return "", retryErr{e}
		}
		return "", e
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parse transcription: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

// --- OpenAI-compatible chat plumbing -------------------------------------------

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// retryErr marks an error as worth retrying against the next model in the chain.
type retryErr struct{ error }

func isRetryable(status int, body string) bool {
	if status == http.StatusTooManyRequests || status >= 500 {
		return true
	}
	b := strings.ToLower(body)
	return strings.Contains(b, "ratelimiterror") ||
		strings.Contains(b, "rate_limit") ||
		strings.Contains(b, "overloaded") ||
		strings.Contains(b, "exceeded your current quota")
}

// chat walks the model fallback chain, building a fresh request per attempt.
func (p *LiteLLMProvider) chat(ctx context.Context, chain []string, build func(model string) chatRequest) (string, error) {
	if len(chain) == 0 {
		return "", errors.New("no VLM model configured")
	}
	var lastErr error
	for i, model := range chain {
		emitEvent(ctx, "info", "VLM: trying %s", model)
		out, err := p.chatOnce(ctx, build(model))
		if err == nil {
			if i > 0 {
				p.log.Info("VLM fell back", "model", model, "afterErr", lastErr)
			}
			emitEvent(ctx, "info", "VLM: %s answered (%d chars)", model, len(out))
			return out, nil
		}
		lastErr = err
		var re retryErr
		if !errors.As(err, &re) || i == len(chain)-1 {
			emitEvent(ctx, "error", "VLM: %s failed: %v", model, err)
			return "", err
		}
		p.log.Warn("VLM model failed, trying next", "model", model, "err", err)
		emitEvent(ctx, "warn", "VLM: %s failed, falling back", model)
	}
	return "", lastErr
}

func (p *LiteLLMProvider) chatOnce(ctx context.Context, body chatRequest) (string, error) {
	blob, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(blob))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	p.auth(req)

	resp, err := p.http.Do(req)
	if err != nil {
		return "", retryErr{err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		e := fmt.Errorf("chat %q HTTP %d: %.300s", body.Model, resp.StatusCode, data)
		if isRetryable(resp.StatusCode, string(data)) {
			return "", retryErr{e}
		}
		return "", e
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parse chat response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", retryErr{fmt.Errorf("chat %q returned no choices: %.200s", body.Model, data)}
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		// e.g. a thinking model that spent the whole budget on reasoning tokens
		return "", retryErr{fmt.Errorf("chat %q returned empty content (finish_reason=%s)", body.Model, out.Choices[0].FinishReason)}
	}
	return content, nil
}

func (p *LiteLLMProvider) auth(req *http.Request) {
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
}

func dataURI(path, mime string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

var jsonBlockRe = regexp.MustCompile(`(?s)\{.*\}`)

// extractJSON pulls the first {...} block out of a model reply that may be wrapped
// in prose or ```json fences.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	if m := jsonBlockRe.FindString(s); m != "" {
		return m
	}
	return s
}
