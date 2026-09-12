# Configuration

All configuration is environment variables, read once at startup
(`backend/cmd/server/main.go`'s `loadConfig()` plus a handful of direct
`os.Getenv` calls). Nothing is reloaded at runtime — change an env var, restart
the container.

## Core

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DATA_DIR` | `/data` | Root for uploads, the local SQLite file (unless `MYSQL_DSN` is set), collages, and the assembled archive |
| `DB_PATH` | `$DATA_DIR/video-extractor.sqlite` | Local SQLite job-store file path. Ignored entirely when `MYSQL_DSN` is set |
| `FFMPEG_BIN` | `ffmpeg` | Path/name of the ffmpeg binary (also needs `ffprobe` on `PATH`) |
| `FFMPEG_WORKERS` | `2` | Size of the bounded worker pool that builds granular per-segment contact sheets |

## Job store

| Env | Default | Meaning |
|---|---|---|
| `MYSQL_DSN` | — | When set, jobs persist to this MySQL/MariaDB server instead of the local SQLite file. A [go-sql-driver/mysql DSN](https://github.com/go-sql-driver/mysql#dsn-data-source-name): `user:pass@tcp(host:3306)/dbname?parseTime=false`. See [previous-videos.md](previous-videos.md) for why you'd want this and the operational tradeoffs of switching an already-running deployment. |

## AI provider

| Env | Default | Meaning |
|---|---|---|
| `ANALYSIS_PROVIDER` | `litellm` | `litellm` (real AI calls) or `noop` (canned placeholder responses, no external calls — automatically used if `LITELLM_BASE_URL` isn't set, and what CI's e2e run uses) |
| `LITELLM_BASE_URL` | — | Base URL of an OpenAI-compatible gateway (e.g. a [LiteLLM](https://github.com/BerriAI/litellm) proxy) |
| `LITELLM_API_KEY` | — | API key for that gateway |
| `LITELLM_VLM_MODEL` | placeholder | Vision-language model id(s) — **comma-separated fallback chain**. The provider walks the list on any 429 / 5xx / `RateLimitError` / empty completion, advancing to the next entry. Used for both the Stage-1 overview and per-segment analysis. |
| `LITELLM_WHISPER_MODEL` | placeholder | Same fallback-chain shape, for transcription |

## Immich integration

See [immich-integration.md](immich-integration.md) for the full behavior.

| Env | Default | Meaning |
|---|---|---|
| `IMMICH_BASE_URL` | — | Your Immich instance's base URL. Enables `POST /jobs/from-immich` when set |
| `IMMICH_API_KEY` | — | An Immich API key with asset read **and write** access. Only strictly required for asset-id imports and for write-back — share-link imports use the link's own public key |
| `IMMICH_WRITEBACK` | `true` | Server-wide master switch for the write-back feature. `false` disables it entirely (no manual button works, no auto-writeback, regardless of per-job settings) |

## Language

See [language.md](language.md) for the full behavior.

| Env | Default | Meaning |
|---|---|---|
| `TRANSCRIBE_LANGUAGE_HINT` | `en,sl` | Server-wide default transcription language hint. A comma-separated candidate list becomes a soft Whisper `prompt` steer; a single code forces Whisper's `language` field. Overridable per job (`languageHint`) |
| `OUTPUT_LANGUAGE` | `sl` | Server-wide default language the VLM writes generated text in (story, segment descriptions, chat replies). Overridable per job (`outputLanguage`) |

## A minimal local run

```bash
ANALYSIS_PROVIDER=noop docker compose up
```

No external calls, no API keys needed — good for exercising the pipeline
mechanics (uploads, collage building, timeline UI) without AI costs. This is
also what `scripts/e2e.sh` and `frontend/e2e/browser-pipeline.mjs` run
against in CI.

## A full production run

See [deployment.md](deployment.md) for the full list of these that a
production deployment typically needs to configure (LiteLLM gateway, Immich
instance, MySQL server).
