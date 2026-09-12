# AGENTS.md — video-extractor

Agentic-session guidance for this repo. Jump straight to the task; don't recap the
architecture back to the user unprompted.

## What this is

A single Docker image that does **coarse-to-fine, asynchronous VLM storytelling
analysis of large videos (2 GB+)**:

1. **Stage 1 (browser):** `<video>` + `<canvas>` sample 16–24 evenly spaced frames,
   stamp each with its timestamp, tile them into one macro collage (~1–2 MB) +
   a `FrameMeta[]` grid. `POST /api/v1/jobs/{id}/overview` → the VLM writes a coarse
   story and an `interestScore` per timeline segment.
2. **Stage 2 (browser):** Web Audio → `MediaRecorder` extracts an Opus/WebM track
   (~5–10 MB). `POST /api/v1/jobs/{id}/audio` → Whisper transcript.
3. **Stage 3 (backend):** the file is uploaded as **one resumable tus upload per
   timeline segment**, highest-priority (user pick > VLM interest > plan order)
   first. When a segment's bytes land, the Go backend runs FFmpeg for a granular
   contact sheet of just that slice (`select='gt(scene,0.3)'` + `drawtext` + `tile`,
   `fps=2` fallback). Parts are reassembled in index order for the full archive.

**Alternate origin — Immich import (`job.origin == "immich"`):** `POST
/api/v1/jobs/from-immich` (asset id or a public share link) skips the browser
entirely. Immich sends no CORS headers, so `internal/immich` downloads the asset
server-side, then `engine.IngestFromImmich` builds Stage 1/2 itself with FFmpeg
(`ffmpeg.RunOverview` / `ffmpeg.ExtractAudio`, grid math mirrored from
`collage-plan.ts` in `planOverviewFrames`) and calls `OnFullUploaded` — from
there it's the identical pipeline a completed upload converges on (same
segment/timeline/VLM code paths, no special-casing downstream). Once complete,
`engine.maybeComplete` deletes the raw video for `origin=immich` jobs (kept
for `origin=upload` — it's the only copy); derived artifacts (collages, audio,
chat) always survive. It can also (best-effort, own goroutine) write the
analysis back onto the source asset: story into Immich's `description`
(marker-delimited so re-analysis replaces it in place rather than appending
forever — see `stripPriorSummary`/`buildDescription`), full structured
analysis (story/transcript/segments) into Immich's custom per-asset metadata
sidecar under key `"video-extractor"` (`PUT /assets/{id}/metadata`) — a real,
verified Immich mechanism distinct from `description`, found via
`/api/spec.json` (its OpenAPI spec; `/api/doc-json` etc. 404 on this
instance). `IMMICH_WRITEBACK` (default on) is the server-wide master switch;
per-job it's **manual by default** — `Job.ImmichAutoWriteback` (request field
`autoWriteback`) opts a job into automatic write-back on completion,
otherwise the user triggers it with `POST /jobs/{id}/immich-writeback`
(`engine.TriggerImmichWriteback`, same underlying `writeBackToImmich`
goroutine as the automatic path). `Job.ImmichWrittenBack` flips true once a
write-back (either path) has completed.

**Chat (`POST /jobs/{id}/chat`):** grounded in `OverviewStory` + `Transcript` +
every segment's `Description`, plus the macro collage image on the first turn
only. `Provider.Chat` reuses the same fallback-chain `chat()` plumbing as
overview/segment analysis — no separate HTTP client.

**Language:** every job carries `TranscribeLanguageHint` (drives Whisper's
`language` field for a single code, or a `prompt` steer for a candidate list
like the `en,sl` default — see `transcribeOnce`) and `OutputLanguage` (appended
to every LiteLLM system prompt: overview, segment, chat). Both default from
`TRANSCRIBE_LANGUAGE_HINT` / `OUTPUT_LANGUAGE` env and can be overridden per
job at create time.

**Activity log (`Job.Events`, capped at `maxJobEvents`=300):** `engine.LogEvent`
persists one entry per pipeline step (download, collage/audio build, per-segment
analysis, completion, write-back) directly; per-model attempts (Whisper/VLM
"trying X" / "X failed, falling back" / "X answered") are emitted from inside
`analysis/litellm.go`'s shared fallback loops via a context-carried callback
(`analysis.WithEvents`/`emitEvent`, wired per-call by `engine.withEvents`) so the
`Provider` interface itself stays untouched. Frontend: `vx-event-log`
(`frontend/src/app/features/timeline/event-log.component.ts`), reads
`JobStore.events()`, auto-scrolls on new entries.

