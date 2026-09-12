# Architecture

## The problem

Analyzing a 2 GB+ video with a vision-language model (VLM) naively — sample
every frame, send everything — is slow and expensive, and the user has to
wait for the whole thing before seeing anything. video-extractor instead uses
a **coarse-to-fine** pipeline: get a cheap, fast, whole-video understanding
first, then spend the expensive per-segment analysis only where it's likely
to matter, in priority order.

## The three stages

```
┌─ Browser ────────────────────────────────┐     ┌─ Go backend (one container) ───────────┐
│ 1. <video>+<canvas> → macro collage      │──►  │ POST /overview → VLM story + interest  │
│    (16–24 stamped frames, ~1–2 MB)       │     │                  scores per segment    │
│ 2. WebAudio/MediaRecorder → Opus (~8 MB) │──►  │ POST /audio    → Whisper transcript    │
│ 3. tus upload, 1 upload / segment,       │──►  │ /files/*  (tusd) → on each segment:    │
│    interesting segments first            │     │   FFmpeg scene-detect + tile + drawtext│
└──────────────────────────────────────────┘     │   → granular contact sheet + VLM desc  │
                                                  │ reassemble parts → full archive        │
                                                  └─────────────────────────────────────────┘
```

### Stage 1 — macro collage (browser)

`frontend/src/app/core/frame-extractor.service.ts` seeks a `<video>` element
to 16–24 evenly spaced timestamps, draws each frame to a `<canvas>`, stamps a
human-readable timecode onto it, and tiles the frames into a single JPEG
("macro collage", ~1–2 MB) using the grid math in
`frontend/src/app/core/collage-plan.ts`. That collage plus its `FrameMeta[]`
grid is `POST`ed to `/api/v1/jobs/{id}/overview`. The VLM returns a coarse
story for the whole video (`Job.OverviewStory`) and an `interestScore`
(0–1) per timeline segment — this is what drives upload prioritization in
Stage 3.

`frame-extractor.seek()` resolves on the `<video>`'s `seeked` event (with a
short `setTimeout` nudge), not `requestVideoFrameCallback` — the latter never
fires for a paused `<video>`, which was a real shipping bug (see
[testing.md](testing.md)).

### Stage 2 — compressed audio (browser)

`frontend/src/app/core/audio-extractor.service.ts` uses `AudioContext` +
`MediaRecorder` to extract just the audio track as Opus/WebM (~5–10 MB, a
fraction of the source file), `POST`ed to `/api/v1/jobs/{id}/audio`. The
backend transcribes it with Whisper (`Job.Transcript`). This stage runs in
the background — it doesn't block Stage 3, and a failure here is non-fatal
(the job just has no transcript).

### Stage 3 — prioritized segment upload + granular analysis (backend)

