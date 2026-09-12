# Deployment

This app ships as a single Docker image (Angular build + Go build + Debian
runtime with `ffmpeg`). This page covers what CI does automatically and how
you'd deploy the result yourself; [`deploy/README.md`](../deploy/README.md)
has a concrete Portainer-based example if you want one.

## What CI does

```
push to main
   │
   ▼
.github/workflows/ci.yml
   │
   ├─ test:            go vet + go test, npm ci, Vitest, ng build
   │
   ├─ build-and-push:   docker build/push -> ghcr.io/<owner>/<repo>
   │                     (tags :latest and :<sha12>)
   │
   └─ e2e:              scripts/e2e.sh (API-level smoke test against the just-
                         built image) THEN frontend/e2e/browser-pipeline.mjs
                         (real headed Chromium under xvfb — see testing.md)
                         against a container running that same image.
```

That's where CI stops — it publishes a working image, but doesn't deploy it
anywhere, since there's no single deployment target that makes sense for
every fork. See [`deploy/README.md`](../deploy/README.md) for one concrete
way to take it from there (Portainer), or just run the published image
directly:

```bash
docker run -d -p 8080:8080 \
  -e ANALYSIS_PROVIDER=litellm \
  -e LITELLM_BASE_URL=... -e LITELLM_API_KEY=... \
  -e LITELLM_VLM_MODEL=... -e LITELLM_WHISPER_MODEL=... \
  -v video_extractor_data:/data \
  ghcr.io/<owner>/<repo>:latest
```

Every CI run's `e2e` job exercises the `noop` analysis provider (GitHub-hosted
runners have no route to any particular AI gateway) — it still produces
non-empty placeholder story/segment text, so the pipeline-mechanics checks
(uploads, collage building, the timeline, chat, the activity log, the
previous-videos panel) are exercised for real; only "is the AI's output
actually good" is something you verify by hand against your own real
deployment with a real model configured.

## What persists across a restart

A single Docker named volume (`/data` in the container) holds uploads,
generated collages/archives, and — unless `MYSQL_DSN` is set — the local
SQLite job store. See [configuration.md](configuration.md) and
[previous-videos.md](previous-videos.md) for the SQLite-vs-MySQL tradeoff.

## Secrets

Whatever's forwarding config into your running container (a Portainer stack,
plain `docker run -e`, a `.env` file with `docker compose`) needs the env vars
in [configuration.md](configuration.md) — `LITELLM_*`/`IMMICH_*`/`MYSQL_DSN`
are the ones worth treating as secrets. However you manage them, keep the
values in exactly one place (your CI provider's secret store, or your
container platform's own secret/env mechanism) — never commit them to the
repo, even as a "just for now" default.
