#!/usr/bin/env python3
"""Deploy this app as a Portainer compose stack — an optional example, see
deploy/README.md. Uses the Portainer *stack* API (not the raw container API):
some Portainer Agent setups break a direct POST /containers/{id}/start for
off-LAN callers, so this lets Portainer bring the container up server-side
instead.

Env:
  PORTAINER_URL          e.g. https://portainer.example.com        (required)
  PORTAINER_TOKEN        Portainer API key (X-API-Key header)       (required)
  PORTAINER_ENDPOINT_ID  numeric endpoint id, default 1
  IMAGE_TAG              image tag to deploy, default "latest"
  IMAGE_REPO             image to deploy, e.g. ghcr.io/<you>/<repo>
  STACK_NAME             Portainer stack name, default "app"
  PORTAINER_STACK_ID     optional; if unset the stack is looked up by name

  # forwarded into the stack's environment (only those that are set):
  ANALYSIS_PROVIDER, LITELLM_BASE_URL, LITELLM_API_KEY,
  LITELLM_VLM_MODEL, LITELLM_WHISPER_MODEL, FFMPEG_WORKERS,
  IMMICH_BASE_URL, IMMICH_API_KEY, IMMICH_WRITEBACK,
  TRANSCRIBE_LANGUAGE_HINT, OUTPUT_LANGUAGE, MYSQL_DSN

If your registry is private, add its credentials as a Portainer Registry
entry first (Portainer UI: Registries → Add registry) — this script assumes
that's already done.
"""
import json
import os
import sys
import urllib.error
import urllib.request

def _req(name):
    v = os.environ.get(name, "").strip()
    if not v:
        sys.exit(f"deploy-portainer: required env {name} is empty")
    return v


def _int(name, default):
    v = os.environ.get(name, "").strip()
    return int(v) if v else default


PORTAINER_URL = _req("PORTAINER_URL").rstrip("/")
PORTAINER_TOKEN = _req("PORTAINER_TOKEN")
ENDPOINT_ID = _int("PORTAINER_ENDPOINT_ID", 1)
IMAGE_TAG = os.getenv("IMAGE_TAG", "latest")
IMAGE_REPO = _req("IMAGE_REPO")
STACK_NAME = os.getenv("STACK_NAME", "app")
STACK_ID = os.getenv("PORTAINER_STACK_ID", "").strip()

COMPOSE_PATH = os.path.join(os.path.dirname(__file__), "stack.yml")

FORWARD_ENV = [
    "ANALYSIS_PROVIDER",
    "LITELLM_BASE_URL",
    "LITELLM_API_KEY",
    "LITELLM_VLM_MODEL",
    "LITELLM_WHISPER_MODEL",
    "FFMPEG_WORKERS",
    "IMMICH_BASE_URL",
    "IMMICH_API_KEY",
    "IMMICH_WRITEBACK",
    "TRANSCRIBE_LANGUAGE_HINT",
    "OUTPUT_LANGUAGE",
    "MYSQL_DSN",
]


def api(method, path, body=None):
    url = f"{PORTAINER_URL}{path}"
    headers = {"X-API-Key": PORTAINER_TOKEN}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read().decode()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        print(f"HTTP {e.code} on {method} {path}:\n{e.read().decode()}")
        raise


def render_compose():
    with open(os.path.abspath(COMPOSE_PATH), encoding="utf-8") as f:
        text = f.read()
    pinned = f"{IMAGE_REPO}:{IMAGE_TAG}"
    out = []
    for line in text.splitlines():
        if line.strip().startswith("image:"):
            indent = line[: len(line) - len(line.lstrip())]
            out.append(f"{indent}image: {pinned}")
        else:
            out.append(line)
    return "\n".join(out) + "\n"


def stack_env():
    return [{"name": k, "value": os.environ[k]} for k in FORWARD_ENV if os.environ.get(k)]


def find_stack_id():
    if STACK_ID:
        return int(STACK_ID)
    for s in api("GET", "/api/stacks") or []:
        if s.get("Name") == STACK_NAME:
            print(f"found existing stack '{STACK_NAME}' id={s['Id']} (endpoint {s.get('EndpointId')})")
            return int(s["Id"])
    return None


def create(compose, env):
    print(f"creating stack '{STACK_NAME}' on endpoint {ENDPOINT_ID}")
    try:
        res = api(
            "POST",
            f"/api/stacks/create/standalone/string?endpointId={ENDPOINT_ID}",
            {"name": STACK_NAME, "stackFileContent": compose, "env": env},
        )
    except urllib.error.HTTPError as e:
        if e.code != 404:
            raise
        print("newer create route 404 — falling back to legacy /api/stacks")
        res = api(
            "POST",
            f"/api/stacks?type=2&method=string&endpointId={ENDPOINT_ID}",
            {"Name": STACK_NAME, "StackFileContent": compose, "Env": env},
        )
    print(f"created stack id={res.get('Id')}")
    print(f"::notice::record PORTAINER_STACK_ID={res.get('Id')} as a repo secret")


def update(stack_id, compose, env):
    print(f"updating stack id={stack_id} on endpoint {ENDPOINT_ID}")
    api(
        "PUT",
        f"/api/stacks/{stack_id}?endpointId={ENDPOINT_ID}",
        {"stackFileContent": compose, "env": env, "prune": True, "pullImage": True},
    )
    print("stack updated (pullImage=true, prune=true)")


def main():
    compose = render_compose()
    env = stack_env()
    print(f"deploying {IMAGE_REPO}:{IMAGE_TAG} with env keys: {[e['name'] for e in env]}")
    sid = find_stack_id()
    if sid is None:
        create(compose, env)
    else:
        update(sid, compose, env)
    print("done")


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError:
        sys.exit(1)
