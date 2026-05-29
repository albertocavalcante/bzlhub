#!/bin/sh
# Default entrypoint for the canopy container. CANOPY_* env vars come
# from Dockerfile ENV (and may be overridden by compose / k8s env).
# Trust the ENV layer as the single source of truth — no defaults
# here.
set -eu
set -- serve --addr "$CANOPY_BIND" --root "$CANOPY_ROOT" --db "$CANOPY_DB"
[ -n "${CANOPY_MIRROR_BASE_URL:-}" ] && set -- "$@" --mirror-base-url "$CANOPY_MIRROR_BASE_URL"
exec /usr/local/bin/canopy "$@"
