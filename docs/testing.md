# Testing strategy

Four layers, each catching a different class of bug, deliberately not
collapsed into one:

```
go test               → backend logic, in isolation, fast
Vitest                → frontend pure logic, in isolation, fast, no browser
scripts/e2e.sh         → whole container, API-only, no browser
browser-pipeline.mjs   → whole container, real Chromium, the actual app
```

## `go test` — backend unit tests

```bash
cd backend && go vet ./... && go test ./...
```

Every package has its own `_test.go` files. A few patterns worth knowing:

- `internal/store` tests use `store.Open(tmpfile)` — a throwaway local
  SQLite file, zero external infra. This is deliberate: `internal/store` is
  driver-agnostic (see [previous-videos.md](previous-videos.md)), so the same
  SQL is exercised whether the test runs against SQLite or the production
  MySQL backend would — no MySQL service container needed in CI.
- `internal/engine` tests that need a full pipeline run (e.g.
  `TestIngestFromImmich_EndToEnd`) spin up a fake Immich server with
  `httptest.NewServer` and shell out to **real** `ffmpeg`/`ffprobe` on a tiny
  synthetic fixture (`ffmpeg -f lavfi -i testsrc2=... -i sine=...`) — skipped
  if `ffmpeg` isn't on `PATH`.
- `internal/analysis` tests fake the LiteLLM HTTP endpoint directly
  (`httptest.NewServer`) to exercise the fallback-chain logic (e.g.
  `TestChat_EmitsModelAttemptEvents` makes the first model always 429 and
  asserts the chain falls through to the second).

## Vitest — frontend pure logic

```bash
npm ci && npm --workspace frontend run test:ci
```

Only exercises **framework-free** logic pulled out of components into
standalone functions in `frontend/src/app/core/` — grid math
(`collage-plan.ts`), upload prioritization (`priority-queue.ts`,
`upload-priority.ts`), Immich input parsing (`immich-input.ts`), date/time
formatting (`format-event-time.ts`, `format-job-date.ts`). No Angular
`TestBed`, no simulated DOM, no browser — these run in milliseconds. This is
the pattern to follow when adding a new pure-logic helper: write it as a
standalone function with a matching `*.spec.ts`, and keep the component
itself a thin wrapper that calls it.

## `scripts/e2e.sh` — API-level container smoke test

```bash
scripts/e2e.sh [--keep] [--image IMG] [--port PORT]
```

Builds (or reuses, with `--image`) the real Docker image, starts a real
container, and drives the whole pipeline with `curl`/`jq`/`ffmpeg` at the raw
HTTP/tus level: create job → POST overview collage → POST audio → tus-upload
every segment → poll until `complete` → assert every segment produced a
granular collage on disk. This is what CI's `e2e` job runs first, against the
image `build-and-push` just produced.

It bypasses the browser entirely — no `frame-extractor`, `audio-extractor`,
or `tus-js-client` code ever runs. That's the gap the next layer fills.

## `frontend/e2e/browser-pipeline.mjs` — real-browser end-to-end

```bash
# one-time: npx playwright install chromium
APP=http://localhost:8080 xvfb-run -a node frontend/e2e/browser-pipeline.mjs
```

Drives the **actual Angular app in real Chromium**: picks a synthetic video
fixture via `input[type=file]`, waits for the real `<video>`/`<canvas>` macro
collage extraction and `AudioContext`/`MediaRecorder` audio extraction to
run, clicks a timeline segment, watches real `tus-js-client` uploads happen,
asks a real chat question through the UI, and asserts on what's actually
rendered in the DOM (not just what the API returned).

Needs `ffmpeg` on `PATH` (builds a VP9/Opus WebM fixture — the open-source
Chromium build has no H.264 decoder) and runs **headed** under `xvfb-run` on
a headless server, since the bundled Playwright headless *shell* can't decode
video at all.

### Why this layer exists — it isn't redundant with the other three

The first real run of this suite caught two bugs that every other layer
missed entirely, because they only manifest with a real browser's actual
video/audio decoding and event timing:

1. `frame-extractor.seek()` awaited `requestVideoFrameCallback`, which
   **never fires for a paused `<video>`** — `extract()` hung forever and the
   app stuck at phase `reading`, no job ever created. `go test`, Vitest, and
   `scripts/e2e.sh` all passed the whole time, because none of them ever ran
   real browser video decoding.
2. `engine.SubmitOverview` unconditionally overwrote every segment's
   `interestScore` when the async VLM overview landed, silently wiping out a
   user's explicit click. Only visible by actually clicking a segment in a
   real page and checking what the timeline shows afterward.

A third, later bug (chat replies never rendering once the main poll loop had
already stopped — see [chat.md](chat.md)) was also only caught this way: an
API-level check that the reply exists server-side wouldn't have noticed that
nothing in the browser was polling to pick it up.

**The standing rule this established:** any client-facing/UI feature gets
driven in a real browser — headed Chromium under `xvfb`, not just a unit or
API-level test — before being considered done. `scripts/e2e.sh` proves the
backend pipeline works; only `browser-pipeline.mjs` proves the *product*
works.

### Wired into CI as a merge gate

`.github/workflows/ci.yml`'s `e2e` job runs both `scripts/e2e.sh` and
`browser-pipeline.mjs` against the freshly built image on every push to
`main` — see [deployment.md](deployment.md). GitHub-hosted runners have no
route to any particular AI gateway, so this CI run always exercises
`ANALYSIS_PROVIDER=noop`; real-AI-quality verification (are the VLM's
answers actually good) is necessarily a separate, manual check against
whatever real deployment you point at a real model.
