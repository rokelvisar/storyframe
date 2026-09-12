# Activity log

Every job carries a running activity log (visible on the page as the
"Activity log" panel, `vx-event-log`) — a human-readable trail of what the
pipeline is actually doing: download/collage/audio steps, per-segment
analysis, job completion, Immich write-back, and — the part that's otherwise
invisible — **which AI model answered each call**, including fallback
attempts when a model rate-limits or errors and the chain moves on to the
next one.

## Why it exists

The AI provider (`LITELLM_VLM_MODEL` / `LITELLM_WHISPER_MODEL`) is configured
as a comma-separated **fallback chain** — the request walks the list on any
429/5xx/`RateLimitError`/empty completion. Without visibility into that,
there's no way to tell from the UI whether a job's story came from the
primary model or the fourth fallback, or whether a model is silently failing
on every call. The activity log makes that visible per job, in real time.

## What gets logged

Two kinds of entries, both `models.JobEvent{Time, Level, Message}`
(`Level` is `info`/`warn`/`error`, color-coded in the UI):

1. **Pipeline steps**, logged directly by `engine.LogEvent` calls scattered
   through `internal/engine`: download progress, "building macro collage
   server-side", "extracting audio track server-side", per-segment "building
   granular collage" / "analyzing" / "description ready", job completion,
   Immich cleanup, and write-back start/success/failure.
2. **Per-model attempts**, emitted from *inside* the shared fallback loops in
   `backend/internal/analysis/litellm.go` — "Whisper: trying
   speaches-whisper-turbo", "VLM: gemini-free/gemini-3.6-flash failed,
   falling back", "VLM: gemini-paid/gemini-3.6-flash answered (767 chars)".

## How the plumbing works

The `Provider` interface (`AnalyzeOverview`, `AnalyzeSegment`, `Transcribe`,
`Chat`) doesn't know anything about job event logs — it wasn't changed to
accept an extra "log this" parameter. Instead, `analysis/events.go` defines a
tiny context-based callback:

```go
type EventFunc func(level, message string)
func WithEvents(ctx context.Context, fn EventFunc) context.Context
func emitEvent(ctx context.Context, level, format string, args ...any) // internal
```

`engine.withEvents(ctx, jobID)` wires an `EventFunc` that calls
`engine.LogEvent` into the context right before calling into the provider;
`litellm.go`'s shared fallback loops call `emitEvent(ctx, ...)` at each
attempt without needing to know who's listening (or whether anyone is — a
nil callback is a silent no-op, so calling a provider method without
`WithEvents` wired in, like from a test, just doesn't emit anything).

`engine.LogEvent(jobID, level, format, args...)` is the actual persistence
point: it appends to `Job.Events` via the store's read-modify-write
`UpdateJob`, truncates any single message over 500 characters, and caps the
whole slice at `maxJobEvents = 300` (oldest entries drop off first — this is
an activity trail, not an audit log, so unbounded growth isn't worth
supporting).

## Frontend

`vx-event-log` (`frontend/src/app/features/timeline/event-log.component.ts`)
reads `JobStore.events()` (a computed signal over `Job.events`), renders each
entry with a monospace timestamp and level-based color, and auto-scrolls to
the newest entry as the list grows (tracked via an `AfterViewChecked` length
check, not a full scroll-position heuristic — simpler and correct for a
log that only ever appends).
