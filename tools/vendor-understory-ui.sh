#!/usr/bin/env bash
# Refresh Go vendors and stage the built Understory UI that its tagged module
# intentionally omits. A plain `go mod vendor` leaves only the stub HTML.
set -euo pipefail

cd "$(dirname "$0")/.."

understory_source=${UNDERSTORY_SOURCE:-../understory}
required_version=$(go list -mod=mod -m -f '{{.Version}}' github.com/albertocavalcante/understory)
source_tag=$(git -C "$understory_source" describe --tags --exact-match)
if [[ $source_tag != "$required_version" ]]; then
	echo "understory UI source is $source_tag; go.mod requires $required_version" >&2
	exit 1
fi

pnpm --dir "$understory_source/web" install --frozen-lockfile
pnpm --dir "$understory_source/web" build
go mod vendor

bundle=$understory_source/pkg/understory/ui/web/build
destination=vendor/github.com/albertocavalcante/understory/pkg/understory/ui/web/build
cp -R "$bundle/." "$destination/"

# A stub index.html still embeds and serves 200 for missing assets. Assert
# that at least one actual JavaScript asset was copied.
if ! find "$destination/_app/immutable" -name '*.js' -type f -print -quit | grep -q .; then
	echo "understory UI bundle has no JavaScript assets" >&2
	exit 1
fi
