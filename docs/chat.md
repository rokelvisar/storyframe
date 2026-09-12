# Chat

Once a job's overview story is ready, the "Ask about this video" panel lets
you ask follow-up questions about it — `POST /jobs/{id}/chat`
(`frontend/src/app/features/timeline/chat.component.ts`).

## What it's grounded in

The system prompt (`chatSystemPrompt` in `backend/internal/analysis/litellm.go`)
is built from what the pipeline has already produced — no separate retrieval
step, no re-analysis of the video:

- `Job.OverviewStory` — the coarse whole-video story from Stage 1.
- `Job.Transcript` — the Whisper transcript from Stage 2.
- Every segment's `StartSec`/`EndSec`/`Description` (segments without a
  description yet are skipped, not sent as empty bullets).

On the **first** chat turn only, the macro collage image is attached too
(the same `dataURI()` + `image_url` mechanism `AnalyzeOverview` uses) — one
extra shot of visual grounding, not repeated on every message since the text
context alone covers most questions cheaply.

There's no per-question lookup of granular per-segment collages — chat is
text-first by design, trading a small amount of visual precision for speed
and cost.

## How it flows

1. `POST /jobs/{id}/chat` with `{"message": "..."}`. Requires
   `Job.OverviewStory` to already be set — `409` otherwise (chat isn't useful
   before there's anything to ground answers in).
2. The handler appends the user's message to `Job.ChatMessages`
   **synchronously** — it's reflected in the very same response, so the UI
   can render the question immediately.
3. `engine.SubmitChat` then answers it **asynchronously** in a goroutine (the
   provider call can take several seconds), appending the assistant's reply
   to `Job.ChatMessages` when it's done.
4. The frontend picks up the reply by polling. This deserves a callout: chat
   is normally used *after* a job is already `complete`, at which point
   `JobStore`'s main 2s poll loop has already stopped (it only runs while a
   job is in flight — see [architecture.md](architecture.md)'s job lifecycle).
   So `sendChatMessage` runs its **own** short-lived poll
   (`waitForChatReply`, up to 60s) instead of assuming the main loop is still
   running. This was a real shipping bug caught only by the real-browser e2e
   test — see [testing.md](testing.md).

## Provider abstraction

`Provider.Chat(ctx, job, history, question, collagePath)` is one more method
on the same `analysis.Provider` interface as `AnalyzeOverview`/`AnalyzeSegment`.
`LiteLLMProvider.Chat` reuses the exact same fallback-chain `chat()` plumbing
those already use (see [activity-log.md](activity-log.md) for how model
attempts get surfaced) — no separate HTTP client or retry logic was written
for chat. `NoopProvider.Chat` returns a canned "chat needs
`ANALYSIS_PROVIDER=litellm`" message rather than erroring, so the UI stays
functional (if unhelpful) without real AI configured.

## Language

Chat replies respect the job's `OutputLanguage` (default `sl`, per-job
override `outputLanguage`) — the same "Respond in {language}." instruction
appended to the overview/segment system prompts is appended to the chat one.
See [language.md](language.md).
