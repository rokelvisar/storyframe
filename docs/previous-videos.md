# Previous videos & the job store

Every job ever created — regardless of origin, regardless of whether it
finished successfully — shows up in the "Previous videos" panel
(`vx-previous-jobs`, thumbnail + filename + origin + date, newest first).
Clicking one reloads it exactly as it was left: the story, transcript, every
segment's description, the chat history, the activity log — without
re-running any analysis.

## API

`GET /api/v1/jobs` (`Store.ListJobs`) returns `ListJobsResponse` — an array
of lightweight `JobSummary` objects (id, filename, status, origin,
durationSec, overviewCollageUrl, overviewStory, createdAt, updatedAt), not
full `Job` objects. This matters at scale: a job's full JSON includes every
segment, the whole chat history, and up to 300 activity-log entries, which
would make browsing history expensive. `ListJobs` unmarshals the same
per-job JSON blob `GetJob` does and projects it down before returning —
newest first, `?limit=` capped (default 100, max 500).

## Frontend

`vx-previous-jobs` (`frontend/src/app/features/history/previous-jobs.component.ts`)
fetches the list on init, renders a grid of thumbnail cards, and calls
`JobStore.loadJob(id)` on click — which fetches the *full* `Job` via
`GET /jobs/{id}` and sets it as the current job. If the job is somehow still
non-terminal (e.g. the page was reloaded mid-Immich-import), `loadJob`
resumes the normal 2s poll loop; otherwise it just displays the finished
state directly, with zero extra network chatter — verified in a real browser
that clicking a card fires no `POST /jobs` request, only the one `GET`.

## Job store: SQLite or MySQL

`internal/store` persists one JSON document per job (a deliberately simple
design — the many small mutations coming from tus completion callbacks and
FFmpeg workers are just read-modify-write cycles guarded by an in-process
lock, `store.UpdateJob`). It's **driver-agnostic**: every SQL statement uses
`?` placeholders, which both `modernc.org/sqlite` and
`github.com/go-sql-driver/mysql` accept unmodified, so `CreateJob`/`GetJob`/
`UpdateJob`/`ListJobs` needed zero branching to support a second backend.

- **`store.Open(path)`** — a local SQLite file. Zero external infra, what
  every Go test in the repo uses (`t.TempDir()` + a throwaway file), and the
  default for local dev (`docker compose up` with no `MYSQL_DSN` set).
- **`store.OpenMySQL(dsn)`** — an existing MySQL/MariaDB server. Used in
  production: `cmd/server/main.go` picks this when the `MYSQL_DSN` env var is
  set, falling back to `store.Open` otherwise.

Why MySQL in production: job history then survives a container/volume loss
(SQLite's file lives inside a Docker named volume — durable, but tied to that
one volume), and becomes queryable from outside the app with any MySQL
client, e.g. alongside other MySQL-backed services you already run. Neither
backend is required to get the "Previous videos" feature itself — it works
identically on plain SQLite — `MYSQL_DSN` is purely about *where* that same
data lives.

### If you switch backends on an already-running deployment

The job store is picked once at startup based on whether `MYSQL_DSN` is set
— there's no automatic migration between the two. Flipping a running
deployment from SQLite to MySQL (or back) means jobs created under the old
backend stop showing up in `GET /jobs` / `GET /jobs/{id}`, even though
nothing was deleted — the old SQLite file is untouched inside the data
volume, just no longer read once the app is pointed at MySQL instead. If you
want continuity across a switch, migrate the `jobs` table rows by hand (both
schemas are simple: `id`, `status`, `data` (the JSON blob), `created_at`,
`updated_at` — a straight `SELECT` from one and `INSERT` into the other
works) before flipping `MYSQL_DSN` on.
