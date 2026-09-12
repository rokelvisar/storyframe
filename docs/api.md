# API reference

The full, authoritative contract — every field, every type, every enum — is
[`shared/openapi.yaml`](../shared/openapi.yaml). This page is a human-readable
tour of the endpoints and how they fit together; when the two disagree,
`openapi.yaml` wins (and it's a bug worth fixing).

All endpoints are under `/api/v1`. tus resumable uploads live at `/files/*`
(outside `/api/v1`, since tus is its own protocol). Generated media (collages,
the assembled archive) are served as static files under `/media/*`.

## Creating a job

| Endpoint | Body | Notes |
|---|---|---|
| `POST /jobs` | `CreateJobRequest` | Local-file path. Returns `CreateJobResponse` — the created `Job` plus a `tusEndpoint` to start uploading segments to. |
| `POST /jobs/from-immich` | `CreateJobFromImmichRequest` | Immich-import path. Exactly one of `assetId` / `shareLink` required. Returns `CreateJobResponse` with **no** `tusEndpoint` — there's no browser upload for this origin, the backend fetches the asset itself. See [immich-integration.md](immich-integration.md). |

Both request shapes accept optional `languageHint` / `outputLanguage`
overrides (see [language.md](language.md)); `CreateJobFromImmichRequest` also
accepts `autoWriteback` (see [immich-integration.md](immich-integration.md)).

## Reading jobs

| Endpoint | Notes |
|---|---|
| `GET /jobs/{id}` | The full `Job` — every field, every segment, the full chat history and activity log. This is what the frontend polls every 2s while a job is in flight. |
| `GET /jobs` | `ListJobsResponse` — every job ever created, newest first, as lightweight `JobSummary` objects (thumbnail/filename/origin/status/dates only — no segments/events/chat). Optional `?limit=` (default 100, max 500). Powers the "Previous videos" panel; see [previous-videos.md](previous-videos.md). |

## Driving Stage 1/2 (browser-upload jobs only)

| Endpoint | Body | Notes |
|---|---|---|
| `POST /jobs/{id}/overview` | `multipart/form-data`: `collage` (JPEG) + `meta` (`OverviewMeta` JSON) | The macro collage from Stage 1. Triggers `engine.SubmitOverview` — the VLM call runs asynchronously; poll `GET /jobs/{id}` for `overviewStory` to land. |
| `POST /jobs/{id}/audio` | `multipart/form-data`: `audio` (WebM) | The compressed audio track from Stage 2. Triggers `engine.SubmitAudio` (Whisper transcription), also async. |

For Immich-origin jobs, these two are called by the backend itself
(`engine.IngestFromImmich`) — there's nothing for a client to POST.

## Driving Stage 3 (segment upload + granular analysis)

Segments are uploaded via the tus protocol at `/files/*` (see
[tus.io](https://tus.io) for the protocol itself); `tusd` completion
callbacks feed into `engine.OnPartUploaded` / `engine.OnFullUploaded`.

| Endpoint | Body | Notes |
|---|---|---|
| `POST /jobs/{id}/segments/{segmentId}/process` | `ProcessSegmentRequest` (optional) | Manually flag a segment as interesting — bumps its upload priority and, once its bytes are in, triggers granular analysis even if the VLM's own `interestScore` was low. This is what a user clicking a timeline segment does. |
| `POST /jobs/{id}/finalize` | — | Called once all segment uploads finish; mostly a signal for bookkeeping since `maybeComplete` already reacts to segment status changes. |

## Chat

| Endpoint | Body | Notes |
|---|---|---|
| `POST /jobs/{id}/chat` | `ChatRequest` (`{"message": "..."}`) | Only available once `overviewStory` is set (`409` otherwise). Appends the question synchronously, appends the assistant's reply asynchronously — poll `GET /jobs/{id}` (or use `chatMessages.length` growing) to pick it up. See [chat.md](chat.md). |

## Immich write-back

| Endpoint | Notes |
|---|---|
| `POST /jobs/{id}/immich-writeback` | Manually (re-)trigger writing the finished analysis back onto the source Immich asset. `400` if the job isn't `origin=immich` or write-back isn't enabled server-wide. Runs best-effort in the background — poll `GET /jobs/{id}` for `immichWrittenBack` to flip `true`. See [immich-integration.md](immich-integration.md). |

## Error shape

Errors are a plain `{"error": "message"}` JSON body with an appropriate HTTP
status (`400` bad request, `404` unknown job, `409` conflict — e.g. chat not
ready yet, `500`/`502` server or upstream failure). There's no structured
error-code enum; match on status code and treat the message as
human-readable, not machine-parsed.

## Key request/response shapes

These are the ones worth knowing off the top of your head; see
`shared/openapi.yaml` for the complete, exact definitions.

- **`Job`** — the whole unit of work: identity, status, every segment, the
  overview story, transcript, chat history, activity log, language settings,
  Immich linkage/write-back state, and the archive URL (or lack of one — see
  [immich-integration.md](immich-integration.md) on storage retention).
- **`TimelineSegment`** — one contiguous slice: its byte range, upload
  progress, interest score/source (`plan`/`user`/`ai`), granular collage URL,
  scene-change timestamps, and description.
- **`JobSummary`** — the lightweight projection `GET /jobs` returns; enough
  to render a "Previous videos" card without shipping the whole job.
- **`ChatMessage`** — one turn (`role`: `user`/`assistant`, `content`,
  `createdAt`).
- **`JobEvent`** — one activity-log line (`time`, `level`:
  `info`/`warn`/`error`, `message`). See [activity-log.md](activity-log.md).
