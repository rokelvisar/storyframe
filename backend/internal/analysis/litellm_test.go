package analysis

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestOverviewSystemPrompt_Language(t *testing.T) {
	p := overviewSystemPrompt("sl")
	if !strings.Contains(p, "Slovenian") {
		t.Fatalf("expected the resolved language name in the prompt, got: %s", p)
	}
	if !strings.Contains(p, `"story"`) || !strings.Contains(p, `"segments"`) {
		t.Fatalf("expected the JSON keys to stay literal in the prompt: %s", p)
	}
}

func TestChatSystemPrompt_GroundsInAnalysis(t *testing.T) {
	job := &models.Job{
		Filename:      "clip.mp4",
		DurationSec:   90,
		OverviewStory: "A person walks a dog in a park.",
		Transcript:    "Good boy!",
		Segments: []*models.TimelineSegment{
			{StartSec: 0, EndSec: 30, Description: "Dog runs across grass."},
			{StartSec: 30, EndSec: 60, Description: ""}, // no description yet -> must be omitted
			{StartSec: 60, EndSec: 90, Description: "Owner throws a ball."},
		},
	}
	prompt := chatSystemPrompt(job, "en")
	for _, want := range []string{"clip.mp4", "90s", "A person walks a dog", "Good boy!", "Dog runs across grass", "Owner throws a ball", "English"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("chat system prompt missing %q\n%s", want, prompt)
		}
	}
	if got := strings.Count(prompt, "\n- "); got != 2 { // one bullet per segment WITH a description
		t.Errorf("expected exactly 2 segment bullets (empty description skipped), got %d in prompt:\n%s", got, prompt)
	}
}

func newTestLiteLLM(t *testing.T, handler http.HandlerFunc) (*LiteLLMProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := Config{
		BaseURL: srv.URL, APIKey: "test-key",
		VLMModels: []string{"m1"}, WhisperModels: []string{"w1"},
		DefaultOutputLanguage: "sl",
	}
	return NewLiteLLMProvider(cfg, testLogger()), srv
}

func TestChat_EmitsModelAttemptEvents(t *testing.T) {
	dir := t.TempDir()
	collage := filepath.Join(dir, "collage.jpg")
	_ = os.WriteFile(collage, []byte("fake"), 0o644)

	// gemini-1 always 429s (retryable) so the chain must fall through to gemini-2.
	p, _ := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "m-bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		writeChatReply(w, "an answer")
	})
	p.cfg.VLMModels = []string{"m-bad", "m-good"}

	var events []string
	ctx := WithEvents(context.Background(), func(level, msg string) {
		events = append(events, level+": "+msg)
	})
	if _, err := p.Chat(ctx, &models.Job{ID: "j1"}, nil, "hi", collage); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(events, "\n")
	for _, want := range []string{"trying m-bad", "m-bad failed, falling back", "trying m-good", "m-good answered"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing event containing %q; got:\n%s", want, joined)
		}
	}
}

func TestChat_FirstTurnAttachesCollageSecondTurnDoesNot(t *testing.T) {
	dir := t.TempDir()
	collage := filepath.Join(dir, "collage.jpg")
	if err := os.WriteFile(collage, []byte("fake-jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls []map[string]any
	p, _ := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		writeChatReply(w, "an answer")
	})

	job := &models.Job{ID: "j1", Filename: "clip.mp4"}

	if _, err := p.Chat(context.Background(), job, nil, "what happens first?", collage); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), job, []models.ChatMessage{
		{Role: models.ChatUser, Content: "what happens first?"},
		{Role: models.ChatAssistant, Content: "an answer"},
	}, "and then?", collage); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(calls))
	}
	if !requestHasImage(calls[0]) {
		t.Error("first turn (empty history) should attach the overview collage image")
	}
	if requestHasImage(calls[1]) {
		t.Error("second turn (non-empty history) should NOT re-attach the image")
	}
}

func requestHasImage(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		content, _ := msg["content"].([]any)
		for _, c := range content {
			part, _ := c.(map[string]any)
			if part["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}

func writeChatReply(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"content": content}, "finish_reason": "stop"}},
	})
}

func TestTranscribeOnce_SingleLanguageSendsLanguageField(t *testing.T) {
	var gotLang, gotPrompt string
	p, _ := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		gotLang = r.FormValue("language")
		gotPrompt = r.FormValue("prompt")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "zdravo"})
	})
	f := writeTempAudio(t)
	text, err := p.transcribeOnce(context.Background(), "w1", f, "sl")
	if err != nil {
		t.Fatal(err)
	}
	if text != "zdravo" || gotLang != "sl" || gotPrompt != "" {
		t.Fatalf("text=%q lang=%q prompt=%q", text, gotLang, gotPrompt)
	}
}

func TestTranscribeOnce_MultiCandidateSendsPromptNotLanguage(t *testing.T) {
	var gotLang, gotPrompt string
	p, _ := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		gotLang = r.FormValue("language")
		gotPrompt = r.FormValue("prompt")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello"})
	})
	f := writeTempAudio(t)
	if _, err := p.transcribeOnce(context.Background(), "w1", f, "en,sl"); err != nil {
		t.Fatal(err)
	}
	if gotLang != "" {
		t.Fatalf("multi-candidate hint must not force `language`, got %q", gotLang)
	}
	if !strings.Contains(gotPrompt, "English") || !strings.Contains(gotPrompt, "Slovenian") {
		t.Fatalf("expected a soft prompt steer naming both candidates, got %q", gotPrompt)
	}
}

func TestTranscribeOnce_NoHintSendsNeither(t *testing.T) {
	var gotLang, gotPrompt string
	p, _ := newTestLiteLLM(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		gotLang = r.FormValue("language")
		gotPrompt = r.FormValue("prompt")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": ""})
	})
	f := writeTempAudio(t)
	if _, err := p.transcribeOnce(context.Background(), "w1", f, ""); err != nil {
		t.Fatal(err)
	}
	if gotLang != "" || gotPrompt != "" {
		t.Fatalf("no hint should mean fully open auto-detect: lang=%q prompt=%q", gotLang, gotPrompt)
	}
}

func writeTempAudio(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "audio.webm")
	if err := os.WriteFile(f, []byte("fake-opus-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}
