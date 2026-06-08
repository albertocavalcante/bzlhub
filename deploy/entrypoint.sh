#!/bin/sh
# Default entrypoint for the bzlhub container. BZLHUB_* env vars come
# from Dockerfile ENV (and may be overridden by compose / k8s env).
# Trust the ENV layer as the single source of truth — no defaults
# here.
set -eu
set -- serve --addr "$BZLHUB_BIND" --root "$BZLHUB_ROOT" --db "$BZLHUB_DB"
[ -n "${BZLHUB_MIRROR_BASE_URL:-}" ] && set -- "$@" --mirror-base-url "$BZLHUB_MIRROR_BASE_URL"
exec /usr/local/bin/bzlhub "$@"
