# Contributing

Issues and PRs are welcome — this is a small, single-maintainer project, so
please open an issue before a large PR to make sure the approach makes sense
first; small fixes/docs/tests are fine to send directly.

## Setup

See the root [`README.md`](README.md#development) for prerequisites and how
to run the backend/frontend locally, and [`docs/testing.md`](docs/testing.md)
for what each test layer covers and how to run it.

## Before opening a PR

```bash
cd backend && go vet ./... && go test ./...
npm ci && npm --workspace frontend run test:ci && npm --workspace frontend run build
```

CI runs the same checks plus an API-level and a real-browser end-to-end pass
(see [`docs/testing.md`](docs/testing.md)) — if you're touching anything
UI-facing, driving it in an actual browser locally first will catch more than
the unit tests alone (see that doc for why; it's caught real shipping bugs
before).

## Commit style

Short, imperative summary line (`feat:`/`fix:`/`docs:`/`chore:` prefixes are
common in this repo's history but not enforced); explain the *why* in the
body when it's not obvious from the diff.

## Reporting a security issue

See [`SECURITY.md`](SECURITY.md) — please don't open a public issue for
anything that looks like a vulnerability.
