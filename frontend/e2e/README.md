# Browser end-to-end test

`browser-pipeline.mjs` drives the **real Angular app in Chromium** against a
running instance and checks the whole client pipeline that `scripts/e2e.sh`
(API-only) can't reach: `<video>`/`<canvas>` macro-collage extraction,
`AudioContext`/`MediaRecorder` audio, `tus-js-client` segment uploads
(interesting-first), the timeline UI, and job completion.

## Run

```bash
# 1. have an instance running (any of):
npm run docker:build && docker compose up          # -> http://localhost:8080
#   or point at the deployed box:  export APP=http://your-deployed-host:8080

# 2. one-time browser download
npx playwright install chromium

# 3. run it (headed — the bundled headless *shell* lacks video decoders,
#    so on a server use xvfb)
APP=http://localhost:8080 xvfb-run -a node frontend/e2e/browser-pipeline.mjs
```

Needs `ffmpeg` on `PATH` (it builds a VP9/Opus WebM fixture — open-source
Chromium has no H.264) and `playwright` (`npm i -D playwright`).

## Why it exists

The first real run found two shipping bugs the unit/API tests missed:

- `frame-extractor.seek()` awaited `requestVideoFrameCallback`, which **never
  fires for a paused `<video>`** — `extract()` hung forever and the app stuck at
  phase `reading` with no job created.
- `engine.SubmitOverview` overwrote every segment's interest score, wiping a
  user's explicit pick when the async VLM overview landed.

## CI

Wired into `.github/workflows/ci.yml`'s `e2e` job as a deploy gate: after the
image is built, it's `docker run`, then this script runs against it with
`APP=http://localhost:8080` in a job that does
`npx playwright install --with-deps chromium`.
