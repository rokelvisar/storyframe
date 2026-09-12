# Deploying it yourself

`deploy/` is an optional example for running this behind
[Portainer](https://www.portainer.io/) (a Docker management UI many
self-hosters already run) and a reverse proxy — not a requirement. The
simplest way to run this app is still:

```bash
docker compose up
```

(see the root [`README.md`](../README.md) for the full quickstart).

## The Portainer example

`deploy/stack.yml` is a plain Docker Compose file with the app's environment
variables templated (`${VAR:-default}`) so it can be deployed as a Portainer
**Stack** — a good fit if you already manage other containers that way, or
want `git push` to redeploy without SSHing into the host.

`deploy/deploy-portainer.py` drives Portainer's Stack API directly: it reads
`deploy/stack.yml`, pins the image tag, and creates or updates the stack via
`POST`/`PUT /api/stacks`. It's a plain script, not tied to any particular CI
provider — run it locally, or wire it into your own CI. Required environment:

| Env | Meaning |
|---|---|
| `PORTAINER_URL` | Your Portainer instance, e.g. `https://portainer.example.com` |
| `PORTAINER_TOKEN` | A Portainer API key (*My account → Access tokens*) |
| `PORTAINER_ENDPOINT_ID` | Numeric id of the Docker environment to deploy to |
| `PORTAINER_STACK_ID` | *(optional)* Numeric stack id, if you already created one. Omit it on the first run — the script looks the stack up by name, creates it if missing, and prints the id it assigned so you can pin it for next time |
| `IMAGE_TAG` | Image tag to deploy, e.g. a commit SHA or `latest` |

Plus whichever of the app's own env vars you want forwarded into the
container's environment (`ANALYSIS_PROVIDER`, `LITELLM_*`, `IMMICH_*`,
`MYSQL_DSN`, etc. — see [`docs/configuration.md`](../docs/configuration.md)):
set them in your own environment before running the script, and add their
names to `FORWARD_ENV` at the top of `deploy-portainer.py` if you add new
ones.

```bash
PORTAINER_URL=https://portainer.example.com \
PORTAINER_TOKEN=... \
PORTAINER_ENDPOINT_ID=1 \
IMAGE_TAG=latest \
ANALYSIS_PROVIDER=litellm LITELLM_BASE_URL=... LITELLM_API_KEY=... \
LITELLM_VLM_MODEL=... LITELLM_WHISPER_MODEL=... \
python3 deploy/deploy-portainer.py
```

## A note on reverse proxies

If you put this behind Nginx, Caddy, Traefik, or an NPM (Nginx Proxy Manager)
instance, remember uploads are large (2 GB+) and long-running (tus `PATCH`
requests can stay open for a while on a slow connection). Whatever you use,
make sure it doesn't impose a low body-size limit or a short proxy timeout —
for an Nginx-based proxy, something like:

```
client_max_body_size 0;
proxy_request_buffering off;
proxy_read_timeout 3600s;
proxy_send_timeout 3600s;
```

## CI/CD

This repo's own `.github/workflows/ci.yml` builds and tests the image on
every push (see [`docs/testing.md`](../docs/testing.md)) and publishes it to
`ghcr.io/<your-fork>/<repo-name>` — it doesn't deploy anywhere, since there's
no one deployment target that makes sense for every fork. Add a `deploy` job
(or a separate workflow) that calls `deploy-portainer.py`, `docker compose
-f ... up -d` over SSH, or whatever fits your own infrastructure.
