# go-bcr-mirror

Pure-Go library for cloning, syncing, and reading a BCR-shape git
registry mirror (typically `github.com/bazelbuild/bazel-central-registry`).

## Why

Bazel registries are git repos. Their HEAD evolves. Tools that need to
know "what's in upstream BCR right now?" can either HTTP-poll
`metadata.json` files one by one OR clone the repo once and read
locally. This library is the second option.

## Use cases

- Bazel registry servers that back metadata reads with a local clone
  (canopy uses this for its bcrmirror backend).
- Drift detectors that compute "behind by N" via `git log` over
  `metadata.json` files — cryptographically traceable + sub-100ms per
  module instead of network-bound.
- Airgap pipelines that periodically sync upstream into an internal
  mirror, then serve from the internal mirror.

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
)

func main() {
    ctx := context.Background()

    m := bcrmirror.New(
        "/var/lib/bcr-mirror",
        "https://github.com/bazelbuild/bazel-central-registry",
    )

    receipt, err := m.Clone(ctx, bcrmirror.CloneOptions{})
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("cloned at %s (%d bytes, %v)\n",
        receipt.SHA, receipt.Bytes, receipt.Duration)

    metadata, err := m.ReadModuleMetadata(ctx, "bazel_skylib")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("bazel_skylib metadata: %s\n", string(metadata))
}
```

## Status

Pre-1.0. API may change between v0.x minor versions. See
[CHANGELOG.md](CHANGELOG.md) for per-release notes.

## License

MIT 2026. See [LICENSE](LICENSE).
