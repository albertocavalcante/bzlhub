#!/usr/bin/env bash
# Build the SvelteKit bundle and stage it where the Go binary embeds it from.
#
# In a script rather than a justfile recipe because recipe bodies are invisible
# to shellcheck and shfmt, and this one has enough steps to get wrong quietly:
# a build that half-succeeds leaves a stale internal/embed/ui, and the Go build
# then embeds yesterday's UI without complaint.
set -euo pipefail

cd "$(dirname "$0")/.."

pnpm --dir ui install --frozen-lockfile
pnpm --dir ui run build

# Replace rather than merge. cp -R onto an existing tree leaves files the new
# build no longer produces, so a renamed asset would ship alongside its
# predecessor and the stale one could win a lookup.
rm -rf internal/embed/ui
mkdir -p internal/embed/ui
cp -R ui/build/. internal/embed/ui/

# A silent empty copy is the failure this guards: the Go embed then succeeds
# with nothing in it and the UI serves blank pages.
count=$(find internal/embed/ui -type f | wc -l | tr -d ' ')
if [[ ${count} -eq 0 ]]; then
	echo "ui-embed: staged 0 files -- the bundle did not build" >&2
	exit 1
fi
echo "ui-embed: staged ${count} files"