**Previous videos (`GET /jobs`, `Store.ListJobs`):** `internal/store` is
driver-agnostic — every method uses `?` placeholders that work unmodified
against both `modernc.org/sqlite` (`store.Open`, local file, zero infra, used
by every test in the repo) and `go-sql-driver/mysql` (`store.OpenMySQL`, used
in production when `MYSQL_DSN` is set — see `cmd/server/main.go`'s branch).
`ListJobs` unmarshals the same per-job JSON blob `GetJob` does, projected down
to `models.JobSummary` (thumbnail/filename/origin/status/dates only) so
browsing history doesn't ship every job's full segments/events/chat. Frontend:
`vx-previous-jobs` (`frontend/src/app/features/history/previous-jobs.component.ts`)
lists thumbnails, `JobStore.loadJob(id)` fetches one back into the normal job
view (resumes polling only if it's somehow still non-terminal). If you run
this against a real MySQL/MariaDB server, use a scoped, non-root user with
access to just that one database — the DSN (with its password) belongs only
in your deployment's secret store, never in the repo.

## Layout

| Path | What |
|---|---|
| `shared/openapi.yaml` | **source of truth** for the wire models |
| `backend/internal/models` | Go mirror of the models — keep in sync by hand |
| `frontend/src/app/models` | TS mirror — keep in sync by hand |
| `backend/internal/{api,engine,tusd,ffmpeg,analysis,store,web,immich}` | server halves |
| `frontend/src/app/core` | services + framework-free logic (`collage-plan.ts`, `priority-queue.ts`, `upload-priority.ts`, `immich-input.ts`) |
| `frontend/src/app/features/timeline` | `timeline`/`segment`/`collage-view`/`chat`/`event-log` components |
| `frontend/src/app/features/history` | `previous-jobs` (the "Previous videos" browse/load-back panel) |
| `deploy/` | Optional Portainer-based deploy example (stack file + script + runbook) |

## Build / test

```
# backend
cd backend && go vet ./... && go test ./...

# frontend (pure-logic unit tests via Vitest; no browser needed)
npm ci && npm --workspace frontend run test:ci && npm --workspace frontend run build

# whole image + API-level end-to-end smoke (needs docker, ffmpeg, curl, jq)
npm run docker:build
scripts/e2e.sh

# real-browser end-to-end (needs ffmpeg, `npx playwright install chromium`; run
# headed under xvfb-run on a server — the bundled headless *shell* can't decode
# video). Drives the actual Angular app: <video>/<canvas> collage extraction,
# MediaRecorder audio, tus-js-client uploads, the timeline UI, job completion.
APP=http://localhost:8080 xvfb-run -a node frontend/e2e/browser-pipeline.mjs
```

`scripts/e2e.sh` and Vitest never touch a browser — they bypass `frame-extractor`
/ `audio-extractor` / `tus-upload` entirely (ffmpeg + curl build the collage and
push chunks over raw tus). Only `frontend/e2e/browser-pipeline.mjs` exercises
those. It exists because the first real run caught two shipping bugs neither
of the above could: `frame-extractor.seek()` hanging forever on
`requestVideoFrameCallback` for a paused `<video>` (app stuck at phase
`reading`, no job ever created), and the VLM overview clobbering a user's
explicit interest-score pick. See `frontend/e2e/README.md`.

## Deploy

Push to `main` → `.github/workflows/ci.yml`: `go test` + Vitest + Angular
build, then build & push `ghcr.io/<owner>/<repo>:{latest,<sha12>}`. CI stops
there — see [`docs/deployment.md`](docs/deployment.md) and
[`deploy/README.md`](deploy/README.md) for how to actually deploy the
published image (an optional Portainer-based example is included, but
`docker run`/`docker compose` the image directly works too).

If you front this with a reverse proxy, remember uploads are large (2 GB+)
and long-running — e.g. for Nginx-based proxies:
`client_max_body_size 0; proxy_request_buffering off; proxy_read_timeout
3600s;` for the tus `PATCH` bodies.

## Deliberate deviations from the original brief

- **tus concatenation:** `tus-js-client` has no first-class Concatenation API with
  arbitrary ordering, so each segment is its own independent complete tus upload
  (metadata `jobId`/`segmentId`/`partIndex`); the Go engine reassembles parts in
  index order. Priority ordering is preserved (it's the order uploads start in).
- **npm registry:** there is none. "Publish" = the ghcr.io image CI builds on
  every push (see the Deploy section above). Root `package.json` is Angular
  workspace tooling only.
