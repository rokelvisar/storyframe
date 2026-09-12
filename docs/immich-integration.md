# Immich integration

[Immich](https://immich.app/) is a self-hosted photo/video library. video-extractor
can pull a video straight from your own Immich instance instead of requiring a
browser file upload, and can optionally push the finished analysis back onto
the source asset.

## Importing

Paste an Immich **asset id** or a **share link**
(`https://<host>/share/<key>`) into the "Import from Immich" box instead of
picking a local file — `POST /api/v1/jobs/from-immich`
(`CreateJobFromImmichRequest`).

Immich's API sends no CORS headers, so this is entirely **server-side**:
`internal/immich` resolves the asset —

- by id: `GET /api/assets/{id}` with your `IMMICH_API_KEY` (`x-api-key`
  header) — needs read access on that key.
- by share link: `GET /api/shared-links/me?key=<key>` — public, no API key
  needed, but subject to the link's own `allowDownload` flag. A share link
  covering several assets needs `assetId` alongside it to disambiguate which
  one to import (otherwise the first `VIDEO` asset wins).

— then streams the original down and hands off to `engine.IngestFromImmich`,
which builds Stage 1 (macro collage) and Stage 2 (audio) itself with FFmpeg
(no browser involved at all), then converges on the exact same Stage 3
pipeline a completed browser upload uses. See
[architecture.md](architecture.md) for the pipeline details — downstream code
doesn't special-case `origin=immich` at all.

Both real REST discovery quirks worth knowing if you're debugging this:

- Immich's OpenAPI spec lives at `/api/spec.json` on a live instance —
  `/api/doc-json`, `/api/openapi.json`, `/api/swagger.json` all 404.
- The raw `duration` field on an asset is bare milliseconds, not an
  `HH:MM:SS` string.

## Storage retention

Once a job completes, the policy differs by origin:

- **`origin=upload`** (local file): the assembled archive is kept — it's the
  *only* copy that exists anywhere, so `Job.archiveUrl` stays set and
  downloadable.
- **`origin=immich`**: the original is already safe in your Immich library
  (`Job.immichAssetId`), so keeping a second copy locally would just be
  wasted disk. `engine.maybeComplete` deletes the raw video
  (`/data/immich/{jobId}` staging dir and `/data/assembled/{jobId}`) once
  analysis is done — `Job.archiveUrl` is left unset, and the frontend shows
  "Original in Immich (asset `{id}`)" as plain text instead of a download
  link. Every *derived* artifact survives: the macro collage, every granular
  collage, the extracted audio, chat history, the activity log — small, and
  the whole point of the UI.

## Writing the analysis back onto the source asset

For `origin=immich` jobs, the finished analysis can also be written back onto
the asset in your own Immich, so it's visible without ever opening
video-extractor again. Two distinct, verified Immich mechanisms are used:

1. **Description** (`PUT /api/assets/{id}` `{"description": "..."}`) — the
   human-readable story, visible right in Immich's own web UI asset-info
   panel. It's wrapped in a marker block
   (`--- video-extractor AI summary (job <id>, <date>) --- ... --- end
   video-extractor summary ---`) so re-analyzing the same asset **replaces**
   that block in place instead of piling up duplicate summaries on every
   re-import — while leaving any of your own free-text description
   completely untouched (`stripPriorSummary`/`buildDescription` in
   `backend/internal/engine/immich_writeback.go`).
2. **Custom asset metadata** (`PUT /api/assets/{id}/metadata`
   `{"items":[{"key":"video-extractor","value":{...}}]}`) — a genuine
   free-form key/value sidecar, separate from `description`, holding the
   *full* structured analysis: story, transcript, and every segment's
   `startSec`/`endSec`/`interestScore`/`description`. Queryable back via
   `GET /api/assets/{id}/metadata/video-extractor` — for any other tooling
   that wants to consume it.

Both calls need the API key to have **write** access, not just read.

### Manual by default, opt-in automatic

Write-back is **manual by default**: once a job is complete, click "Write
back to Immich" on the page (or `POST /jobs/{id}/immich-writeback`). This is
deliberate — the app shouldn't silently modify your photo library's metadata
without you asking it to.

To have it happen automatically on completion instead, check "Auto
write-back to Immich" before importing (`CreateJobFromImmichRequest.autoWriteback`
→ `Job.immichAutoWriteback`). `Job.immichWrittenBack` tracks whether a
write-back (manual or automatic) has completed at least once — the frontend
button label flips from "Write back to Immich" to "Re-write to Immich" once
it has.

There's also a server-wide master switch, `IMMICH_WRITEBACK` (default
`true`): set it `false` to disable the write-back feature entirely,
regardless of any per-job setting — the manual button will refuse with a
`400` and the auto path is never even attempted.

Write-back is **best-effort**: it runs in its own goroutine, never blocks or
affects job completion, and a failure (network blip, Immich down) is logged
but doesn't surface as a job error. If the overview story hasn't landed yet
when write-back starts, it polls for up to 60s before giving up (job
completion itself doesn't wait on the overview goroutine, since segment
completion and overview analysis are independent async paths — see
[architecture.md](architecture.md)).
