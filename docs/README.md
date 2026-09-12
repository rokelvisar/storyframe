# video-extractor documentation

This folder is the deep-dive documentation for video-extractor. The top-level
[`README.md`](../README.md) is a quickstart; this folder explains *how* and
*why* each piece works, for anyone extending the app or operating it in
production.

| Doc | Covers |
|---|---|
| [architecture.md](architecture.md) | The 3-stage pipeline, job lifecycle/state machine, backend and frontend structure |
| [api.md](api.md) | REST endpoint reference (full contract lives in [`shared/openapi.yaml`](../shared/openapi.yaml)) |
| [configuration.md](configuration.md) | Every environment variable, what it does, and its default |
| [immich-integration.md](immich-integration.md) | Importing from Immich, storage retention policy, writing analysis back onto the source asset |
| [chat.md](chat.md) | Asking follow-up questions about a video |
| [activity-log.md](activity-log.md) | The per-job pipeline/AI-model activity trail |
| [previous-videos.md](previous-videos.md) | Browsing/reloading past jobs, the MySQL vs. SQLite job store |
| [language.md](language.md) | Transcription language hints and VLM output language |
| [deployment.md](deployment.md) | What CI does, what a deployment needs, an optional Portainer-based example ([`deploy/README.md`](../deploy/README.md)) |
| [testing.md](testing.md) | Test strategy: Go unit tests, Vitest, API-level e2e, real-browser e2e, and why each exists |

## The one-paragraph version

video-extractor turns a large video (2 GB+) into a VLM-written story plus a
scored, browsable timeline, without requiring the whole file to be analyzed at
once. A cheap **macro collage** (a handful of frames tiled into one image)
gets the VLM a coarse understanding and an interest score per timeline
segment in seconds; only the segments worth a closer look get a **granular**
per-segment analysis, and those uploads happen in priority order so the
highest-interest parts finish first. The source video can come from a local
file picked in the browser, or be imported directly from a self-hosted Immich
instance by asset id or share link. Every job — its story, transcript, chat
history, and activity log — is durable and browsable later from the
"Previous videos" panel.

## Where things live

```
backend/            Go server: REST API, tus resumable uploads, FFmpeg
                     orchestration, job store, AI provider abstraction
frontend/            Angular 20 SPA: file/Immich picker, timeline, chat,
                     activity log, previous-videos panel
shared/openapi.yaml  Source of truth for the wire format — backend and
                     frontend models are hand-kept in sync with it
deploy/              Optional Portainer-based deploy example + runbook
docs/                You are here
```

See [`AGENTS.md`](../AGENTS.md) in the repo root for terse, implementation-level
pointers (file names, function names) aimed at an agent working in the code;
this `docs/` folder is the narrative version aimed at a human (or an agent)
trying to understand *why* the system is shaped the way it is.
