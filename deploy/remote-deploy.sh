#!/usr/bin/env bash
# Runs on the test server as the deploy user. stdin carries only the registry token (first line).
set -euo pipefail
cd /opt/vera
IFS= read -r PAT || true
if ! printf '%s' "$PAT" | docker login ghcr.io -u "$GHCR_USER" --password-stdin; then
  echo "docker login to ghcr.io failed for $GHCR_USER" >&2
  exit 1
fi
unset PAT
docker network inspect vera-archive >/dev/null 2>&1 || docker network create vera-archive >/dev/null
mkdir -p /opt/vera/sdk
prev="$(cat .current_image 2>/dev/null || true)"
deploy() {
  VERA_IMAGE="$1" docker compose -f compose.yml pull || return 1
  VERA_IMAGE="$1" docker compose -f compose.yml up -d --remove-orphans || return 1
  for i in $(seq 1 40); do
    if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then return 0; fi
    sleep 5
  done
  return 1
}
if deploy "$NEW_IMAGE"; then
  echo "$NEW_IMAGE" > .current_image
  echo "deployed $NEW_IMAGE"
  docker image prune -f >/dev/null
else
  echo "readyz failed for $NEW_IMAGE" >&2
  docker compose -f compose.yml logs --tail=80 vera >&2 || true
  if [ -n "$prev" ]; then
    echo "rolling back to $prev" >&2
    deploy "$prev" || echo "rollback also unhealthy" >&2
  fi
  exit 1
fi