The source video is split into fixed-length timeline segments
(`Job.Segments`, default 30s each). Instead of one big upload, **each
segment is its own independent resumable [tus](https://tus.io) upload**
(`frontend/src/app/core/tus-upload.service.ts`), started in priority order:
a user's explicit click > a high VLM `interestScore` from Stage 1 > plain
plan order. tus was chosen for resumability on flaky connections with 2 GB+
files; `tus-js-client` has no first-class arbitrary-order concatenation API,
so each segment stays a fully independent tus upload (tagged with
`jobId`/`segmentId`/`partIndex` metadata) and the Go engine reassembles parts
in index order for the full archive (`engine.concatParts`).

As each segment's bytes land (`engine.OnPartUploaded`), a bounded FFmpeg
worker pool (`FFMPEG_WORKERS`, default 2) builds a **granular** contact sheet
for just that slice: `select='gt(scene,0.3)'` scene-change detection,
`drawtext` timestamps, `tile` into a grid, falling back to a fixed `fps=2`
sample if scene detection finds too few frames. That collage goes to the VLM
for a focused per-segment description (`TimelineSegment.Description`).
`engine.maybeComplete` flips the job to `complete` once every segment is
`done` or `error` and the full file is assembled.

## Job lifecycle (state machine)

```
created ──► downloading* ──► overview_received ──► audio_received ──► analyzing ──► complete
                                                                                  └─► error
```

`downloading` only applies to `origin=immich` jobs (the backend is fetching
the asset itself); browser-upload jobs skip straight from `created` toward
`overview_received`/`audio_received` as Stage 1/2 responses land — these two
statuses aren't strictly ordered relative to each other, since the overview
and audio requests race. `analyzing` covers the Stage 3 granular-analysis
window. Each `TimelineSegment` has its own finer-grained status
(`pending → uploading → uploaded → processing → done|error`).

## Alternate origin: Immich import

`job.origin == "immich"` jobs skip the browser pipeline entirely — see
[immich-integration.md](immich-integration.md) for the full story. In short:
`POST /api/v1/jobs/from-immich` triggers `engine.IngestFromImmich`, which
downloads the asset server-side and builds Stage 1/2 itself with FFmpeg
(`ffmpeg.RunOverview` / `ffmpeg.ExtractAudio`, using the same grid math as
the browser's `collage-plan.ts`, mirrored in `planOverviewFrames`), then
calls `OnFullUploaded` — from there it's the identical Stage 3 pipeline a
completed browser upload converges on. No downstream code needs to know or
care which origin a job came from.

## Backend structure

| Package | Responsibility |
|---|---|
| `internal/api` | HTTP routes (chi router), request/response marshaling, the embedded Angular SPA and generated-media file server |
| `internal/engine` | The async orchestrator: tracks upload progress, drives the FFmpeg worker pool, calls the analysis provider, decides when a job is complete |
| `internal/tusd` | Wires up the tus resumable-upload protocol handler and its completion callbacks into `engine` |
| `internal/ffmpeg` | Shells out to `ffmpeg`/`ffprobe` for collage building, scene detection, audio extraction, dimension probing |
| `internal/analysis` | The `Provider` interface (`AnalyzeOverview`, `AnalyzeSegment`, `Transcribe`, `Chat`) plus two implementations: `litellm` (real AI, OpenAI-compatible gateway with fallback chains) and `noop` (canned responses, no external calls — used in CI and local dev without API keys) |
| `internal/immich` | Immich REST client: asset resolution by id or share link, download, description/metadata write-back |
| `internal/store` | Job persistence — one JSON document per job, driver-agnostic SQL (SQLite for local dev/tests, MySQL for production) |
| `internal/models` | The Go structs that mirror `shared/openapi.yaml` |
| `internal/web` | Embeds the built Angular `dist/` into the Go binary (`embed.FS`) |

## Frontend structure

Angular 20, standalone components (no `NgModule`s), signals for state
(`signal`/`computed`, no NgRx or similar). The whole client-side pipeline for
one job — extraction, job creation, upload orchestration, polling — is owned
by one injectable service, `JobStore`
(`frontend/src/app/core/job-store.ts`), which every component reads from as
signals.

| Path | What |
|---|---|
| `frontend/src/app/core` | Services (`JobStore`, `ApiService`, `FrameExtractorService`, `AudioExtractorService`, `TusUploadService`) and framework-free pure logic (`collage-plan.ts`, `priority-queue.ts`, `upload-priority.ts`, `immich-input.ts`, `format-event-time.ts`, `format-job-date.ts`) |
| `frontend/src/app/features/timeline` | `timeline`, `segment`, `collage-view`, `chat`, `event-log` components — everything about the *currently loaded* job |
| `frontend/src/app/features/history` | `previous-jobs` — the cross-job "Previous videos" browse/load-back panel |
| `frontend/src/app/models` | TypeScript mirror of `shared/openapi.yaml` |

Pure, framework-free logic (grid math, priority ordering, date formatting,
input parsing) is deliberately pulled out of components into standalone
functions in `core/`, each with a matching `*.spec.ts` — this is what
`npm --workspace frontend run test:ci` (Vitest) exercises; it never touches a
real browser. See [testing.md](testing.md) for why a *separate* real-browser
suite also exists.

## The wire contract

[`shared/openapi.yaml`](../shared/openapi.yaml) is the single source of truth
for every request/response shape. `backend/internal/models/*.go` and
`frontend/src/app/models/*.ts` are hand-kept mirrors of it — there's no code
generation step, so changing a field means updating all three by hand (and
usually a Go/TS build is enough to catch a missed spot).
