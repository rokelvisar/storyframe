#!/usr/bin/env bash
# End-to-end smoke test for the whole pipeline against a real container.
#
#   scripts/e2e.sh [--keep] [--image IMG] [--port PORT]
#
# Drives: create job -> POST overview collage -> POST audio -> tus-upload every
# segment (raw tus protocol via curl) -> poll until the job is `complete` ->
# assert every segment produced a granular collage on disk.
#
# Requires: docker, ffmpeg, curl, jq, base64.
set -euo pipefail

KEEP=0
IMAGE="video-extractor:e2e"
PORT=18080
BUILD=1
while [ $# -gt 0 ]; do
  case "$1" in
    --keep) KEEP=1 ;;
    --image) IMAGE="$2"; BUILD=0; shift ;;
    --port) PORT="$2"; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
CID=""
BASE="http://127.0.0.1:${PORT}"

cleanup() {
  [ -n "$CID" ] && docker rm -f "$CID" >/dev/null 2>&1 || true
  [ "$KEEP" -eq 1 ] || rm -rf "$WORK"
}
trap cleanup EXIT

for bin in docker ffmpeg curl jq base64; do
  command -v "$bin" >/dev/null || { echo "missing dependency: $bin" >&2; exit 1; }
done

echo "==> workdir $WORK"

if [ "$BUILD" -eq 1 ]; then
  echo "==> building $IMAGE"
  docker build -q -t "$IMAGE" "$ROOT" >/dev/null
fi

echo "==> generating a 40s sample video"
SAMPLE="$WORK/sample.mp4"
ffmpeg -v error -y \
  -f lavfi -i "testsrc=size=640x360:rate=25:duration=40" \
  -f lavfi -i "sine=frequency=440:duration=40" \
  -c:v libx264 -pix_fmt yuv420p -c:a aac -movflags +faststart "$SAMPLE"
SIZE=$(stat -c%s "$SAMPLE")
DURATION=40

echo "==> starting container"
CID=$(docker run -d --rm -p "${PORT}:8080" \
  -e ANALYSIS_PROVIDER=noop -e FFMPEG_WORKERS=3 \
  "$IMAGE")

for i in $(seq 1 60); do
  curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break
  sleep 1
  [ "$i" -eq 60 ] && { echo "server did not come up"; docker logs "$CID"; exit 1; }
done
echo "    up"

echo "==> POST /api/v1/jobs"
JOB=$(curl -fsS -X POST "$BASE/api/v1/jobs" -H 'content-type: application/json' \
  -d "{\"filename\":\"sample.mp4\",\"sizeBytes\":$SIZE,\"durationSec\":$DURATION,\"segmentSeconds\":10}")
JID=$(echo "$JOB" | jq -r '.job.id')
NSEG=$(echo "$JOB" | jq '.job.segments | length')
echo "    job=$JID segments=$NSEG"
[ "$NSEG" -ge 3 ] || { echo "expected >=3 segments"; exit 1; }

echo "==> building an overview collage + meta"
COLLAGE="$WORK/collage.jpg"
ffmpeg -v error -y -i "$SAMPLE" \
  -vf "fps=1/2,scale=240:-1,tile=4x5" -frames:v 1 "$COLLAGE"
META=$(jq -n --argjson cols 4 --argjson rows 5 --argjson cw 240 --argjson ch 135 '
  { cols:$cols, rows:$rows, cellWidth:$cw, cellHeight:$ch,
    frames: [ range(0;20) | { t: (. * 2), row: (./4|floor), col: (.%4),
              x: ((.%4)*240), y: ((./4|floor)*135), w:240, h:135,
              label: "00:00:0\(.)" } ] }')
curl -fsS -X POST "$BASE/api/v1/jobs/$JID/overview" \
  -F "collage=@$COLLAGE;type=image/jpeg" -F "meta=$META" >/dev/null
echo "    overview accepted"

echo "==> extracting + POSTing audio"
AUDIO="$WORK/audio.webm"
ffmpeg -v error -y -i "$SAMPLE" -vn -c:a libopus -b:a 24k "$AUDIO" 2>/dev/null \
  || ffmpeg -v error -y -i "$SAMPLE" -vn -c:a libvorbis "$AUDIO"
curl -fsS -X POST "$BASE/api/v1/jobs/$JID/audio" -F "audio=@$AUDIO" >/dev/null
echo "    audio accepted"

echo "==> tus-uploading $NSEG segments (interesting ones first)"
tus_upload_segment() {
  local start="$1" end="$2" index="$3" segid="$4"
  local len=$(( end - start ))
  local part="$WORK/part_$index.bin"
  dd if="$SAMPLE" of="$part" bs=1M iflag=skip_bytes,count_bytes \
     skip="$start" count="$len" status=none

  local md
  md="jobId $(printf %s "$JID" | base64 -w0)"
  md="$md,segmentId $(printf %s "$segid" | base64 -w0)"
  md="$md,partIndex $(printf %s "$index" | base64 -w0)"

  local loc
  loc=$(curl -fsS -D - -o /dev/null -X POST "$BASE/files/" \
    -H "Tus-Resumable: 1.0.0" -H "Upload-Length: $len" -H "Upload-Metadata: $md" \
    | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
  [ -n "$loc" ] || { echo "no Location for segment $index"; exit 1; }
  case "$loc" in http*) : ;; *) loc="$BASE$loc" ;; esac

  curl -fsS -X PATCH "$loc" \
    -H "Tus-Resumable: 1.0.0" -H "Upload-Offset: 0" \
    -H "Content-Type: application/offset+octet-stream" \
    --data-binary "@$part" -o /dev/null
}

# order: highest interestScore first (noop provider peaks in the middle)
while read -r s e i id; do
  echo "    segment $i  bytes $s-$e"
  tus_upload_segment "$s" "$e" "$i" "$id"
done < <(echo "$JOB" | jq -r '.job.segments | sort_by(-.interestScore)[] | "\(.byteStart) \(.byteEnd) \(.index) \(.id)"')

echo "==> polling for completion"
STATUS=""
for i in $(seq 1 90); do
  J=$(curl -fsS "$BASE/api/v1/jobs/$JID")
  STATUS=$(echo "$J" | jq -r '.status')
  DONE=$(echo "$J" | jq '[.segments[] | select(.status=="done")] | length')
  echo "    [$i] status=$STATUS done=$DONE/$NSEG"
  [ "$STATUS" = "complete" ] && break
  [ "$STATUS" = "error" ] && { echo "job errored: $(echo "$J" | jq -r .error)"; exit 1; }
  sleep 2
done
[ "$STATUS" = "complete" ] || { echo "did not complete in time"; docker logs "$CID" | tail -40; exit 1; }

echo "==> asserting granular collages exist"
MISSING=0
for url in $(echo "$J" | jq -r '.segments[].granularCollageUrl // empty'); do
  path="/data${url#/media}"
  if docker exec "$CID" test -s "$path"; then
    echo "    ok  $url"
  else
    echo "    MISSING $url ($path)"; MISSING=1
  fi
done
COLLAGE_COUNT=$(echo "$J" | jq '[.segments[].granularCollageUrl // empty] | length')
[ "$COLLAGE_COUNT" -eq "$NSEG" ] || { echo "only $COLLAGE_COUNT/$NSEG collages"; MISSING=1; }
[ "$MISSING" -eq 0 ] || exit 1

echo
echo "PASS — job $JID complete, $COLLAGE_COUNT/$NSEG granular collages, story: $(echo "$J" | jq -r '.overviewStory' | head -c 80)…"
